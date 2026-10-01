// FORK(easytier-tun): 把宿主 TUN 设备与 EasyTier 实例的包面接起来。
//
// 上游从不调用 Instance.SendPacket/ReceivePacket，也没有建过设备；本文件补上这一段：
//   device.Read        → instance.SendPacket    （宿主 → overlay）
//   instance.ReceivePacket → device.Write       （overlay → 宿主）
//
// 设备参数沿用 easytier-go 官方范例 examples/tun：宿主建设备、AutoRoute=false（绝不抢默认路由）、
// MTU 1380；路由安装交给 sing-tun 的 Inet4RouteAddress，不手写 route add。
//
// 规格与验收标准见 docs/easytier_tun_spec.md。
package easytier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"

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
func runTunBridge(parent context.Context, device packetDevice, plane packetPlane) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	go func() {
		<-ctx.Done()
		_ = device.Close()
	}()

	results := make(chan error, 2)
	go func() { results <- copyDeviceToPlane(ctx, device, plane) }()
	go func() { results <- copyPlaneToDevice(ctx, plane, device) }()

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

func copyPlaneToDevice(ctx context.Context, plane packetPlane, device io.Writer) error {
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
		if _, err := device.Write(packet); err != nil {
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
		bridge.err = runTunBridge(ctx, device, plane)
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
