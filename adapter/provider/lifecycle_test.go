package provider

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

func TestCloseOutdatedProxiesByIdentity(t *testing.T) {
	keep, drop, replaced := &idleProxy{}, &idleProxy{}, &idleProxy{}
	closeOutdatedProxies(
		[]C.Proxy{keep, drop, replaced},
		[]C.Proxy{keep, &idleProxy{}},
	)
	if keep.idled.Load() != 0 {
		t.Fatal("a proxy that is still present was closed")
	}
	if drop.idled.Load() != 1 {
		t.Fatal("a removed proxy was not closed")
	}
	if replaced.idled.Load() != 1 {
		t.Fatal("a proxy replaced by a different object was not closed")
	}
}
