package easytier

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// TDD（红）：包面桥接的可测缝。runTunBridge 的契约：
//   - device.Read → plane.SendPacket（非 IPv4 包丢弃）
//   - plane.ReceivePacket → device.Write
//   - ctx 取消 → 关闭设备（唤醒阻塞的 Read）、等两条循环退出、返回 nil
//   - 任一侧真实错误 → 返回该错误

type fakePlane struct {
	mu       sync.Mutex
	sent     [][]byte
	incoming chan []byte
	closed   chan struct{}
	sendErr  error
	recvErr  error
}

func newFakePlane() *fakePlane {
	return &fakePlane{incoming: make(chan []byte, 16), closed: make(chan struct{})}
}

func (p *fakePlane) SendPacket(_ context.Context, packet []byte) error {
	if p.sendErr != nil {
		return p.sendErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, append([]byte(nil), packet...))
	return nil
}

func (p *fakePlane) ReceivePacket(ctx context.Context) ([]byte, error) {
	if p.recvErr != nil {
		return nil, p.recvErr
	}
	select {
	case packet := <-p.incoming:
		return packet, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closed:
		return nil, io.EOF
	}
}

func (p *fakePlane) sentPackets() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.sent...)
}

type fakeDevice struct {
	reads    chan []byte
	writes   chan []byte
	readErr  error
	writeErr error
	closed   chan struct{}
	once     sync.Once
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{reads: make(chan []byte, 16), writes: make(chan []byte, 16), closed: make(chan struct{})}
}

func (d *fakeDevice) Read(p []byte) (int, error) {
	if d.readErr != nil {
		return 0, d.readErr
	}
	select {
	case packet := <-d.reads:
		return copy(p, packet), nil
	case <-d.closed:
		return 0, errors.New("device closed")
	}
}

func (d *fakeDevice) Write(p []byte) (int, error) {
	if d.writeErr != nil {
		return 0, d.writeErr
	}
	select {
	case d.writes <- append([]byte(nil), p...):
		return len(p), nil
	case <-d.closed:
		return 0, errors.New("device closed")
	}
}

func (d *fakeDevice) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

func TestRunTunBridgeForwardsDeviceToPlane(t *testing.T) {
	device, plane := newFakeDevice(), newFakePlane()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	packetA := []byte{0x45, 0x00, 0x00, 0x3c, 0x00, 0x01, 0x00, 0x00, 0x40, 0x06, 0x00, 0x00, 10, 0, 0, 1, 10, 0, 0, 2}
	done := make(chan error, 1)
	go func() { done <- runTunBridge(ctx, device, plane) }()

	device.reads <- packetA
	deadline := time.After(2 * time.Second)
	for {
		if len(plane.sentPackets()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("packet not forwarded to plane")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := plane.sentPackets()[0]; string(got) != string(packetA) {
		t.Fatalf("payload mismatch: %v", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancel should end the bridge cleanly, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop after cancel")
	}
}

func TestRunTunBridgeSkipsNonIPv4(t *testing.T) {
	device, plane := newFakeDevice(), newFakePlane()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runTunBridge(ctx, device, plane) }()

	device.reads <- []byte{0x08, 0x06, 0x00, 0x01}                    // 非 IPv4（ARP 风格）
	device.reads <- []byte{0x00}                                      // 过短
	device.reads <- []byte{0x45, 0x00, 0x00, 0x14, 0x00, 0x02, 0x00, 0x00, 0x40, 0x01, 0x00, 0x00, 10, 0, 0, 1, 10, 0, 0, 3} // 合法 IPv4

	deadline := time.After(2 * time.Second)
	for len(plane.sentPackets()) < 1 {
		select {
		case <-deadline:
			t.Fatal("valid packet not forwarded")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := plane.sentPackets(); len(got) != 1 || got[0][9] != 0x01 {
		t.Fatalf("expected only the ICMP packet, got %v", got)
	}

	cancel()
	<-done
}

func TestRunTunBridgeForwardsPlaneToDevice(t *testing.T) {
	device, plane := newFakeDevice(), newFakePlane()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runTunBridge(ctx, device, plane) }()

	packet := []byte{0x45, 0x00, 0x00, 0x14, 0x00, 0x03, 0x00, 0x00, 0x40, 0x11, 0x00, 0x00, 10, 0, 0, 4, 10, 0, 0, 5}
	plane.incoming <- packet

	select {
	case got := <-device.writes:
		if string(got) != string(packet) {
			t.Fatalf("device write mismatch: %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("packet never written to device")
	}

	cancel()
	<-done
}

func TestRunTunBridgePropagatesDeviceError(t *testing.T) {
	device, plane := newFakeDevice(), newFakePlane()
	device.readErr = errors.New("read boom")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	select {
	case err := <-func() chan error { ch := make(chan error, 1); go func() { ch <- runTunBridge(ctx, device, plane) }(); return ch }():
		if err == nil {
			t.Fatal("expected the device error to surface")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not return on device error")
	}
}

func TestRunTunBridgeClosesDeviceOnCancel(t *testing.T) {
	device, plane := newFakeDevice(), newFakePlane()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- runTunBridge(ctx, device, plane) }()
	cancel()

	select {
	case <-device.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("device was not closed on cancel")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected clean shutdown, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit")
	}
}
