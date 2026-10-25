//go:build integration

package easytier_test

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
)

// Requires a real TUN device: run explicitly as root, e.g.
//
//	sudo go test -tags integration ./component/easytier -run TestTunBridgeIntegrationDeviceLifecycle -v
//
// It is not part of the default suite and must fail (not skip) when the device
// cannot be created.
func TestTunBridgeIntegrationDeviceLifecycle(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("needs root/CAP_NET_ADMIN to create a TUN device; run with sudo")
	}
	before := interfaceNames(t)
	plane := &blockingPlane{}
	bridge, err := easytier.StartTunBridge(context.Background(), easytier.TunDeviceOptions{
		Prefix: netip.MustParsePrefix("10.250.99.1/24"),
	}, plane)
	if err != nil {
		t.Fatalf("StartTunBridge: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	if got := interfaceNames(t); len(got) != len(before)+1 {
		t.Fatalf("expected one new interface, before=%v after=%v", before, got)
	}
	if err := bridge.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := interfaceNames(t); len(got) != len(before) {
		t.Fatalf("Close returned but the interface was still present: before=%v after=%v", before, got)
	}
}

type blockingPlane struct{}

func (p *blockingPlane) SendPacket(context.Context, []byte) error { return nil }
func (p *blockingPlane) ReceivePacket(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func interfaceNames(t *testing.T) []string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces: %v", err)
	}
	names := make([]string, 0, len(ifaces))
	for _, i := range ifaces {
		names = append(names, i.Name)
	}
	return names
}

// TestTunBridgeIntegrationReloadKeepsActiveConnection covers the original
// defect end to end: a real TUN bridge behind a real *adapter.Proxy, an active
// connection, then a config reload. The old bridge must stay up until the
// connection closes, and only then must its interface disappear.
func TestTunBridgeIntegrationReloadKeepsActiveConnection(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("needs root/CAP_NET_ADMIN to create a TUN device; run with sudo")
	}
	before := interfaceNames(t)
	ctx := context.Background()

	bridge, err := easytier.StartTunBridge(ctx, easytier.TunDeviceOptions{
		Prefix: netip.MustParsePrefix("10.250.98.1/24"),
	}, &blockingPlane{})
	if err != nil {
		t.Fatalf("StartTunBridge: %v", err)
	}

	oldAdapter := &bridgeAdapter{bridge: bridge}
	oldProxy := adapter.NewProxy(outbound.NewAutoCloseProxyAdapter(oldAdapter))
	keepAdapter := &bridgeAdapter{} // no bridge: Close just records
	keepProxy := adapter.NewProxy(outbound.NewAutoCloseProxyAdapter(keepAdapter))
	replacement := adapter.NewProxy(outbound.NewAutoCloseProxyAdapter(&bridgeAdapter{}))

	prev := tunnel.Proxies()
	tunnel.UpdateProxies(map[string]C.Proxy{"et-old": oldProxy, "keep": keepProxy}, nil)
	t.Cleanup(func() { tunnel.UpdateProxies(prev, nil) })

	conn, err := oldProxy.DialContext(ctx, &C.Metadata{})
	if err != nil {
		t.Fatalf("dial through the real proxy: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	t.Cleanup(func() { _ = conn.Close() })

	// Reload replaces et-old but keeps the active connection.
	tunnel.UpdateProxies(map[string]C.Proxy{"et-new": replacement, "keep": keepProxy}, nil)
	if oldAdapter.closed.Load() != 0 {
		t.Fatal("old bridge closed while its connection was still active")
	}
	if got := interfaceNames(t); len(got) != len(before)+1 {
		t.Fatalf("active bridge lost its interface during reload: before=%v after=%v", before, got)
	}
	if keepAdapter.closed.Load() != 0 {
		t.Fatal("an unchanged proxy was closed by the reload")
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close: %v", err)
	}
	if got := oldAdapter.closed.Load(); got != 1 {
		t.Fatalf("old bridge closed %d times after the last connection ended, want 1", got)
	}
	if got := interfaceNames(t); len(got) != len(before) {
		t.Fatalf("Close returned but the old interface was still present: before=%v after=%v", before, got)
	}
}

type bridgeAdapter struct {
	outbound.ProxyAdapter
	bridge *easytier.TunBridge
	closed atomic.Int32
}

func (b *bridgeAdapter) Name() string                 { return "et-test" }
func (b *bridgeAdapter) DialOptions() []dialer.Option { return nil }
func (b *bridgeAdapter) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	return &bridgeConn{}, nil
}
func (b *bridgeAdapter) Close() error {
	b.closed.Add(1)
	if b.bridge != nil {
		return b.bridge.Close()
	}
	return nil
}

type bridgeConn struct{ C.Conn }

func (c *bridgeConn) Close() error { return nil }
