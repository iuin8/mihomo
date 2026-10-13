//go:build with_gvisor

// FORK(easytier-tun): gvisor 栈的出向分流点。
//
// 栈把包交给 NIC 时，最后一个可控的缝是 NIC 的 queueing discipline：
// `nic.writeRawPacket` → `n.qDisc.WritePacket(pkt)`（gVisor `stack/nic.go:433`），
// 而 `NICOptions.QDisc` 由本 fork 在 `NewEndpoint()` 这里替换（见 tun_ingress_gvisor.go）。
// 在这条缝上分流，平台无关：
//   - darwin：sing-tun 的 `tun_darwin_gvisor.go` 会塞一个 `fifo` qdisc，我们在它外面再包一层；
//   - linux ：`tun_linux_gvisor.go` 不给 qdisc，我们补一个等价实现（行为与 gVisor 自己的
//     `delegatingQueueingDiscipline` 一致：把单包塞进列表后直写 link endpoint）。
//
// 分流出去的包**不写设备**：所有权仍属调用方（gVisor 的发送方自己 DecRef），
// 这里只把裸 IP 包复制一份交给 guest，绝不 IncRef/DecRef（对齐 fdbased 的写法）。
package easytier

import (
	"sync/atomic"

	"github.com/metacubex/gvisor/pkg/tcpip"
	"github.com/metacubex/gvisor/pkg/tcpip/stack"

	"github.com/metacubex/mihomo/log"
)

var (
	_ stack.QueueingDiscipline = (*overlayQDisc)(nil)
	_ stack.QueueingDiscipline = (*linkEndpointQDisc)(nil)
)

// overlayQDisc 在最后一缝上把"目的地属于 overlay 对端"的包交回 EasyTier，其余原样下沉。
type overlayQDisc struct {
	lower  stack.QueueingDiscipline
	logged atomic.Uint64
}

// linkEndpointQDisc 是没有平台 qdisc 时的等价实现（与 gVisor 的 delegatingQueueingDiscipline 同义）。
type linkEndpointQDisc struct {
	writer stack.LinkWriter
}

func (q *linkEndpointQDisc) WritePacket(pkt *stack.PacketBuffer) tcpip.Error {
	var pkts stack.PacketBufferList
	pkts.PushBack(pkt)
	_, err := q.writer.WritePackets(pkts)
	return err
}

func (q *linkEndpointQDisc) Close() {}

// wrapQDiscForOverlay 给平台的 qdisc（可为 nil）套上出向分流层。
func wrapQDiscForOverlay(qDisc stack.QueueingDiscipline, writer stack.LinkWriter) stack.QueueingDiscipline {
	if qDisc == nil {
		qDisc = &linkEndpointQDisc{writer: writer}
	}
	return &overlayQDisc{lower: qDisc}
}

func (q *overlayQDisc) WritePacket(pkt *stack.PacketBuffer) tcpip.Error {
	if sink := CurrentOverlayEgress(); sink != nil && pkt != nil {
		if raw := rawIPPacket(pkt); len(raw) >= minIPv4PacketLen &&
			packetDestIsOtherOverlayPeer(raw, sink.LocalPrefix()) {
			if sink.SendToOverlay(raw) {
				if q.logged.Add(1) <= 20 {
					log.Debugln("[EasyTier] overlay egress -> guest: %s", describeIPv4Packet(raw))
				}
				return nil
			}
			// 交不回去就回退到原路径：宁可让内核去处理，也不能悄悄丢包。
			log.Debugln("[EasyTier] overlay egress fallback to host device: %s", describeIPv4Packet(raw))
		}
	}
	return q.lower.WritePacket(pkt)
}

// rawIPPacket 取出**完整**的 IP 包（IP 头 + 载荷），因为 guest 要的是内核视角那一整包。
//
// 不能用 `pkt.NetworkHeader().Slice()`：它只返回被 Push 标记的那一段 —— 出向包上
// 那正好是 20 字节 IP 头，载荷不在里面（实测：单独用它会把截断的包交给 guest）。
// `AsSlices()` 才是"要写给设备的那一整包"（fdbased 写 fd 用的也是它），
// 起点是 `headerOffset()`（TUN 上没有链路头，正好落在 IP 头）。
// 返回的切片一定是新的：PacketBuffer 在本函数返回后即可能被回收。
func rawIPPacket(pkt *stack.PacketBuffer) []byte {
	views := pkt.AsSlices()
	total := 0
	for _, view := range views {
		total += len(view)
	}
	raw := make([]byte, 0, total)
	for _, view := range views {
		raw = append(raw, view...)
	}
	return raw
}

func (q *overlayQDisc) Close() {
	q.lower.Close()
}
