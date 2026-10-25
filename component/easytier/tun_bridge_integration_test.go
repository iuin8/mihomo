//go:build integration

package easytier

import (
	"context"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
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
	bridge, err := StartTunBridge(context.Background(), TunDeviceOptions{
		Prefix: netip.MustParsePrefix("10.144.99.1/24"),
	}, plane)
	if err != nil {
		t.Fatalf("StartTunBridge: %v", err)
	}
	if got := interfaceNames(t); len(got) != len(before)+1 {
		t.Fatalf("expected one new interface, before=%v after=%v", before, got)
	}
	if err := bridge.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(interfaceNames(t)) == len(before) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("interfaces not reclaimed after Close: before=%v after=%v", before, interfaceNames(t))
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
