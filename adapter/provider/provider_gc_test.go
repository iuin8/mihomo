package provider

import (
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func TestSetProxiesCollectsOnlyWhenReplacingOldProxies(t *testing.T) {
	tests := []struct {
		name       string
		oldProxies []C.Proxy
		wantCalled bool
	}{
		{name: "first load", wantCalled: false},
		{name: "replace old proxies", oldProxies: []C.Proxy{newTestProxy("old")}, wantCalled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			old := collectOutdatedProxies
			collectOutdatedProxies = func() { called = true }
			defer func() { collectOutdatedProxies = old }()

			bp := &baseProvider{
				proxies:     tt.oldProxies,
				healthCheck: NewHealthCheck(nil, "", 0, 0, false, nil),
			}
			bp.setProxies(nil)

			if called != tt.wantCalled {
				t.Fatalf("collectOutdatedProxies called = %t, want %t", called, tt.wantCalled)
			}
		})
	}
}

func newTestProxy(string) C.Proxy {
	return adapter.NewProxy(outbound.NewCompatible())
}
