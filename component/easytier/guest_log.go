package easytier

import (
	"bytes"
	"sync"

	"github.com/metacubex/mihomo/log"
)

// guestLogLineLimit 单行最大长度：超长行截断后输出，绝不无界增长。
const guestLogLineLimit = 8 << 10

// GuestLogWriter 把 WASI guest 写到 stdout/stderr 的 tracing 输出按行转进 mihomo 日志。
//
// 为什么需要它：guest 的日志走 `io::stdout()`（easytier/src/common/log/mod.rs:240），
// 而 wazero 在没有挂 Writer 时把这些输出直接丢弃 —— 内嵌实例因此完全不可观测
// （客户端侧只有 ABI 事件，内部 `[USER_PACKET]` 之类的 trace 行一条都看不到）。
//
// 行为约定：
//   - 线程安全（guest 会从多个任务并发写）；
//   - 未换行的残余在 Close 时冲刷，避免半行丢失；
//   - 单行超限则截断，保证内存有界。
type GuestLogWriter struct {
	mu      sync.Mutex
	prefix  string
	pending []byte
}

// NewGuestLogWriter 返回一个带实例名的转发器（prefix 用于区分同一进程里的多个实例）。
func NewGuestLogWriter(prefix string) *GuestLogWriter {
	return &GuestLogWriter{prefix: prefix}
}

// Write 实现 io.Writer。
func (w *GuestLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		w.emit(w.pending[:index])
		w.pending = append(w.pending[:0], w.pending[index+1:]...)
	}
	if len(w.pending) > guestLogLineLimit {
		w.emit(w.pending)
		w.pending = w.pending[:0]
	}
	return len(p), nil
}

// Close 冲刷未换行的残余。
func (w *GuestLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.emit(w.pending)
		w.pending = w.pending[:0]
	}
	return nil
}

func (w *GuestLogWriter) emit(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) == 0 {
		return
	}
	if len(line) > guestLogLineLimit {
		line = line[:guestLogLineLimit]
	}
	log.Debugln("[EasyTier](%s) guest: %s", w.prefix, string(line))
}
