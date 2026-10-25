package log

import (
	"strings"
	"testing"
	"time"
)

type blockingWriter struct {
	release chan struct{}
	got     []string
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	b.got = append(b.got, strings.TrimSpace(string(p)))
	return len(p), nil
}

func TestBoundedWriterDoesNotBlockSlowSink(t *testing.T) {
	bw := &blockingWriter{release: make(chan struct{})}
	w := NewBoundedWriter(bw, 2)
	start := time.Now()
	for i := 0; i < 100; i++ {
		if _, err := w.Write([]byte("line")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Write blocked on a stuck sink: %v", elapsed)
	}
	if got := w.Dropped(); got == 0 {
		t.Fatalf("expected dropped lines with a stuck sink, got %d", got)
	}
	close(bw.release)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(bw.got) == 0 {
		t.Fatal("queued lines never reached the sink")
	}
}
