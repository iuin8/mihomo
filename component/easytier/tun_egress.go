// FORK(easytier-tun): 把宿主 mihomo 栈发往 overlay 对端的包交回 EasyTier。
//
// 为什么必须这样（2026-10-13 两容器实验台实测 ✓，判据见 docs/easytier/tun-mode-spec.md）：
// 宿主 TUN 栈给 overlay 对端的回包**不能**写进 TUN 设备 ——
//   - Linux：源地址属于本机（例如 10.145.0.200 配在 easytier0 上），从 Meta 口进来的包
//     会被 `__fib_validate_source` 判为 martian 源直接丢弃（`accept_local` 默认 0），
//     除非改 sysctl，否则连内核日志都不留；
//   - macOS：即使不判 martian，也需要 `net.inet.ip.forwarding=1` 才会被转发（默认 0）。
//
// 实测现象：包写进设备 ⇒ 内核静默丢弃 ⇒ 对端收不到 SYN-ACK ⇒
// gVisor 的 `performHandshake` 永久阻塞（`stack_gvisor_tcp.go` 的 `Forward` 卡在
// `r.CreateEndpoint`）⇒ mihomo 连连接都不会建（无 `[TCP]` 日志、`/connections` 为空），
// 表现为"借出口一律 504/超时"。把这类包直接交回 guest（由它经 overlay 送给对端），
// 两个平台都不再依赖任何系统配置 —— 这正是"只装 clash 就能用"的要求。
//
// 判据与入向完全对称（复用同一个 packetDestIsOtherOverlayPeer）：
// 目的地**落在本节点 overlay 网段、且不是本机自己**的包属于 overlay，交回 guest；
// 目的地是本机 overlay 地址的包仍写 TUN 设备 —— 那是本机内核的服务端/客户端要收的包。
package easytier

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/metacubex/mihomo/log"
)

// OverlayEgress 是"经 overlay 送出去"的出口：由 EasyTier 侧登记，宿主 TUN 侧消费。
type OverlayEgress interface {
	// LocalPrefix 返回本节点 overlay 网段（用于判断目的地是否属于 overlay 对端）。
	LocalPrefix() netip.Prefix
	// SendToOverlay 把目的地在 overlay 网段内的包交回 EasyTier 实例（best effort）。
	// 返回 false 表示没能交出去，调用方**必须**回退到原路径（写设备）。
	SendToOverlay(packet []byte) bool
}

// 宿主 TUN 的出向出口登记。与入向一样用互斥锁：登记/清理低频，且存的是接口值。
var (
	overlayEgressMu sync.Mutex
	overlayEgress   OverlayEgress
)

// PublishOverlayEgress 登记把包交回 overlay 的出口，供宿主 TUN 出向分流使用。
func PublishOverlayEgress(sink OverlayEgress) {
	if sink == nil {
		return
	}
	overlayEgressMu.Lock()
	overlayEgress = sink
	overlayEgressMu.Unlock()
}

// ClearOverlayEgress 只清理仍属于自己的那条登记（避免把后登记的实例清掉）。
func ClearOverlayEgress(sink OverlayEgress) {
	if sink == nil {
		return
	}
	overlayEgressMu.Lock()
	if overlayEgress == sink {
		overlayEgress = nil
	}
	overlayEgressMu.Unlock()
}

// CurrentOverlayEgress 返回当前的 overlay 出口；没有可用实例时返回 nil。
func CurrentOverlayEgress() OverlayEgress {
	overlayEgressMu.Lock()
	defer overlayEgressMu.Unlock()
	return overlayEgress
}

// planeEgress 把包交给 EasyTier 实例的包面（*corehost.Instance 天然满足 packetPlane）。
type planeEgress struct {
	ctx    context.Context
	plane  packetPlane
	prefix netip.Prefix
	sent   atomic.Uint64
	failed atomic.Uint64
	logged atomic.Uint64
}

// NewPlaneEgress 造一条"经包面送回 overlay"的出口；prefix 非法时返回 nil（调用方不登记）。
func NewPlaneEgress(ctx context.Context, plane packetPlane, prefix netip.Prefix) OverlayEgress {
	if ctx == nil || plane == nil || !prefix.IsValid() {
		return nil
	}
	return &planeEgress{ctx: ctx, plane: plane, prefix: prefix}
}

func (e *planeEgress) LocalPrefix() netip.Prefix {
	if e == nil {
		return netip.Prefix{}
	}
	return e.prefix
}

func (e *planeEgress) SendToOverlay(packet []byte) bool {
	if e == nil || len(packet) == 0 {
		return false
	}
	if err := e.plane.SendPacket(e.ctx, packet); err != nil {
		e.failed.Add(1)
		if e.logged.Add(1) <= 5 {
			// 交不回去时必须留证：静默失败会让"对端收不到回包"变成无解的悬案。
			log.Debugln("[EasyTier] overlay egress send failed: %v (%s)", err, describeIPv4Packet(packet))
		}
		return false
	}
	e.sent.Add(1)
	return true
}

// String 便于日志与测试断言。
func (e *planeEgress) String() string {
	if e == nil {
		return "easytier:overlay-egress(nil)"
	}
	return fmt.Sprintf("easytier:overlay-egress(%s sent=%d failed=%d)", e.prefix, e.sent.Load(), e.failed.Load())
}
