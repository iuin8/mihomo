package log

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	got     []string
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	b.mu.Lock()
	b.got = append(b.got, strings.TrimSpace(string(p)))
	b.mu.Unlock()
	return len(p), nil
}

func TestBoundedWriterDoesNotBlockSlowSink(t *testing.T) {
	bw := newBlockingWriter()
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
	if w.Dropped() == 0 {
		t.Fatal("expected dropped lines with a stuck sink")
	}
	close(bw.release)
	if !w.Close(time.Second) {
		t.Fatal("Close did not finish after the sink was released")
	}
}

func TestFlushWaitsForInFlightLine(t *testing.T) {
	bw := newBlockingWriter()
	w := NewBoundedWriter(bw, 4)
	defer w.Close(time.Second)
	if _, err := w.Write([]byte("only")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Do not wait for the writer goroutine: the producer already recorded the
	// line as pending, so Flush must not claim success during the hand-off.
	if w.Flush(50 * time.Millisecond) {
		t.Fatal("Flush reported success while the line was still in flight")
	}
	<-bw.started
	if w.Flush(50 * time.Millisecond) {
		t.Fatal("Flush reported success while the sink was still blocked")
	}
	close(bw.release)
	if !w.Flush(time.Second) {
		t.Fatal("Flush did not observe the line reaching the sink")
	}
	if len(bw.got) != 1 {
		t.Fatalf("sink got %d lines, want 1", len(bw.got))
	}
}

func TestCloseIsBoundedAndRepeatedCallsAreHonest(t *testing.T) {
	bw := newBlockingWriter()
	w := NewBoundedWriter(bw, 4)
	if _, err := w.Write([]byte("line")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	<-bw.started
	start := time.Now()
	if w.Close(100 * time.Millisecond) {
		t.Fatal("Close claimed success while the sink was stuck")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close blocked: %v", elapsed)
	}
	if w.Close(100 * time.Millisecond) {
		t.Fatal("second Close claimed success while the writer was still stuck")
	}
	close(bw.release)
	if !w.Close(time.Second) {
		t.Fatal("Close did not report completion after the sink was released")
	}

	before := w.Dropped()
	if _, err := w.Write([]byte("late")); err != nil {
		t.Fatalf("Write after Close: %v", err)
	}
	if w.Dropped() != before+1 {
		t.Fatalf("Write after Close: dropped=%d, want %d", w.Dropped(), before+1)
	}
}

func TestFatalFlushesLastLine(t *testing.T) {
	if os.Getenv("LOG_FATAL_CHILD") == "1" {
		Fatalln("fatal-last-line")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestFatalFlushesLastLine")
	cmd.Env = append(os.Environ(), "LOG_FATAL_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("child did not exit through Fatalln")
	}
	if !strings.Contains(string(out), "fatal-last-line") {
		t.Fatalf("fatal line lost; child output:\n%s", out)
	}
}
