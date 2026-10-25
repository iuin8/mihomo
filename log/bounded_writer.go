package log

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// BoundedWriter decouples log producers from a slow log sink (a pipe whose
// reader is not draining, a blocked terminal, ...). Writes never block: once
// the queue is full, lines are dropped and counted.
type BoundedWriter struct {
	dst     io.Writer
	ch      chan []byte
	dropped atomic.Uint64
	pending atomic.Int64 // lines taken from ch and not yet written

	mu     sync.Mutex
	closed bool
	once   sync.Once
	wg     sync.WaitGroup
}

func NewBoundedWriter(dst io.Writer, capacity int) *BoundedWriter {
	if capacity <= 0 {
		capacity = 1024
	}
	w := &BoundedWriter{dst: dst, ch: make(chan []byte, capacity)}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for b := range w.ch {
			w.pending.Add(1)
			_, _ = w.dst.Write(b)
			w.pending.Add(-1)
		}
	}()
	return w
}

func (w *BoundedWriter) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.dropped.Add(1)
		return len(p), nil
	}
	select {
	case w.ch <- b:
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped returns how many lines were discarded because the sink was too slow
// (or because the writer was already closed).
func (w *BoundedWriter) Dropped() uint64 { return w.dropped.Load() }

// Flush reports whether every queued and in-flight line reached the sink
// before the timeout. It is false when the sink is stuck.
func (w *BoundedWriter) Flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if len(w.ch) == 0 && w.pending.Load() == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Microsecond)
	}
}

// Close stops accepting lines and closes the queue. It waits at most timeout
// for the writer goroutine; a stuck sink makes it return false instead of
// blocking the caller. Lines still queued when the timeout expires are
// abandoned (the process is usually exiting).
func (w *BoundedWriter) Close(timeout time.Duration) bool {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return true
	}
	w.closed = true
	close(w.ch)
	w.mu.Unlock()

	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
