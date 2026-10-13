package easytier

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// v4Packet 造一个最小 IPv4 头，只填源/目的地址。
func v4Packet(src, dst netip.Addr) []byte {
	p := make([]byte, 20)
	p[0] = 0x45
	s4, d4 := src.As4(), dst.As4()
	copy(p[12:16], s4[:])
	copy(p[16:20], d4[:])
	_ = binary.BigEndian
	return p
}

// 两条缝的判据都在这一个文件里钉住（2026-10-13 两容器实验台实测后重写）：
//
//	入向（guest → 宿主）：`packetDestInOverlay` 为 **false** 才投喂 mihomo 的栈；
//	出向（宿主栈 → guest）：`packetDestIsOtherOverlayPeer` 为 **true** 才交回 guest。
//
// 两条共同的含义：**overlay 网段内的包属于 EasyTier 与内核**（本机服务、数据面监听口、
// 其它节点），只有"替对端出网"的包（目的地不在网段内）才归 mihomo 的栈。

// 入向：目的地是本机 overlay 地址 ⇒ 不投喂（那个 socket 在内核侧），交回设备。
func TestIngressKeepsOwnOverlayAddressOnDevice(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("183.2.172.177"), netip.MustParseAddr("10.144.0.13"))
	if !packetDestInOverlay(packet, local) {
		t.Fatal("our own overlay address must count as inside the overlay prefix")
	}
}

// 入向：目的地是另一个 overlay 节点 ⇒ 同样不投喂（EasyTier 自己会转）。
func TestIngressKeepsOtherOverlayPeerOnDevice(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("10.144.0.6"), netip.MustParseAddr("10.144.0.30"))
	if !packetDestInOverlay(packet, local) {
		t.Fatal("another overlay peer must count as inside the overlay prefix")
	}
}

// 入向：公网目的地 ⇒ 必须投喂给 mihomo 的栈（"本机替对端出网"）。
func TestIngressInjectsForeignDest(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("10.144.0.12"), netip.MustParseAddr("1.1.1.1"))
	if packetDestInOverlay(packet, local) {
		t.Fatal("foreign destination must not be treated as inside the overlay prefix")
	}
}

// 出向：目的地是另一个 overlay 节点 ⇒ 交回 guest（写设备会被内核丢掉）。
func TestEgressDivertsOtherOverlayPeer(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("223.5.5.5"), netip.MustParseAddr("10.144.0.6"))
	if !packetDestIsOtherOverlayPeer(packet, local) {
		t.Fatal("another overlay peer must be diverted back to the guest")
	}
}

// 出向：目的地是本机自己的 overlay 地址 ⇒ 走内核（本机服务的回包由内核交付）。
func TestEgressKeepsOwnOverlayAddressOnDevice(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("223.5.5.5"), netip.MustParseAddr("10.144.0.13"))
	if packetDestIsOtherOverlayPeer(packet, local) {
		t.Fatal("our own overlay address must stay on the host device")
	}
}
