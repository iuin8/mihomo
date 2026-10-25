package tunnel_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/dialer"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
)

type countingAdapter struct {
	outbound.ProxyAdapter
	closed atomic.Int32
}

func (c *countingAdapter) Name() string                 { return "counting" }
func (c *countingAdapter) DialOptions() []dialer.Option { return nil }
func (c *countingAdapter) Close() error                 { c.closed.Add(1); return nil }
func (c *countingAdapter) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	return &countingConn{}, nil
}

type countingConn struct{ C.Conn }

func (c *countingConn) Close() error { return nil }

// realProxy mirrors what the parser hands to the tunnel: *adapter.Proxy
// wrapping the close-when-idle adapter.
func realProxy(t *testing.T) (C.Proxy, *countingAdapter) {
	t.Helper()
	inner := &countingAdapter{}
	p := adapter.NewProxy(outbound.NewAutoCloseProxyAdapter(inner))
	var _ interface{ CloseWhenIdle() } = p // real *adapter.Proxy must forward it
	return p, inner
}

func TestUpdateProxiesClosesReplacedByIdentity(t *testing.T) {
	prev := tunnel.Proxies()
	t.Cleanup(func() { tunnel.UpdateProxies(prev, nil) })

	a, aInner := realProxy(t)
	b, bInner := realProxy(t)
	tunnel.UpdateProxies(map[string]C.Proxy{"a": a, "b": b}, nil)
	tunnel.UpdateProxies(map[string]C.Proxy{"a": a}, nil) // a kept, b removed
	if got := aInner.closed.Load(); got != 0 {
		t.Fatalf("kept proxy closed %d times", got)
	}
	if got := bInner.closed.Load(); got != 1 {
		t.Fatalf("removed proxy closed %d times, want 1", got)
	}

	replacement, replInner := realProxy(t)
	tunnel.UpdateProxies(map[string]C.Proxy{"a": replacement}, nil) // same name, new object
	if got := aInner.closed.Load(); got != 1 {
		t.Fatalf("replaced proxy closed %d times, want 1", got)
	}
	if got := replInner.closed.Load(); got != 0 {
		t.Fatalf("new proxy closed %d times", got)
	}
}
