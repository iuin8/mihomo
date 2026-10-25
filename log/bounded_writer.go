package log

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// BoundedWriter decouples log producers from a slow log sink (a pipe whose
// reader is not draining, a blocked terminal, ...). Writes never block:
// once the queue is full, lines are dropped and counted.
type BoundedWriter struct {
	dst     io.Writer
	ch      chan []byte
	dropped atomic.Uint64
	once    sync.Once
	wg      sync.WaitGroup
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
			_, _ = w.dst.Write(b)
		}
	}()
	return w
}

func (w *BoundedWriter) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	select {
	case w.ch <- b:
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped returns how many lines were discarded because the sink was too slow.
func (w *BoundedWriter) Dropped() uint64 { return w.dropped.Load() }

// Flush waits (bounded) for queued lines to reach the sink.
func (w *BoundedWriter) Flush(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for len(w.ch) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func (w *BoundedWriter) Close() error {
	w.once.Do(func() {
		close(w.ch)
		w.wg.Wait()
	})
	return nil
}
