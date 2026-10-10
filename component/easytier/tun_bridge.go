// FORK(easytier-tun): 把宿主 TUN 设备与 EasyTier 实例的包面接起来。
//
// 上游从不调用 Instance.SendPacket/ReceivePacket，也没有建过设备；本文件补上这一段：
//
//	device.Read        → instance.SendPacket    （宿主 → overlay）
//	instance.ReceivePacket → device.Write       （overlay → 宿主）
//
// 设备参数沿用 easytier-go 官方范例 examples/tun：宿主建设备、AutoRoute=false（绝不抢默认路由）、MTU 1380。
//
// 路由分两半（2026-10-09 实测 ✓）：
//   - 出向：`Inet4RouteAddress` 只在 Linux 的 auto-route/redirect 规则里被消费，darwin 上毫无作用；
//   - 回程：Linux 内核会因接口配了地址自动派生网段路由，macOS **不会** —— 必须由 tun_route.go 自己装。
//
// 规格与验收标准见 docs/easytier_tun_spec.md。
package easytier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/log"

	tun "github.com/metacubex/sing-tun"
)

const (
	// defaultTunMTU 与 EasyTier 默认 MTU 及官方范例保持一致。
	defaultTunMTU = 1380
	// tunDeviceNamePrefix 用于生成设备名，避免与 mihomo 自己的 TUN 撞名。
	tunDeviceNamePrefix = "easytier"
	// maxTunPacketSize 是单个 IP 包的最大长度（IPv4 理论上限）。
	maxTunPacketSize = 65535
	// minIPv4PacketLen 是 IPv4 头部最小长度，用于快速过滤非 IPv4 数据。
	minIPv4PacketLen = 20
)

// packetPlane 是 EasyTier 实例的包面；*corehost.Instance 天然满足该接口。
type packetPlane interface {
	SendPacket(ctx context.Context, packet []byte) error
	ReceivePacket(ctx context.Context) ([]byte, error)
}

// packetDevice 是宿主 TUN 设备；sing-tun 的 Tun 接口满足它。
type packetDevice interface {
	io.Reader
	io.Writer
	Close() error
}

// TunDeviceOptions 描述宿主 TUN 设备。
type TunDeviceOptions struct {
	Prefix netip.Prefix   // 设备地址（overlay IPv4 前缀）
	MTU    int            // 0 表示使用 defaultTunMTU
	Routes []netip.Prefix // pin 进这块设备的前缀（可为空）
}

// CreateTunDevice 创建 TUN 设备。需要 root 或 CAP_NET_ADMIN；失败时返回可读错误。
func CreateTunDevice(options TunDeviceOptions) (packetDevice, error) {
	mtu := options.MTU
	if mtu <= 0 {
		mtu = defaultTunMTU
	}
	name := tun.CalculateInterfaceName(tunDeviceNamePrefix)
	device, err := tun.New(tun.Options{
		Name:              name,
		Inet4Address:      []netip.Prefix{options.Prefix},
		MTU:               uint32(mtu),
		GSO:               false,
		AutoRoute:         false, // 只装显式指定的前缀，绝不抢默认路由
		StrictRoute:       false,
		Inet4RouteAddress: options.Routes,
		// InterfaceMonitor 留空：与 mihomo 自身 TUN 在既未开 auto-route 也未开
		// auto-detect-interface 时的行为一致（listener/sing_tun/server.go）。
	})
	if err != nil {
		return nil, fmt.Errorf("easytier: create TUN %q failed (root or CAP_NET_ADMIN required): %w", name, err)
	}
	return device, nil
}

// runTunBridge 双向搬运裸 IP 包，直到 ctx 取消或任一侧出错。
// ctx 取消时会关闭设备（TUN 的 Read 没有 ctx 参数，只能靠关闭唤醒）。
func runTunBridge(parent context.Context, device packetDevice, plane packetPlane, local netip.Prefix) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	if cleanup, err := ensureOverlayRoute(local, local.Addr()); err != nil {
		// 装不上不阻断：Linux 不需要它，darwin 上缺了它只影响"回给 overlay 对端"的包。
		log.Warnln("[EasyTier] overlay return route not installed: %v", err)
	} else {
		defer cleanup()
	}

	go func() {
		<-ctx.Done()
		_ = device.Close()
	}()

	results := make(chan error, 2)
	go func() { results <- copyDeviceToPlane(ctx, device, plane) }()
	go func() { results <- copyPlaneToDevice(ctx, plane, &ingressSink{device: device, local: local}) }()

	first := <-results
	cancel() // 让另一条循环尽快退出
	second := <-results
	if parent.Err() != nil {
		return nil // 上层主动关闭，不算错误
	}
	if first != nil {
		return first
	}
	return second
}

