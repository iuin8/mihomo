//go:build darwin

// FORK(easytier-tun): 清理"上一个内核进程留下的"同名 TUN 设备 fd。
//
// 为什么必须有它（2026-10-13 实测 ✓，判据见 docs/easytier/tun-mode-spec.md §10）：
// mihomo 的 `POST /restart` 走 `syscall.Exec` —— 进程镜像被替换，但**已打开的 fd 不会关闭**
// （`hub/route/restart.go` 直接 exec，没有先关 outbound）。于是每次那样重启，旧的 utun 都
// **继续存活并继续占着同一个 overlay 地址**。macOS 上该地址被多张接口同时持有后，本地投递
// 彻底错乱：实测 6 张 utun 持有 10.144.0.6 时，`nc -v -z 10.144.0.6 <任意端口>`（连系统自带
// 的 ControlCenter:7000 也一样）一律 "Operation timed out"，而本进程唯一持有的 198.18.0.1
// 上同一端口全部 succeeded。现象是"对端 SYN 到了、包也写进设备了、就是永远没有 SYN-ACK"。
//
// 能自己修，是因为 exec 之后那些 fd 就挂在**当前进程**上 —— 在这里（建设备之前）把它们关掉，
// 内核随即销毁对应的 utun，地址恢复成唯一持有。
//
// 只关**utun**且**持有本节点 overlay 地址**的 fd（用 getsockopt(UTUN_OPT_IFNAME) 反查接口名，
// 再比对接口地址），绝不碰 mihomo 自己的 TUN 或其它 socket。
package easytier

import (
	"net"
	"net/netip"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/metacubex/mihomo/log"
)

// utunControlNameSize 是 UTUN_OPT_IFNAME 返回的接口名缓冲（IFNAMSIZ=16，留余量）。
const utunControlNameSize = 64

// x/sys/unix 没导出这两个常量（它们在 Apple 的 kern_control.h / utun_control.h 里）。
const (
	sysprotoControl = 2 // SYSPROTO_CONTROL
	utunOptIfname   = 2 // UTUN_OPT_IFNAME
)

// closeStaleOverlayTunFDs 关掉本进程继承来的、持有同一 overlay 地址的 utun fd，返回关闭数量。
// 任何一步失败都只是跳过该 fd：清理失败绝不能挡住建隧道。
func closeStaleOverlayTunFDs(address netip.Addr) int {
	if !address.IsValid() {
		return 0
	}
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0
	}
	closed := 0
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd <= 2 { // 0/1/2 是标准流，永远不动
			continue
		}
		name := utunInterfaceName(fd)
		if name == "" || !interfaceHoldsAddress(name, address) {
			continue
		}
		if err := unix.Close(fd); err != nil {
			continue
		}
		closed++
		log.Warnln("[EasyTier] closed stale TUN fd %d (%s) still holding %s after an exec-style restart", fd, name, address)
	}
	if closed > 0 {
		log.Warnln("[EasyTier] closed %d stale TUN device(s) holding %s: an exec-style core restart leaks TUN fds, "+
			"which makes the overlay address unusable on macOS until they are closed", closed, address)
	}
	return closed
}

// utunInterfaceName 用 getsockopt(SYSPROTO_CONTROL, UTUN_OPT_IFNAME) 反查 utun 接口名。
// 非 utun 的 fd 会返回错误，返回空串。
func utunInterfaceName(fd int) string {
	buffer := make([]byte, utunControlNameSize)
	size := uint32(len(buffer))
	_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), uintptr(sysprotoControl), uintptr(utunOptIfname),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0)
	if errno != 0 || size == 0 {
		return ""
	}
	for i, b := range buffer {
		if b == 0 {
			buffer = buffer[:i]
			break
		}
	}
	return string(buffer)
}

// interfaceHoldsAddress 判断指定接口是否持有该地址。
func interfaceHoldsAddress(name string, address netip.Addr) bool {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if prefix, err := netip.ParsePrefix(addr.String()); err == nil && prefix.Addr().Unmap() == address.Unmap() {
			return true
		}
	}
	return false
}
