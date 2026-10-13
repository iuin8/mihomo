package easytier

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"
)

// 出向出口（把宿主栈发往 overlay 对端的包交回 guest）的三条契约：
//   - 目的地是**其它** overlay 节点 ⇒ 交给包面（不写设备，内核会丢）；
//   - 交不回去 ⇒ 返回 false，调用方必须回退原路径（绝不静默丢包）；
//   - 空包 / 非法参数一律不交。
//
// 生产实证：这条缝缺失时，借出口一侧永远收不到 SYN-ACK —— 包写进 TUN 设备后被内核丢弃
// （Linux 判 martian 源，见 tun_egress.go 顶注），gVisor 的 performHandshake 永久阻塞。
func TestPlaneEgressSendsToPlane(t *testing.T) {
	plane := newFakePlane()
	egress := NewPlaneEgress(context.Background(), plane, netip.MustParsePrefix("10.144.0.6/24"))
	if egress == nil {
		t.Fatal("egress must be created for a valid prefix and plane")
	}
	if got := egress.LocalPrefix(); got != netip.MustParsePrefix("10.144.0.6/24") {
		t.Fatalf("LocalPrefix = %s", got)
	}

	packet := ipv4Packet([4]byte{10, 144, 0, 1})
	if !egress.SendToOverlay(packet) {
		t.Fatal("SendToOverlay must report success when the plane accepts the packet")
	}
	sent := plane.sentPackets()
	if len(sent) != 1 || !bytes.Equal(sent[0], packet) {
		t.Fatalf("plane must receive an exact copy of the packet, got %d packets", len(sent))
	}

	if egress.SendToOverlay(nil) {
		t.Fatal("empty packet must not be handed to the plane")
	}

	plane.sendErr = errors.New("plane down")
	if egress.SendToOverlay(packet) {
		t.Fatal("failed send must report false so the caller can fall back to the host device")
	}
}

func TestNewPlaneEgressRejectsUnusableInput(t *testing.T) {
	plane := newFakePlane()
	prefix := netip.MustParsePrefix("10.144.0.6/24")
	if NewPlaneEgress(context.Background(), nil, prefix) != nil {
		t.Fatal("nil plane must yield no egress")
	}
	if NewPlaneEgress(nil, plane, prefix) != nil {
		t.Fatal("nil context must yield no egress")
	}
	if NewPlaneEgress(context.Background(), plane, netip.Prefix{}) != nil {
		t.Fatal("invalid prefix must yield no egress")
	}
}

// 登记的所有权语义：后登记的接管，先退出的实例不得清掉后来者。
func TestOverlayEgressRegistryOwnership(t *testing.T) {
	first := NewPlaneEgress(context.Background(), newFakePlane(), netip.MustParsePrefix("10.144.0.6/24"))
	second := NewPlaneEgress(context.Background(), newFakePlane(), netip.MustParsePrefix("10.144.0.6/24"))
	if first == nil || second == nil {
		t.Fatal("test setup: egresses must exist")
	}
	defer func() {
		ClearOverlayEgress(first)
		ClearOverlayEgress(second)
	}()

	PublishOverlayEgress(first)
	if CurrentOverlayEgress() != first {
		t.Fatal("published egress must be current")
	}
	PublishOverlayEgress(second)
	if CurrentOverlayEgress() != second {
		t.Fatal("later registration must take over")
	}
	ClearOverlayEgress(first) // 旧实例退出
	if CurrentOverlayEgress() != second {
		t.Fatal("a stale clear must not remove the newer registration")
	}
	ClearOverlayEgress(second)
	if CurrentOverlayEgress() != nil {
		t.Fatal("clearing the owner must leave no egress")
	}
}
