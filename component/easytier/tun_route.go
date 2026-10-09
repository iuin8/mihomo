// FORK(easytier-tun): 让"回给 overlay 对端的包"能进隧道（macOS 必需的回程路由）。
//
// 为什么必须自己装：Linux 内核在给接口配上地址时会自动派生 `proto kernel` 的网段路由，
// 所以那里的内核转发与回程都不需要额外动作；**macOS 不派生** ——
// 而 sing-tun 的 Inet4RouteAddress 在 darwin **没有任何消费者**（它只被 Linux 的 auto-route 规则
// 与 redirect 规则读取），所以指望它装路由是错的。
//
// 不装这条路由的后果（实测 ✓）：mihomo 的 TUN 栈为投喂进来的包产生应答，
// 应答的目的地址是 overlay 对端（如 10.144.0.1），内核找不到 overlay 网段的路由 →
// 走默认路由出物理网卡 → 对端永远收不到，表现为 delay 测试一直 Timeout。
//
// 接口名不能用创建时请求的名字：macOS 的 utun 由内核分配编号（本次实测同一个进程
// 先后拿到过 utun1025 与 utun4），所以**按地址反查接口**。
package easytier

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"time"

	"github.com/metacubex/mihomo/log"
)

const overlayRouteAddressPoll = 100 * time.Millisecond

// overlayRouteAddressWait 是等待设备地址就绪的上限（设备刚创建时地址可能还没配上）。
// 变量而非常量：测试要把它缩短到毫秒级。
var overlayRouteAddressWait = 3 * time.Second

// ensureOverlayRoute 在 darwin 上把 overlay 网段 pin 到设备所在接口，返回清理函数。
// 其他平台（Linux 自动派生）不做任何事，也不应干预内核的路由管理。
func ensureOverlayRoute(prefix netip.Prefix, address netip.Addr) (func(), error) {
	noop := func() {}
	if runtime.GOOS != "darwin" || !prefix.IsValid() || !address.IsValid() {
		return noop, nil
	}
	name, err := findInterfaceByAddress(address)
	if err != nil {
		return noop, err
	}
	network := prefix.Masked().String()
	if output, err := exec.Command("route", "-n", "add", "-net", network, "-interface", name).CombinedOutput(); err != nil {
		return noop, fmt.Errorf("easytier: add overlay route %s via %s: %w (%s)", network, name, err, output)
	}
	log.Infoln("[EasyTier] overlay route %s installed on %s (return path for overlay peers)", network, name)
	return func() {
		if output, err := exec.Command("route", "-n", "delete", "-net", network, "-interface", name).CombinedOutput(); err != nil {
			log.Debugln("[EasyTier] remove overlay route %s via %s failed: %v (%s)", network, name, err, output)
		}
	}, nil
}

// findInterfaceByAddress 轮询直到某块接口持有该地址。
func findInterfaceByAddress(address netip.Addr) (string, error) {
	deadline := time.Now().Add(overlayRouteAddressWait)
	for {
		if name, ok := interfaceByAddress(address); ok {
			return name, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("easytier: no interface holds %s yet", address)
		}
		time.Sleep(overlayRouteAddressPoll)
	}
}

func interfaceByAddress(address netip.Addr) (string, bool) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", false
	}
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			prefix, err := netip.ParsePrefix(addr.String())
			if err != nil {
				continue
			}
			if prefix.Addr().Unmap() == address.Unmap() {
				return iface.Name, true
			}
		}
	}
	return "", false
}