// ingressSink 把 overlay 入向包交给宿主。
//
// FORK(easytier-tun): 优先投喂宿主 mihomo 的 TUN 栈（栈没有注入口，故由 WrapTunForIngress 多路复用），
// 由 mihomo 统一做路由 / 规则 / NAT —— 这是"本机给别人当网关"唯一在各平台都成立的路径；
// 没有可用栈时退回写进本地 TUN 设备（Linux 上内核会转发 + 容器 NAT，macOS 上只够本机用）。
//
// FORK(easytier-tun): 目的地是**本机自己**的包**要**投喂 —— 本机经远端出口的连接，
// 回程包的目的地就是本机 overlay 地址（连接由本节点的数据面发起），不投喂它就永远完不成。
//
// 只有"目的地是**另一个** overlay 节点"的包才跳过：投喂那些会让 mihomo 反过来再拨同一个
// overlay 地址、绕回 overlay —— 那类用法应交给 EasyTier 自身的子网代理（proxy-networks）。
type ingressSink struct {
	device    io.Writer    // 本地 TUN 设备（兜底）
	local     netip.Prefix // 本节点 overlay 网段，可为零值
	localOnce sync.Once    // 只提示一次降级
	seen      atomic.Uint64
	injected  atomic.Uint64
	logged    atomic.Uint64
}

func (s *ingressSink) put(packet []byte) error {
	sink := CurrentIngressSink()
	if s.seen.Add(1)%100 == 1 {
		// 每 100 个包一条：sink 是否就位、本节点网段解析成什么、投喂了多少（排障用）。
		log.Debugln("[EasyTier] overlay ingress: sink=%v local=%s total=%d injected=%d",
			sink != nil, s.local, s.seen.Load(), s.injected.Load())
	}
	if sink != nil && !packetDestIsOtherOverlayPeer(packet, s.local) {
		if sink.Inject(packet) {
			s.injected.Add(1)
		} else {
			log.Debugln("[EasyTier] overlay ingress packet dropped: sink queue is full")
		}
		return nil
	}
	if sink == nil {
		s.localOnce.Do(func() {
			log.Debugln("[EasyTier] no host TUN ingress sink; overlay ingress goes to the local TUN device")
		})
	}
	if s.logged.Add(1) <= 20 {
		// 兜底路径的包（前 20 个）：看清楚目的地，并核对校验和 ——
		// 内核静默丢弃的最常见原因就是校验和不对（guest 可能按"网卡会补"的方式交包）。
		log.Debugln("[EasyTier] overlay ingress -> local TUN: %s ip_sum=%v l4_sum=%v",
			describeIPv4Packet(packet), ipv4ChecksumValid(packet), l4ChecksumValid(packet))
	}
	_, err := s.device.Write(packet)
	return err
}

// checksum 按 RFC 1071 计算互联网校验和（返回 true 表示校验通过）。
func checksum(header []byte, sum uint32) uint32 {
	for i := 0; i+1 < len(header); i += 2 {
		sum += uint32(header[i])<<8 | uint32(header[i+1])
	}
	if len(header)%2 == 1 {
		sum += uint32(header[len(header)-1]) << 8
	}
	return sum
}

