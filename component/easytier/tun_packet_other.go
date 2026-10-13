//go:build !darwin

// FORK(easytier-tun): Linux 的 TUN 在 IFF_NO_PI 下就是**裸 IP 包**（sing-tun 也是这么建的 ✓），
// 不需要 macOS utun 那 4 字节地址族头 ✓ ⇒ 这里直通 ✓（macOS 那侧见 tun_packet_darwin.go ✓）。
package easytier

func wrapPacketDevice(device packetDevice) packetDevice { return device }
