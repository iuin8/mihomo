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
//   - 回程：Linux 内核会因接口配了地址自动派生网段路由，macOS **不会** —— 必须由 tun_route.go 自己装；
//   - 出向（tun-routes）：**两侧内核都不会派生**（那些前缀不在接口网段里）⇒ 同样必须自己装
//     （ensurePinnedRoute）。`Inet4RouteAddress` 在这里**不解决问题**：它只在 Linux 的
//     auto-route/redirect 规则里被消费，而本 fork 用 AutoRoute=false，所以两条路都不走。
//
// 规格与验收标准见 docs/easytier/tun-mode-spec.md。
package easytier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

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
	// FORK(easytier-tun): 先清掉 exec 式重启继承来的陈旧 TUN fd（见 tun_stale_darwin.go）——
	// 否则同一个 overlay 地址会被多张接口同时持有，macOS 上该地址直接变得不可用。
	closeStaleOverlayTunFDs(options.Prefix.Addr())
	mtu := options.MTU
	if mtu <= 0 {
		mtu = defaultTunMTU
	}
	name := tun.CalculateInterfaceName(tunDeviceNamePrefix)
	device, err := tun.New(tun.Options{
		Name:              name,
		Inet4Address:      []netip.Prefix{tunAddressPrefix(options.Prefix)},
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
	// FORK(easytier-tun): macOS 的 utun 要求每包带 4 字节地址族头 —— 裸读写会被内核整包丢弃 ✗✓。
	// 详见 tun_packet_darwin.go（这是"fork 自建 TUN 在 macOS 上完全不通"的真正原因 ✓）。
	return wrapPacketDevice(device), nil
}

// runTunBridge 双向搬运裸 IP 包，直到 ctx 取消或任一侧出错。
// ctx 取消时会关闭设备（TUN 的 Read 没有 ctx 参数，只能靠关闭唤醒）。
func runTunBridge(parent context.Context, device packetDevice, plane packetPlane, local netip.Prefix, pinned []netip.Prefix) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// FORK(easytier-tun): 保留这条网段路由 —— **"本机内核 → overlay 对端"那条路仍然需要它** ✓
	// （2026-10-13 验证：删掉它并不能修好 macOS 的本地交付 ✗，所以没有任何理由去掉 ✗✓）。
	if cleanup, err := ensureOverlayRoute(local, local.Addr()); err != nil {
		// 装不上不阻断：Linux 不需要它，darwin 上缺了它只影响"内核发往 overlay 对端"的包。
		log.Warnln("[EasyTier] overlay return route not installed: %v", err)
	} else {
		defer cleanup()
	}
	// FORK(easytier-tun): tun-routes 真的 pin 进设备 —— 此前只塞进 sing-tun 的
	// Inet4RouteAddress，而那个字段在 AutoRoute=false 下没有任何消费者 ⇒ 等于没配（实测踩到）。
	// 空列表时这里一次都不循环，行为与以前完全一致。
	for _, prefix := range pinned {
		cleanup, err := ensurePinnedRoute(prefix, local.Addr())
		if err != nil {
			// 装不上不阻断：packetPlane 的双向搬运仍然工作，只是该前缀不会走这块设备。
			log.Warnln("[EasyTier] pinned route %s not installed: %v", prefix, err)
			continue
		}
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
// 判据（2026-10-13 两容器实验台实测修正 ✓，见 docs/easytier/tun-mode-spec.md）：
//
//   - 目的地**落在本节点 overlay 网段**（含本机自己的地址）⇒ 写 TUN 设备，交给内核。
//     这类包属于内核：本机服务（例如 sshd、mihomo 自己的控制器）与 **EasyTier 自己的数据面
//     监听口**都在内核里 —— guest 把包交给宿主，正是因为那个 socket 在宿主侧。
//     投喂它们进 mihomo 的栈是**错**的：mihomo 会当成新连接再拨一次同一个地址，把
//     数据面握手搅掉（实测：借出口的 delay 请求 ⇒ 数据面 SYN 被 mihomo 截胡 ⇒ 503）；
//     更糟的是内核发起的 flow（本机 → 对端）其回程包也会被投喂 ⇒ 栈里没有连接状态 ⇒ 回 RST。
//
//   - 目的地**不在** overlay 网段（公网 / 其它子网）⇒ 投喂宿主 mihomo 的 TUN 栈，
//     由 mihomo 统一做路由 / 规则 / 出口 —— 这是"本机替对端出网"（exit / 子网代理）的包，
//     也是唯一在各平台都成立的路径（macOS 上内核不转发，见 tun_egress.go 顶注）。
//
// 没有可用栈时一律退回写设备（与以前一致）。
type ingressSink struct {
	device    io.Writer    // 本地 TUN 设备（兜底）
	local     netip.Prefix // 本节点 overlay 网段，可为零值
	localOnce sync.Once    // 只提示一次降级
	seen      atomic.Uint64
	injected  atomic.Uint64
	logged    atomic.Uint64
}

// tunAddressPrefix 决定设备上配多大的地址前缀。
//
// FORK(easytier-tun): **darwin 上用 /32** —— 2026-10-13 实测（干净状态、单张持有者 ✓）：
// 设备上配 /24 时 macOS 会派生 `10.144/24 → utun1025` 的 on-link 网段路由 ✗，于是**连本机自己
// 的 overlay 地址都不再本地交付** ✗ —— `nc -v -z 10.144.0.6 <任意端口>`（本机自连 ✓、家侧拨入 ✓、
// 连系统自带的 ControlCenter:7000 ✓）一律 `Operation timed out` ✓，而包既没进本地 socket ✓
// 也没进隧道 ✓；同一台机器上 mihomo 自己的 TUN（`198.18.0.1/30`、**没有**网段路由 ✓）一切正常 ✓✓。
//
// /32 之后设备只持有**一个本机地址**、不派生网段路由 ✓ ⇒ 内核按正常本机地址做本地交付 ✓✓；
// 对端网段的路由由 ensureOverlayRoute 自己装 ✓（darwin 本来就装 ✓，见 tun_route.go 的说明 ✓）。
// 其它平台保持原前缀 ✓：Linux 依赖内核从接口地址派生网段路由 ✓（实测可用 ✓，别动它 ✗）。
func tunAddressPrefix(prefix netip.Prefix) netip.Prefix {
	if runtime.GOOS == "darwin" && prefix.IsValid() && prefix.Addr().Is4() {
		return netip.PrefixFrom(prefix.Addr(), 32)
	}
	return prefix
}

func (s *ingressSink) put(packet []byte) error {
	sink := CurrentIngressSink()
	total := s.seen.Add(1)
	if sink != nil && !packetDestInOverlay(packet, s.local) {
		injected := sink.Inject(packet)
		if injected {
			s.injected.Add(1)
		}
		// 计数必须在**决策之后**打印：这一行以前在 Inject 之前打印，于是首包永远显示
		// injected=0，被读成"过滤器把包丢了"（实测误导过一次）——那次投喂到底成没成
		// 根本没被记录。前 20 个包连校验和一起打：排障时一眼看出 guest 交来的包是否合法。
		if total%100 == 1 || s.logged.Add(1) <= 20 {
			log.Debugln("[EasyTier] overlay ingress: sink=true local=%s total=%d injected=%d now=%v %s ip_sum=%v l4_sum=%v",
				s.local, total, s.injected.Load(), injected,
				describeIPv4Packet(packet), ipv4ChecksumValid(packet), l4ChecksumValid(packet))
		}
		if !injected {
			log.Debugln("[EasyTier] overlay ingress packet dropped: sink queue is full")
		}
		return nil
	}
	if total%100 == 1 {
		log.Debugln("[EasyTier] overlay ingress: sink=%v local=%s total=%d injected=%d (no host stack sink)",
			sink != nil, s.local, total, s.injected.Load())
	}
	if sink == nil {
		s.localOnce.Do(func() {
			log.Debugln("[EasyTier] no host TUN ingress sink; overlay ingress goes to the local TUN device")
		})
	}
	written, err := s.device.Write(packet)
	if s.logged.Add(1) <= 20 {
		// 兜底路径的包（前 20 个）：目的地、校验和、以及**写设备的结果** ——
		// 校验和不对与写入失败都会让包静默消失（内核丢 / 设备丢），必须分开留证。
		log.Debugln("[EasyTier] overlay ingress -> local TUN: %s ip_sum=%v l4_sum=%v wrote=%d err=%v",
			describeIPv4Packet(packet), ipv4ChecksumValid(packet), l4ChecksumValid(packet), written, err)
	}
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

// packetDestInOverlay 判断目的地是否落在本节点 overlay 网段内（含本机自己的地址）。
// prefix 为零值、包不是 IPv4 时一律 false。
func packetDestInOverlay(packet []byte, prefix netip.Prefix) bool {
	if !prefix.IsValid() || len(packet) < minIPv4PacketLen || packet[0]>>4 != 4 {
		return false
	}
	dst := netip.AddrFrom4([4]byte{packet[16], packet[17], packet[18], packet[19]})
	return prefix.Contains(dst)
}

// packetDestIsOtherOverlayPeer 判断目的地是否**落在本节点 overlay 网段、但不是本机自己**。
// 这类包不投喂宿主栈（会绕回 overlay）；而目的地在网段内**且就是本机**的包必须投喂。
func packetDestIsOtherOverlayPeer(packet []byte, prefix netip.Prefix) bool {
	// 版本 nibble 必须显式判：网段是 IPv4 的，而 helper 直接按 IPv4 偏移取地址 ——
	// 不判版本的话，一个 IPv6 包的 16..20 字节会被误读成 IPv4 目的地址（可能命中网段 ⇒ 误分流）。
	if !prefix.IsValid() || len(packet) < 20 || packet[0]>>4 != 4 {
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
	var logged atomic.Uint64
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
		if logged.Add(1) <= 20 {
			// FORK(easytier-tun): 设备→guest 方向也必须可见 ✓ —— 缺了它就看不出"内核把包又送回隧道"
			// 这类闭环（实测排查 2026-10-13 的 macOS 本地交付问题时正是盲区 ✗）。
			log.Debugln("[EasyTier] TUN device -> overlay: %s", describeIPv4Packet(buffer[:length]))
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
		bridge.err = runTunBridge(ctx, device, plane, options.Prefix, options.Routes)
		if err := bridge.err; err != nil {
			// FORK(easytier-tun): 桥停了就是 overlay 通路断了 —— 这条以前没有任何日志。
			log.Warnln("[EasyTier] TUN bridge stopped: %v", err)
		}
	}()
	installBridgeFinalizer(bridge)
	return bridge, nil
}

// installBridgeFinalizer 让"调用方再也不会调用 Close"的桥也能被收尾。
//
// FORK(easytier-tun, 2026-10-16):**这是设备泄漏的根因** ✗✓。mihomo 换配置时只是替换出站集合
// （tunnel.UpdateProxies 里 `proxies = newProxies`，没有任何 Close ✗）⇒ 被移出配置的 easytier 出站
// 永远不会 Close ⇒ 它的 TUN 设备、overlay 地址与 /24 路由**一直留在内核里** ✗
// （实测:一次重载后出现 4 张 utun，两张是上一代的 10.144.0.6/.7 ✗）。
// ⇒ 桥一旦不可达就由 GC 收尾；Close 里会清掉 finalizer（幂等，不会重复收尾 ✓）。
func installBridgeFinalizer(bridge *TunBridge) {
	if bridge == nil {
		return
	}
	runtime.SetFinalizer(bridge, func(b *TunBridge) {
		// 现场可见：这条只在"调用方再也不会 Close"时出现（例如被移出配置的出站）✓
		log.Debugln("[EasyTier] TUN bridge finalized: abandoned by a config reload, device closed")
		_ = b.Close()
	})
}

// Close 停止搬运并关闭设备；幂等，可重复调用。
func (b *TunBridge) Close() error {
	if b == nil {
		return nil
	}
	runtime.SetFinalizer(b, nil) // 显式收尾 ⇒ 不需要 GC 再跑一遍
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
