//go:build with_gvisor

// FORK(easytier-tun): 出向分流缝（NIC 的 queueing discipline）的契约测试。
//
// 分流点必须满足：
//   - 目的地是**其它** overlay 节点 ⇒ 交回 guest，**不写设备**；
//   - 目的地是本机 overlay 地址 / 公网 ⇒ 原样下沉到设备（那是本机内核的包）；
//   - 交不回去 ⇒ 回退下沉，绝不静默丢包。
package easytier

import (
	"net/netip"
	"testing"

	"github.com/metacubex/gvisor/pkg/buffer"
	"github.com/metacubex/gvisor/pkg/tcpip"
	"github.com/metacubex/gvisor/pkg/tcpip/header"
	"github.com/metacubex/gvisor/pkg/tcpip/stack"
)

type recordingQDisc struct {
	packets [][]byte
}

func (q *recordingQDisc) WritePacket(pkt *stack.PacketBuffer) tcpip.Error {
	q.packets = append(q.packets, append([]byte(nil), pkt.NetworkHeader().Slice()...))
	return nil
}

func (q *recordingQDisc) Close() {}

type recordingLinkWriter struct {
	batches [][][]byte
}

func (w *recordingLinkWriter) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	var batch [][]byte
	for _, pkt := range pkts.AsSlice() {
		batch = append(batch, append([]byte(nil), pkt.NetworkHeader().Slice()...))
	}
	w.batches = append(w.batches, batch)
	return len(batch), nil
}

type recordingEgress struct {
	prefix  netip.Prefix
	packets [][]byte
	accept  bool
}

func (e *recordingEgress) LocalPrefix() netip.Prefix { return e.prefix }

func (e *recordingEgress) SendToOverlay(packet []byte) bool {
	e.packets = append(e.packets, append([]byte(nil), packet...))
	return e.accept
}

// packetBuffer 按**出向包的真实形状**造包：预留头空间 + Push 网络头 + 载荷。
// 这样 `NetworkHeader().Slice()` 只有 20 字节 IP 头，而 `AsSlices()` 是整包 ——
// 正是为了钉住"必须交出整包，不能只交头"这条契约（曾经踩到）。
func packetBuffer(dst [4]byte) *stack.PacketBuffer {
	payload := make([]byte, header.TCPMinimumSize)
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		ReserveHeaderBytes: header.IPv4MinimumSize,
		Payload:            buffer.MakeWithData(payload),
	})
	copy(pkt.NetworkHeader().Push(header.IPv4MinimumSize), ipv4Packet(dst))
	return pkt
}

func TestOverlayQDiscDivertsPeerDestinedPackets(t *testing.T) {
	egress := &recordingEgress{prefix: netip.MustParsePrefix("10.145.0.200/24"), accept: true}
	PublishOverlayEgress(egress)
	defer ClearOverlayEgress(egress)

	lower := &recordingQDisc{}
	qDisc := wrapQDiscForOverlay(lower, &recordingLinkWriter{})

	// 目的地是别的 overlay 节点：必须交回 guest，且**不能**再写设备。
	peer := packetBuffer([4]byte{10, 145, 0, 201})
	defer peer.DecRef()
	if err := qDisc.WritePacket(peer); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if len(egress.packets) != 1 {
		t.Fatalf("peer-destined packet must be handed back to the guest, got %d", len(egress.packets))
	}
	if got := len(egress.packets[0]); got != header.IPv4MinimumSize+header.TCPMinimumSize {
		t.Fatalf("guest must receive the whole packet (IP header + payload), got %d bytes", got)
	}
	if len(lower.packets) != 0 {
		t.Fatalf("diverted packet must not reach the host device, got %d", len(lower.packets))
	}

	// 目的地就是本机 overlay 地址：那是本机内核的包，必须下沉。
	own := packetBuffer([4]byte{10, 145, 0, 200})
	defer own.DecRef()
	if err := qDisc.WritePacket(own); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	// 公网目的地（exit 场景的回包目标是 overlay 对端，但本机自己发起的连接目标是公网）：下沉。
	public := packetBuffer([4]byte{223, 5, 5, 5})
	defer public.DecRef()
	if err := qDisc.WritePacket(public); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if len(lower.packets) != 2 {
		t.Fatalf("own/public destinations must stay on the host device, got %d", len(lower.packets))
	}
	if len(egress.packets) != 1 {
		t.Fatalf("only peer-destined packets may be diverted, got %d", len(egress.packets))
	}
}

// 交不回去必须回退：宁可让内核去处理，也不能悄悄丢包。
func TestOverlayQDiscFallsBackWhenEgressRefuses(t *testing.T) {
	egress := &recordingEgress{prefix: netip.MustParsePrefix("10.145.0.200/24"), accept: false}
	PublishOverlayEgress(egress)
	defer ClearOverlayEgress(egress)

	lower := &recordingQDisc{}
	qDisc := wrapQDiscForOverlay(lower, &recordingLinkWriter{})
	pkt := packetBuffer([4]byte{10, 145, 0, 201})
	defer pkt.DecRef()
	if err := qDisc.WritePacket(pkt); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if len(egress.packets) != 1 || len(lower.packets) != 1 {
		t.Fatalf("refused diversion must fall back to the device: egress=%d lower=%d",
			len(egress.packets), len(lower.packets))
	}
}

// 没有出向出口（例如未配置 ipv4）时行为与以前完全一致：全部下沉。
func TestOverlayQDiscPassesThroughWithoutEgress(t *testing.T) {
	ClearOverlayEgress(&recordingEgress{})
	lower := &recordingQDisc{}
	qDisc := wrapQDiscForOverlay(lower, &recordingLinkWriter{})
	pkt := packetBuffer([4]byte{10, 145, 0, 201})
	defer pkt.DecRef()
	if err := qDisc.WritePacket(pkt); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if len(lower.packets) != 1 {
		t.Fatalf("without an egress sink every packet must go to the device, got %d", len(lower.packets))
	}
}

// 平台没给 qdisc 时（linux）补的等价实现必须把包直写 link endpoint。
func TestLinkEndpointQDiscWritesThrough(t *testing.T) {
	writer := &recordingLinkWriter{}
	var qDisc stack.QueueingDiscipline = wrapQDiscForOverlay(nil, writer)
	pkt := packetBuffer([4]byte{1, 1, 1, 1})
	defer pkt.DecRef()
	if err := qDisc.WritePacket(pkt); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if len(writer.batches) != 1 || len(writer.batches[0]) != 1 {
		t.Fatalf("expected exactly one packet to reach the link endpoint, got %v", writer.batches)
	}

}
