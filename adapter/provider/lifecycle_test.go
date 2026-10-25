package provider

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/dialer"
	C "github.com/metacubex/mihomo/constant"
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

func mustProxy(t *testing.T) C.Proxy {
	p, _ := realProxy(t)
	return p
}

func realProxy(t *testing.T) (C.Proxy, *countingAdapter) {
	t.Helper()
	inner := &countingAdapter{}
	return adapter.NewProxy(outbound.NewAutoCloseProxyAdapter(inner)), inner
}

func TestCloseOutdatedProxiesByIdentity(t *testing.T) {
	keep, keepInner := realProxy(t)
	drop, dropInner := realProxy(t)
	replaced, replacedInner := realProxy(t)
	closeOutdatedProxies(
		[]C.Proxy{keep, drop, replaced},
		[]C.Proxy{keep, mustProxy(t)},
	)
	if keepInner.closed.Load() != 0 {
		t.Fatal("a proxy that is still present was closed")
	}
	if dropInner.closed.Load() != 1 {
		t.Fatal("a removed proxy was not closed")
	}
	if replacedInner.closed.Load() != 1 {
		t.Fatal("a proxy replaced by a different object was not closed")
	}
}
