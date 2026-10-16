package easytier

import (
	"errors"
	"net/netip"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 接口反查：本机必然持有 127.0.0.1，而未分配的地址必然查不到。
func TestInterfaceByAddress(t *testing.T) {
	if _, ok := interfaceByAddress(netip.MustParseAddr("127.0.0.1")); !ok {
		t.Fatal("loopback address must resolve to an interface")
	}
	if name, ok := interfaceByAddress(netip.MustParseAddr("203.0.113.7")); ok {
		t.Fatalf("unassigned address must not resolve, got %q", name)
	}
}

// 契约：非 darwin 不动内核路由表（Linux 自己派生）；darwin 上地址还没配上时必须报错而不是静默装错。
func TestEnsureOverlayRoute(t *testing.T) {
	original := overlayRouteAddressWait
	overlayRouteAddressWait = 50 * time.Millisecond
	t.Cleanup(func() { overlayRouteAddressWait = original })

	cleanup, err := ensureOverlayRoute(netip.MustParsePrefix("10.144.0.0/24"), netip.MustParseAddr("203.0.113.7"))
	if runtime.GOOS == "darwin" {
		if err == nil {
			cleanup()
			t.Fatal("darwin must report an error when no interface holds the address")
		}
	} else if err != nil {
		t.Fatalf("non-darwin must be a no-op, got %v", err)
	}

	if runtime.GOOS != "darwin" {
		cleanup, err := ensureOverlayRoute(netip.Prefix{}, netip.Addr{})
		if err != nil {
			t.Fatalf("invalid prefix must be a no-op: %v", err)
		}
		cleanup()
	}
}

// 契约：tun-routes 的 pin 必须**真的装**。地址还没配上时必须报错——静默成功就是"配了等于没配"，
// 那正是 Inet4RouteAddress 那个坑（AutoRoute=false 时它在任何平台都没有消费者）。
func TestEnsurePinnedRoute(t *testing.T) {
	original := overlayRouteAddressWait
	overlayRouteAddressWait = 50 * time.Millisecond
	t.Cleanup(func() { overlayRouteAddressWait = original })

	// 非 linux/darwin 平台按设计是 no-op。
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("pinned route install is a no-op on %s", runtime.GOOS)
	}

	// 未分配的地址 ⇒ 找不到接口 ⇒ 必须报错，而不是返回一个什么都不做的 cleanup。
	if _, err := ensurePinnedRoute(netip.MustParsePrefix("1.1.1.1/32"), netip.MustParseAddr("203.0.113.7")); err == nil {
		t.Fatal("must report an error when no interface holds the address")
	}

	// 无效前缀/地址 ⇒ 明确 no-op（不改内核路由表）。
	if cleanup, err := ensurePinnedRoute(netip.Prefix{}, netip.MustParseAddr("127.0.0.1")); err != nil || cleanup == nil {
		t.Fatalf("invalid prefix must be a no-op, got cleanup=%v err=%v", cleanup != nil, err)
	}
}

// ── 2026-10-16:路由**夺回**契约 ✗✓ ─────────────────────────────────────────────
//
// 线上实测:两个节点都往同一个 overlay 网段装路由 ⇒ 先到者占住；后来者的 `route add` 失败、
// 只记一句 Warn 就继续 ⇒ 先到者一停，路由仍指着它的设备，overlay 回程流量进黑洞
// （症状:家侧借网一直 Timeout ✗）。下面这些用例锁住"必须夺回来"与"已经对就一个多余动作都不做"。

// recordRunner 记录调用序列，用于断言行为（真实环境复现这一幕需要 root）。
func recordRunner(t *testing.T, getOutput string, getErr error, failAdd bool) *[]string {
	t.Helper()
	calls := &[]string{}
	previous := routeRunner
	t.Cleanup(func() { routeRunner = previous })
	routeRunner = func(args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(args, " "))
		switch {
		case len(args) >= 3 && args[2] == "get":
			return []byte(getOutput), getErr
		case failAdd && len(args) >= 3 && args[2] == "add":
			return []byte("add net 10.144.0.0: gateway utun1026: File exists"), errors.New("exit status 1")
		default:
			return nil, nil
		}
	}
	return calls
}