func foldedChecksum(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// ipv4ChecksumValid 校验 IPv4 首部校验和（内核会静默丢弃校验和错误的包）。
func ipv4ChecksumValid(packet []byte) bool {
	if len(packet) < minIPv4PacketLen {
		return false
	}
	headerLength := int(packet[0]&0x0f) * 4
	if headerLength < minIPv4PacketLen || len(packet) < headerLength {
		return false
	}
	return foldedChecksum(checksum(packet[:headerLength], 0)) == 0
}

// l4ChecksumValid 校验 TCP/UDP 校验和（含伪首部）；其他协议返回 true（不判定）。
func l4ChecksumValid(packet []byte) bool {
	if len(packet) < minIPv4PacketLen {
		return false
	}
	headerLength := int(packet[0]&0x0f) * 4
	if len(packet) < headerLength+8 {
		return true
	}
	protocol := packet[9]
	if protocol != 6 && protocol != 17 {
		return true
	}
	segment := packet[headerLength:]
	sum := checksum(packet[12:20], 0) // 源/目的地址
	sum += uint32(protocol)
	sum += uint32(len(segment))
	sum = checksum(segment, sum)
	return foldedChecksum(sum) == 0
}

// describeIPv4Packet 用可读形式描述一个 IPv4 包的协议与目的地址:端口（排障用）。
func describeIPv4Packet(packet []byte) string {
	if len(packet) < minIPv4PacketLen {
		return fmt.Sprintf("len=%d (not IPv4)", len(packet))
	}
	protocol := "proto" + strconv.Itoa(int(packet[9]))
	headerLength := int(packet[0]&0x0f) * 4
	if headerLength < minIPv4PacketLen || len(packet) < headerLength {
		return fmt.Sprintf("%s len=%d", protocol, len(packet))
	}
	source := netip.AddrFrom4([4]byte{packet[12], packet[13], packet[14], packet[15]})
	destination := netip.AddrFrom4([4]byte{packet[16], packet[17], packet[18], packet[19]})
	var sourcePort, destinationPort uint16
	flags := ""
	if len(packet) >= headerLength+4 {
		sourcePort = uint16(packet[headerLength])<<8 | uint16(packet[headerLength+1])
		destinationPort = uint16(packet[headerLength+2])<<8 | uint16(packet[headerLength+3])
	}
	if packet[9] == 6 && len(packet) >= headerLength+14 {
		flags = fmt.Sprintf(" flags=0x%02x", packet[headerLength+13])
	}
	return fmt.Sprintf("%s %s:%d -> %s:%d%s len=%d", protocol, source, sourcePort, destination, destinationPort, flags, len(packet))
}

// packetDestIsOtherOverlayPeer 判断 IPv4 包的目的地址是否落在 prefix 内（prefix 为零值时恒 false）。
// packetDestIsOtherOverlayPeer 判断目的地是否**落在本节点 overlay 网段、但不是本机自己**。
// 这类包不投喂宿主栈（会绕回 overlay）；而目的地在网段内**且就是本机**的包必须投喂。
func packetDestIsOtherOverlayPeer(packet []byte, prefix netip.Prefix) bool {
	if !prefix.IsValid() || len(packet) < 20 {
		return false
	}
	dst := netip.AddrFrom4([4]byte{packet[16], packet[17], packet[18], packet[19]})
	if !prefix.Contains(dst) {
		return false
	}
	return dst != prefix.Addr().Unmap()
}

func copyDeviceToPlane(ctx context.Context, device io.Reader, plane packetPlane) error {
	buffer := make([]byte, maxTunPacketSize)
	for {
		length, err := device.Read(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("easytier: read TUN packet: %w", err)
		}
		if length == 0 || !isIPv4Packet(buffer[:length]) {
			continue
		}
		if err := plane.SendPacket(ctx, buffer[:length]); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("easytier: send TUN packet to EasyTier: %w", err)
		}
	}
}

func copyPlaneToDevice(ctx context.Context, plane packetPlane, sink *ingressSink) error {
	for {
		packet, err := plane.ReceivePacket(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("easytier: receive packet from EasyTier: %w", err)
		}
		if len(packet) == 0 {
			continue
		}
		if err := sink.put(packet); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("easytier: write EasyTier packet to TUN: %w", err)
		}
	}
}

func isIPv4Packet(packet []byte) bool {
	return len(packet) >= minIPv4PacketLen && packet[0]>>4 == 4
}

// TunBridge 持有 TUN 设备与两条搬运循环的生命周期。
type TunBridge struct {
	device packetDevice
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// StartTunBridge 创建设备并启动双向搬运；调用方收尾时必须调用 Close。
func StartTunBridge(parent context.Context, options TunDeviceOptions, plane packetPlane) (*TunBridge, error) {
	device, err := CreateTunDevice(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	bridge := &TunBridge{device: device, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(bridge.done)
		go func() {
			// 延迟自检：启动瞬间宿主 TUN 可能还没建好，15 秒后再报一次真实状态（Info 级，便于排障）。
			time.Sleep(15 * time.Second)
			log.Infoln("[EasyTier] ingress sink ready=%v local=%s", CurrentIngressSink() != nil, options.Prefix)
		}()
		bridge.err = runTunBridge(ctx, device, plane, options.Prefix)
	}()
	return bridge, nil
}

// Close 停止搬运并关闭设备；幂等，可重复调用。
func (b *TunBridge) Close() error {
	if b == nil {
		return nil
	}
	b.cancel()
	<-b.done
	if b.device == nil {
		return nil
	}
	device := b.device
	b.device = nil
	return device.Close()
}

// Err 返回搬运循环结束时的错误（须在 Close 之后读）。
func (b *TunBridge) Err() error {
	if b == nil {
		return nil
	}
	return b.err
}
