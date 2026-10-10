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

// 本机经远端出口上网时，回程包的目的地**就是本机 overlay 地址** —— 必须投喂（这是本轮修复的核心）。
func TestIngressRuleAllowsOwnOverlayAddress(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("183.2.172.177"), netip.MustParseAddr("10.144.0.13"))
	if packetDestIsOtherOverlayPeer(packet, local) {
		t.Fatal("destined to our own overlay address must be injected, not skipped")
	}
}

// 目的地是**另一个** overlay 节点：跳过（否则 mihomo 会再拨一次，绕回 overlay）。
func TestIngressRuleSkipsOtherOverlayPeer(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("10.144.0.6"), netip.MustParseAddr("10.144.0.30"))
	if !packetDestIsOtherOverlayPeer(packet, local) {
		t.Fatal("destined to another overlay peer must be skipped")
	}
}

// 与前缀无关的普通公网包：不跳过。
func TestIngressRuleIgnoresForeignDest(t *testing.T) {
	local := netip.MustParsePrefix("10.144.0.13/24")
	packet := v4Packet(netip.MustParseAddr("10.144.0.12"), netip.MustParseAddr("1.1.1.1"))
	if packetDestIsOtherOverlayPeer(packet, local) {
		t.Fatal("foreign destination must not be treated as an overlay peer")
	}
}
