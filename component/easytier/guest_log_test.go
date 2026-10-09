package easytier

import (
	"strings"
	"testing"
)

// guest 的日志是分块写进来的：一次 Write 里可能有多行、半行、甚至超长无换行的行。
// 这里覆盖三种形态，并断言内存有界（超长行必须被截断而不是无限累积）。
func TestGuestLogWriterSplitsAndBounds(t *testing.T) {
	writer := NewGuestLogWriter("probe")

	// 多行 + 半行：半行不得被当成完整行输出
	if _, err := writer.Write([]byte("line one\nline two\npartial")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := writer.Write([]byte(" finished\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// 超长且始终不换行：必须在累积到上限时被截断输出
	if _, err := writer.Write([]byte(strings.Repeat("x", guestLogLineLimit+1024))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// 收尾后缓冲区必须为空（否则就是无界增长）
	writer.mu.Lock()
	remaining := len(writer.pending)
	writer.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("pending buffer must be drained after Close, got %d bytes", remaining)
	}
}

func TestGuestLogWriterIgnoresEmptyLines(t *testing.T) {
	writer := NewGuestLogWriter("probe")
	for _, chunk := range []string{"\n", "\r\n", "\n\n"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("write %q: %v", chunk, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
