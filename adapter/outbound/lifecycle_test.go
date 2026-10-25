package outbound

import (
	"context"
	"sync/atomic"
	"testing"

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