// 该网段已被**别的**接口占着（今天线上就是这个状态）⇒ 必须先删再装，且指向新设备。
func TestTakeOverDarwinRouteTakesOverFromAnotherInterface(t *testing.T) {
	calls := recordRunner(t, "   route to: 10.144.0.0\ninterface: utun1025\n", nil, false)

	if err := takeOverDarwinRoute("10.144.0.0/24", "utun1026"); err != nil {
		t.Fatalf("takeOverDarwinRoute: %v", err)
	}
	if len(*calls) != 3 ||
		!strings.Contains((*calls)[0], "get") ||
		!strings.Contains((*calls)[1], "delete") ||
		!strings.Contains((*calls)[2], "add") ||
		!strings.Contains((*calls)[2], "-interface utun1026") {
		t.Fatalf("期望 get→delete→add(指向新接口)，实际: %s", strings.Join(*calls, " | "))
	}
}

// 路由已经指向本设备 ⇒ 幂等:只查询，不做任何增删。
func TestTakeOverDarwinRouteIsIdempotent(t *testing.T) {
	calls := recordRunner(t, "interface: utun1026\n", nil, false)

	if err := takeOverDarwinRoute("10.144.0.0/24", "utun1026"); err != nil {
		t.Fatalf("takeOverDarwinRoute: %v", err)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], "get") {
		t.Fatalf("期望只查询一次，实际: %s", strings.Join(*calls, " | "))
	}
}

// 当前没有该网段的路由 ⇒ 直接装，不要先删。
func TestTakeOverDarwinRouteAddsWhenAbsent(t *testing.T) {
	calls := recordRunner(t, "", errors.New("exit status 1"), false)

	if err := takeOverDarwinRoute("10.144.0.0/24", "utun1026"); err != nil {
		t.Fatalf("takeOverDarwinRoute: %v", err)
	}
	if len(*calls) != 2 || !strings.Contains((*calls)[0], "get") || !strings.Contains((*calls)[1], "add") {
		t.Fatalf("期望 get→add，实际: %s", strings.Join(*calls, " | "))
	}
}

// 装不上必须报错（调用方据此记 Warn），不能假装成功。
func TestTakeOverDarwinRouteReportsAddFailure(t *testing.T) {
	recordRunner(t, "", errors.New("exit status 1"), true)

	if err := takeOverDarwinRoute("10.144.0.0/24", "utun1026"); err == nil {
		t.Fatal("期望返回错误，实际 nil")
	}
}

// 解析器要认得 route(8) 的真实输出形状（含前导空格与其它字段）。
func TestRouteInterfaceForParses(t *testing.T) {
	recordRunner(t, "   route to: 10.144.0.0\n  destination: 10.144.0.0\n    interface: utun1025\n", nil, false)

	name, ok := routeInterfaceFor("10.144.0.0/24")
	if !ok || name != "utun1025" {
		t.Fatalf("期望 utun1025/true，实际 %q/%v", name, ok)
	}
}

// recordRunnerFailingAddFor 与 recordRunner 相同，但**只让指向 failInterface 的那条 add 失败**，
// 用来构造"夺回失败"的场景。
func recordRunnerFailingAddFor(t *testing.T, getOutput string, failInterface string) *[]string {
	t.Helper()
	calls := &[]string{}
	previous := routeRunner
	t.Cleanup(func() { routeRunner = previous })
	routeRunner = func(args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if len(args) >= 3 && args[2] == "get" {
			return []byte(getOutput), nil
		}
		if len(args) >= 3 && args[2] == "add" && strings.Contains(strings.Join(args, " "), "-interface "+failInterface) {
			return []byte("add net 10.144.0.0: gateway " + failInterface + ": File exists"), errors.New("exit status 1")
		}
		return nil, nil
	}
	return calls
}

// 夺回失败时（新设备还没起来等）必须把**原来那条**恢复回去，绝不留下"删了没装回来"的空窗 ✗✓。
func TestTakeOverDarwinRouteRestoresPreviousOwnerOnFailure(t *testing.T) {
	calls := recordRunnerFailingAddFor(t, "interface: utun1025\n", "utun1026")

	err := takeOverDarwinRoute("10.144.0.0/24", "utun1026")
	if err == nil {
		t.Fatal("夺回失败必须回报错误给调用方（它据此记 Warn）")
	}
	joined := strings.Join(*calls, " | ")
	if len(*calls) != 4 ||
		!strings.Contains((*calls)[0], "get") ||
		!strings.Contains((*calls)[1], "delete") ||
		!strings.Contains((*calls)[2], "-interface utun1026") ||
		!strings.Contains((*calls)[3], "-interface utun1025") {
		t.Fatalf("期望 get→delete→add(新失败)→add(恢复旧)，实际: %s", joined)
	}
}
