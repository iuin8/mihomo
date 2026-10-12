package easytier

import (
	"net/netip"
	"runtime"
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
