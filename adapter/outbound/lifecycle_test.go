package outbound

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/component/dialer"
	C "github.com/metacubex/mihomo/constant"
)

type fakeAdapter struct {
	ProxyAdapter
	closed atomic.Int32
}

func (f *fakeAdapter) Name() string                 { return "fake" }
func (f *fakeAdapter) DialOptions() []dialer.Option { return nil }
func (f *fakeAdapter) Close() error                 { f.closed.Add(1); return nil }
func (f *fakeAdapter) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	return &fakeConn{}, nil
}

type fakeConn struct {
	C.Conn
	closed atomic.Int32
}

func (c *fakeConn) Close() error { c.closed.Add(1); return nil }

func TestCloseWhenIdleWaitsForActiveConnection(t *testing.T) {
	inner := &fakeAdapter{}
	a := NewAutoCloseProxyAdapter(inner).(*autoCloseProxyAdapter)
	conn, err := a.DialContext(context.Background(), nil)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	a.CloseWhenIdle()
	if inner.closed.Load() != 0 {
		t.Fatal("adapter closed while a connection was still active")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close: %v", err)
	}
	if inner.closed.Load() != 1 {
		t.Fatalf("adapter closed %d times after the last conn, want 1", inner.closed.Load())
	}
}

func TestCloseWhenIdleWithoutConnectionsClosesImmediately(t *testing.T) {
	inner := &fakeAdapter{}
	a := NewAutoCloseProxyAdapter(inner).(*autoCloseProxyAdapter)
	a.CloseWhenIdle()
	if inner.closed.Load() != 1 {
		t.Fatalf("idle adapter closed %d times, want 1", inner.closed.Load())
	}
}

func TestDialAfterCloseWhenIdleFails(t *testing.T) {
	inner := &fakeAdapter{}
	a := NewAutoCloseProxyAdapter(inner).(*autoCloseProxyAdapter)
	a.CloseWhenIdle()
	if _, err := a.DialContext(context.Background(), nil); err == nil {
		t.Fatal("a replaced adapter accepted a new dial")
	}
}

type chainAdapter struct {
	ProxyAdapter
	closed atomic.Int32
	raw    net.Conn
}

func (a *chainAdapter) Name() string                 { return "chain" }
func (a *chainAdapter) Addr() string                 { return "chain:1080" }
func (a *chainAdapter) ProxyInfo() C.ProxyInfo       { return C.ProxyInfo{} }
func (a *chainAdapter) DialOptions() []dialer.Option { return nil }
func (a *chainAdapter) Close() error                 { a.closed.Add(1); return nil }
func (a *chainAdapter) DialContext(context.Context, *C.Metadata) (C.Conn, error) {
	return NewConn(a.raw, a), nil
}

type rawConn struct {
	net.Conn
	closed      atomic.Int32
	closeWrites atomic.Int32
}

func (c *rawConn) Close() error { c.closed.Add(1); return nil }

func (c *rawConn) CloseWrite() error {
	c.closeWrites.Add(1)
	return nil
}

func (c *rawConn) FrontHeadroom() int { return 18 }

type rawConnNoHalfClose struct {
	net.Conn
	closed atomic.Int32
}

func (c *rawConnNoHalfClose) Close() error { c.closed.Add(1); return nil }

func (c *rawConnNoHalfClose) FrontHeadroom() int { return 18 }

func TestAutoReleaseConnPreservesHeadroomAndHalfClose(t *testing.T) {
	raw := &rawConn{}
	ca := &chainAdapter{raw: raw}
	a := NewAutoCloseProxyAdapter(ca).(*autoCloseProxyAdapter)
	conn, err := a.DialContext(context.Background(), nil)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	if got := N.CalculateFrontHeadroom(conn); got != 18 {
		t.Fatalf("front headroom = %d, want 18 (wrapper hides the inner writer)", got)
	}
	a.CloseWhenIdle() // simulate a reload while the connection is active
	cw, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("autoReleaseConn does not expose CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	if raw.closeWrites.Load() != 1 {
		t.Fatalf("inner CloseWrite calls = %d, want 1", raw.closeWrites.Load())
	}
	if raw.closed.Load() != 0 {
		t.Fatal("half-close closed the wrapped connection")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ca.closed.Load() != 1 {
		t.Fatalf("adapter Close calls = %d, want 1", ca.closed.Load())
	}
	if raw.closed.Load() != 1 {
		t.Fatalf("raw Close calls = %d, want 1", raw.closed.Load())
	}
}

func TestAutoReleaseConnCloseWriteFallback(t *testing.T) {
	raw := &rawConnNoHalfClose{}
	ca := &chainAdapter{raw: raw}
	a := NewAutoCloseProxyAdapter(ca).(*autoCloseProxyAdapter)
	conn, err := a.DialContext(context.Background(), nil)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	a.CloseWhenIdle()
	cw, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("autoReleaseConn does not expose CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite fallback: %v", err)
	}
	if ca.closed.Load() != 0 {
		t.Fatal("CloseWrite fallback released the adapter reference")
	}
	if got := N.CalculateFrontHeadroom(conn); got != 18 {
		t.Fatalf("front headroom = %d, want 18", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ca.closed.Load() != 1 {
		t.Fatalf("adapter Close calls = %d, want 1", ca.closed.Load())
	}
}

type fakePacketConn struct {
	C.PacketConn
}

func (c *fakePacketConn) FrontHeadroom() int { return 18 }

func TestAutoReleasePacketConnPreservesHeadroom(t *testing.T) {
	conn := &autoReleasePacketConn{PacketConn: &fakePacketConn{}}
	if got := N.CalculateFrontHeadroom(conn); got != 18 {
		t.Fatalf("packet front headroom = %d, want 18", got)
	}
}
