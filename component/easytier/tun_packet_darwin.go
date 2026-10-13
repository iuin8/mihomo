//go:build darwin

// FORK(easytier-tun): macOS 的 utun **每包都带 4 字节地址族头**，而 sing-tun 暴露给外部的
// `Tun` 接口只有裸 `Read`/`Write`（`tun_darwin.go`：`Write` 就是 `tunFile.Write(p)` ✗）。
//
// ⇒ 本 fork 早期直接用 `device.Read`/`device.Write` 传裸 IP 包，在 macOS 上**两侧全错** ✓✗：
//   - **写**：内核把 IP 头前 4 字节当成地址族（`0x45 0x00 0x00 0x3c` ✗）⇒ **整包丢弃** ✗，
//     而 `write(2)` 仍然返回成功 ⇒ 日志里是 `wrote=60 err=<nil>` 却毫无效果（2026-10-13 实测 ✓）；
//   - **读**：内核交给用户的包带 4 字节头（首字节 `0x00` ✗）⇒ 被 `isIPv4Packet` 判为"不是 IPv4"
//     而后跳过 ✗ ⇒ "设备 → guest 方向一条包都没有"（实测 ✓）。
//
// 这就是"同一台机器、同一份 ifconfig、同一份 SIOCAIFADDR，mihomo 自己的 TUN 一切正常 ✓、
// fork 自建的 TUN 完全不通 ✗"的真正原因 ✓：差别不在接口，而在**读写 ABI** ✓。
//
// 修法：在设备外面包一层，写时补头、读时剥头 ✓（与 sing-tun 内部 `WritePacket`/`ReadPacket`
// 的做法一致 ✓）。Linux 的 TUN 在 `IFF_NO_PI` 下是裸 IP 包 ✓，所以这个包装只在 darwin 生效 ✓。
package easytier

import (
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

// utunPacketHeaderSize 是 macOS utun 的每包地址族头长度。
const utunPacketHeaderSize = 4

// packetHeaderDevice 把裸 IP 包设备适配成 macOS utun 需要的"带地址族头"设备。
//
// 两个 scratch 缓冲都是**按"单读者单写者"使用**的 ✓：读只发生在 copyDeviceToPlane 那一个
// goroutine ✓，写只发生在 copyPlaneToDevice 那一个 goroutine ✓（fork 的包面就是这样接的 ✓）
// ⇒ 各自复用一个缓冲，避免每包一次分配 ✓。
type packetHeaderDevice struct {
	inner        packetDevice
	readScratch  []byte
	writeScratch []byte
}

// wrapPacketDevice 在 darwin 上补一层地址族头处理；其它平台由 tun_packet_other.go 直通。
func wrapPacketDevice(device packetDevice) packetDevice {
	if device == nil {
		return nil
	}
	return &packetHeaderDevice{inner: device}
}

// Read 剥掉地址族头后返回裸 IP 包。
func (d *packetHeaderDevice) Read(p []byte) (int, error) {
	if need := len(p) + utunPacketHeaderSize; len(d.readScratch) < need {
		d.readScratch = make([]byte, need)
	}
	buffer := d.readScratch
	length, err := d.inner.Read(buffer)
	if err != nil {
		return 0, err
	}
	if length < utunPacketHeaderSize {
		return 0, fmt.Errorf("easytier: short utun read: %d bytes", length)
	}
	if buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 {
		// 只认"4 字节全零 + 地址族"这种头 ✓；其它形状原样返回并让上层判 IPv4 ✓，
		// 免得将来 sing-tun 换了读法（GSO/recvmsg_x）时把包全丢掉 ✗。
		return copy(p, buffer[:length]), nil
	}
	family := buffer[3]
	if family != unix.AF_INET && family != unix.AF_INET6 {
		return copy(p, buffer[:length]), nil
	}
	return copy(p, buffer[utunPacketHeaderSize:length]), nil
}

// Write 补上地址族头再交给内核 —— 没有它内核会把整个包丢掉。
func (d *packetHeaderDevice) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	family := byte(unix.AF_INET6)
	switch p[0] >> 4 {
	case 4:
		family = unix.AF_INET
	case 6:
		family = unix.AF_INET6
	default:
		return 0, fmt.Errorf("easytier: cannot write non-IP packet (first byte %#x) to utun", p[0])
	}
	need := utunPacketHeaderSize + len(p)
	if len(d.writeScratch) < need {
		d.writeScratch = make([]byte, need)
	}
	packet := d.writeScratch[:need]
	packet[0], packet[1], packet[2] = 0, 0, 0
	packet[3] = family
	copy(packet[utunPacketHeaderSize:], p)
	if _, err := d.inner.Write(packet); err != nil {
		return 0, err
	}
	// 报告的是裸包长度 ✓：调用方关心的是"我的包写出去了吗"，不是"含头多少字节" ✓。
	return len(p), nil
}

func (d *packetHeaderDevice) Close() error { return d.inner.Close() }

var _ io.ReadWriteCloser = (*packetHeaderDevice)(nil)
