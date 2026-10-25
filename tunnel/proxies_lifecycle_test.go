package tunnel

import (
	"sync/atomic"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

type idleProxy struct {
	C.Proxy
	idled atomic.Int32
}

func (p *idleProxy) CloseWhenIdle() { p.idled.Add(1) }

func TestUpdateProxiesClosesReplacedByIdentity(t *testing.T) {
	prev := Proxies()
	t.Cleanup(func() { UpdateProxies(prev, nil) })

	a, b := &idleProxy{}, &idleProxy{}
	UpdateProxies(map[string]C.Proxy{"a": a, "b": b}, nil)
	UpdateProxies(map[string]C.Proxy{"a": a}, nil) // a kept, b removed
	if got := a.idled.Load(); got != 0 {
		t.Fatalf("kept proxy closed %d times", got)
	}
	if got := b.idled.Load(); got != 1 {
		t.Fatalf("removed proxy closed %d times, want 1", got)
	}

	replacement := &idleProxy{}
	UpdateProxies(map[string]C.Proxy{"a": replacement}, nil) // same name, new object
	if got := a.idled.Load(); got != 1 {
		t.Fatalf("replaced proxy closed %d times, want 1", got)
	}
	if got := replacement.idled.Load(); got != 0 {
		t.Fatalf("new proxy closed %d times", got)
	}
}
