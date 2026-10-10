package easytier

import (
	"context"
	"errors"
	"io"
	"net/netip"
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
	go func() { done <- runTunBridge(ctx, device, plane, netip.Prefix{}) }()

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
	go func() { done <- runTunBridge(ctx, device, plane, netip.Prefix{}) }()

	device.reads <- []byte{0x08, 0x06, 0x00, 0x01}                                                                           // 非 IPv4（ARP 风格）
	device.reads <- []byte{0x00}                                                                                             // 过短
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
	go func() { done <- runTunBridge(ctx, device, plane, netip.Prefix{}) }()

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
	case err := <-func() chan error {
		ch := make(chan error, 1)
		go func() { ch <- runTunBridge(ctx, device, plane, netip.Prefix{}) }()
		return ch
	}():
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
	go func() { done <- runTunBridge(ctx, device, plane, netip.Prefix{}) }()
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

// —— FORK(easytier-tun)：入向改道（overlay → 宿主 mihomo TUN 栈）的可测缝 ——

type recordingWriter struct {
	packets [][]byte
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	copied := make([]byte, len(p))
	copy(copied, p)
	w.packets = append(w.packets, copied)
	return len(p), nil
}

type recordingSink struct{ packets [][]byte }

func (s *recordingSink) Inject(packet []byte) bool {
	copied := make([]byte, len(packet))
	copy(copied, packet)
	s.packets = append(s.packets, copied)
	return true
}

func ipv4Packet(dst [4]byte) []byte {
	packet := make([]byte, 20)
	packet[0] = 0x45
	packet[16], packet[17], packet[18], packet[19] = dst[0], dst[1], dst[2], dst[3]
	return packet
}

func TestPacketDestInPrefix(t *testing.T) {
	overlay := netip.MustParsePrefix("10.144.0.0/24")
	if !packetDestIsOtherOverlayPeer(ipv4Packet([4]byte{10, 144, 0, 6}), overlay) {
		t.Fatal("overlay destination must be inside the prefix")
	}
	if packetDestIsOtherOverlayPeer(ipv4Packet([4]byte{10, 144, 1, 6}), overlay) {
		t.Fatal("non-overlay destination must be outside the prefix")
	}
	if packetDestIsOtherOverlayPeer(ipv4Packet([4]byte{10, 144, 0, 6}), netip.Prefix{}) {
		t.Fatal("invalid prefix must match nothing")
	}
}

// 有宿主 TUN 注入口时：公网目标与**本机自己**的 overlay 目标都投喂进栈；
// 只有**其它** overlay 节点仍留在本地 TUN（投喂它们会让 mihomo 再拨一次、绕回 overlay）。
//
// 2026-10-10 实测修正：旧版本把"本机自己的 overlay 地址"也留在设备上，注释里记为
// "macOS 上走不通"的已知限制 —— 那正是生产上"家侧经本机出口打公网 502/Timeout"的原因：
// 本机经远端出口的连接，回程包目的地**就是本机 overlay 地址**，留在设备上＝永远不进 mihomo 的栈。
func TestIngressSinkPrefersHostTun(t *testing.T) {
	sink := &recordingSink{}
	PublishIngressSink(sink)
	defer ClearIngressSink(sink)
	device := &recordingWriter{}
	// 生产的 Prefix 是**主机自己的地址**/前缀（CreateTunDevice 把它配到设备上），不是网络地址。
	ingress := &ingressSink{device: device, local: netip.MustParsePrefix("10.144.0.6/24")}

	if err := ingress.put(ipv4Packet([4]byte{1, 1, 1, 1})); err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(sink.packets) != 1 || len(device.packets) != 0 {
		t.Fatalf("public destination must go to the host TUN: sink=%d device=%d", len(sink.packets), len(device.packets))
	}

	// 目的地就是本机自己（10.144.0.6 = local 的前缀地址）：必须投喂进栈 ——
	// 本机经远端出口的连接，回程包就是这个形状。
	if err := ingress.put(ipv4Packet([4]byte{10, 144, 0, 6})); err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(sink.packets) != 2 || len(device.packets) != 0 {
		t.Fatalf("packets for our own overlay address must be injected: sink=%d device=%d", len(sink.packets), len(device.packets))
	}

	// 其它 overlay 节点（不是本机地址）：仍交给内核按 10.144/24 路由回隧道。
	deviceBefore, sinkBefore := len(device.packets), len(sink.packets)
	if err := ingress.put(ipv4Packet([4]byte{10, 144, 0, 1})); err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(device.packets) != deviceBefore+1 || len(sink.packets) != sinkBefore {
		t.Fatalf("packets for other overlay nodes must stay on the local TUN: device %d→%d sink %d→%d",
			deviceBefore, len(device.packets), sinkBefore, len(sink.packets))
	}
}

// 没有宿主 TUN（未启用 tun 入站）时退回本地 TUN，不丢包。
func TestIngressSinkFallsBackWithoutHostTun(t *testing.T) {
	device := &recordingWriter{}
	ingress := &ingressSink{device: device}
	if err := ingress.put(ipv4Packet([4]byte{1, 1, 1, 1})); err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(device.packets) != 1 {
		t.Fatalf("expected fallback to the local TUN, got %d", len(device.packets))
	}
}

// 包装后的设备：注入的包要能被栈读到，真设备的包照旧透传。
func TestWrapTunForIngressDeliversInjectedPackets(t *testing.T) {
	raw := &recordingTun{incoming: make(chan []byte, 1)}
	wrapped, sink := WrapTunForIngress(raw)
	defer wrapped.Close()

	injected := ipv4Packet([4]byte{1, 1, 1, 1})
	if !sink.Inject(injected) {
		t.Fatal("inject must succeed while the queue has room")
	}
	buffer := make([]byte, 64)
	n, err := wrapped.Read(buffer)
	if err != nil || n != len(injected) {
		t.Fatalf("read injected: n=%d err=%v", n, err)
	}

	fromDevice := ipv4Packet([4]byte{8, 8, 8, 8})
	raw.incoming <- fromDevice
	n, err = wrapped.Read(buffer)
	if err != nil || n != len(fromDevice) {
		t.Fatalf("read from device: n=%d err=%v", n, err)
	}
}

type recordingTun struct {
	incoming chan []byte
	closed   bool
}

func (t *recordingTun) Read(p []byte) (int, error) {
	packet, ok := <-t.incoming
	if !ok {
		return 0, io.EOF
	}
	return copy(p, packet), nil
}

func (t *recordingTun) Write(p []byte) (int, error) { return len(p), nil }

func (t *recordingTun) Close() error {
	if !t.closed {
		t.closed = true
		close(t.incoming)
	}
	return nil
}

// 校验和判据的最小自检：构造一个真实合法的 IPv4/TCP SYN 逐字段校验，再故意改坏一字节。
func TestChecksumValidation(t *testing.T) {
	packet := ipv4Packet([4]byte{10, 144, 0, 6})
	packet[9] = 6 // TCP
	packet[12], packet[13], packet[14], packet[15] = 10, 144, 0, 1
	packet = append(packet, 0x1f, 0x90, 0x01, 0xbb, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
		0x50, 0x02, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00)
	packet[2], packet[3] = byte(len(packet)>>8), byte(len(packet))
	packet[10], packet[11] = 0, 0
	headerLength := int(packet[0]&0x0f) * 4
	header := foldedChecksum(checksum(packet[:headerLength], 0))
	packet[10], packet[11] = byte(header>>8), byte(header)

	if !ipv4ChecksumValid(packet) {
		t.Fatal("a correctly built IPv4 header must validate")
	}
	packet[10] ^= 0xff
	if ipv4ChecksumValid(packet) {
		t.Fatal("a corrupted IPv4 header must NOT validate")
	}
	packet[10] ^= 0xff

	// TCP：先填一个正确校验和 → 必须通过；改坏一字节 → 必须失败。
	segment := packet[headerLength:]
	tcpHeader := foldedChecksum(checksum(segment, checksum(packet[12:20], uint32(6)+uint32(len(segment)))))
	segment[16], segment[17] = byte(tcpHeader>>8), byte(tcpHeader)
	if !l4ChecksumValid(packet) {
		t.Fatal("a correctly built TCP checksum must validate")
	}
	segment[16] ^= 0xff
	if l4ChecksumValid(packet) {
		t.Fatal("a corrupted TCP checksum must NOT validate")
	}
}
