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
	"strings"
	"time"

	"github.com/metacubex/mihomo/log"
)

const overlayRouteAddressPoll = 100 * time.Millisecond

// overlayRouteAddressWait 是等待设备地址就绪的上限（设备刚创建时地址可能还没配上）。
// 变量而非常量：测试要把它缩短到毫秒级。
var overlayRouteAddressWait = 3 * time.Second

// routeRunner 是执行 route(8) 的唯一入口。做成变量是为了让测试能模拟
// "该网段已有路由、但指向别的接口"这一场景（真实环境里需要 root 才能复现）。
var routeRunner = func(args ...string) ([]byte, error) {
	return exec.Command(args[0], args[1:]...).CombinedOutput()
}

// routeInterfaceFor 查询某网段当前由哪块接口承载；第二个返回值为 false 表示当前没有该网段的路由。
func routeInterfaceFor(network string) (string, bool) {
	output, err := routeRunner("route", "-n", "get", "-net", network)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(output), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "interface:"); ok {
			if name = strings.TrimSpace(name); name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// takeOverDarwinRoute 保证 network 由 name 这块接口承载。
//
// FORK(easytier-tun, 2026-10-16):**这里以前是 bug 的所在地** ✗✓。darwin 的 `route add` 在该网段
// 已有路由时**直接失败**，而调用方只记一句 Warn 就继续 ⇒ 旧设备消失后路由仍然指着它，
// overlay 回程流量进黑洞（实测:`10.144/24 → utun1025` 指着已停用节点的设备，家侧借网一直 Timeout ✗）。
// ⇒ 现在先查它指向谁:不是本设备就删掉再装；已经是本设备就什么都不做（幂等 ✓）。
//
// 作用域（回答"会不会误删有用的路由" ✗✓）:`network` 只可能是**本节点自己的 overlay 网段**
// （ensureOverlayRoute 传 prefix.Masked()）或**调用方在 tun-routes 里显式 pin 的前缀** ——
// 这两类路由按定义就该由本设备承载，绝不会碰到默认路由或其它网段 ✓。
// 唯一的竞争情形是"同一台机器上还有另一个 easytier 节点" ✗（两者都声明同一 overlay 网段）⇒
// 后启动者接管，这正是 SOP 里"一机一节点"的原因 ✓。
//
// 失败安全:先删后装之间存在空窗 ⇒ **add 失败时必须把原来那条恢复回去** ✗✓，
// 绝不留下"删了却没装回来"的状态（那条恢复不回也只是 Warn，不会覆盖原始错误 ✓）。
func takeOverDarwinRoute(network, name string) error {
	previous, held := routeInterfaceFor(network)
	if held {
		if previous == name {
			return nil // 已经对了:幂等，别做多余动作
		}
		log.Warnln("[EasyTier] route %s is held by %s, taking it over for %s", network, previous, name)
		if output, err := routeRunner("route", "-n", "delete", "-net", network); err != nil {
			log.Warnln("[EasyTier] delete stale route %s failed: %v (%s)", network, err, output)
		}
	}
	if output, err := routeRunner("route", "-n", "add", "-net", network, "-interface", name); err != nil {
		if held && previous != name {
			if restoreOutput, restoreErr := routeRunner("route", "-n", "add", "-net", network, "-interface", previous); restoreErr != nil {
				log.Warnln("[EasyTier] restore route %s via %s failed: %v (%s)", network, previous, restoreErr, restoreOutput)
			} else {
				log.Warnln("[EasyTier] restored route %s via %s after a failed takeover", network, previous)
			}
		}
		return fmt.Errorf("route %s via %s: %w (%s)", network, name, err, output)
	}
	return nil
}

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
	if err := takeOverDarwinRoute(network, name); err != nil {
		return noop, fmt.Errorf("easytier: add overlay route %s via %s: %w", network, name, err)
	}
	log.Infoln("[EasyTier] overlay route %s installed on %s (return path for overlay peers)", network, name)
	return func() {
		if output, err := routeRunner("route", "-n", "delete", "-net", network, "-interface", name); err != nil {
			log.Debugln("[EasyTier] remove overlay route %s via %s failed: %v (%s)", network, name, err, output)
		}
	}, nil
}

// ensurePinnedRoute 把 tun-routes 的前缀真的 pin 到设备接口上，返回清理函数。
//
// 为什么不能只靠 sing-tun 的 Inet4RouteAddress：那个字段只在 Linux 的 auto-route / redirect
// 规则里被消费，而本 fork 用 AutoRoute=false（绝不抢默认路由），所以两条路都不走 ⇒ 字段等于没设。
// 而 tun-routes 的语义是"这些前缀的包必须从这块设备出去"，两侧内核都不会自己派生（它们不在接口网段里）。
func ensurePinnedRoute(prefix netip.Prefix, address netip.Addr) (func(), error) {
	noop := func() {}
	if !prefix.IsValid() || !address.IsValid() {
		return noop, nil
	}
	name, err := findInterfaceByAddress(address)
	if err != nil {
		return noop, err
	}
	network := prefix.Masked().String()
	var del []string
	switch runtime.GOOS {
	case "linux":
		// replace:幂等，设备重建/重启后不会因"已存在"而失败。
		if output, err := routeRunner("ip", "route", "replace", network, "dev", name); err != nil {
			return noop, fmt.Errorf("easytier: pin route %s via %s: %w (%s)", network, name, err, output)
		}
		del = []string{"ip", "route", "del", network, "dev", name}
	case "darwin":
		// 同 ensureOverlayRoute:已有路由如果指着别的接口，必须夺回来（否则 pin 形同虚设 ✗）。
		if err := takeOverDarwinRoute(network, name); err != nil {
			return noop, fmt.Errorf("easytier: pin route %s via %s: %w", network, name, err)
		}
		del = []string{"route", "-n", "delete", "-net", network, "-interface", name}
	default:
		return noop, nil
	}
	log.Infoln("[EasyTier] pinned route %s installed on %s", network, name)
	return func() {
		if output, err := routeRunner(del...); err != nil {
			log.Debugln("[EasyTier] remove pinned route %s via %s failed: %v (%s)", network, name, err, output)
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
