// FORK(easytier-tun): 把 overlay 入向的裸 IP 包投喂给宿主 mihomo 的 TUN 栈。
//
// 为什么需要这一层：sing-tun 的栈只能 Start/Close（stack.go），**没有注入口**，它自己从设备读包；
// 而往 utun 设备里写＝把包交给操作系统内核，并不等于喂给栈。于是这里在"设备 ↔ 栈"之间插一层包装：
// Read 优先返回注入队列里的包，其余仍读真设备。overlay 来的包因此以"来自系统"的身份进入
// mihomo 的路由 / 规则 / NAT —— macOS 上不依赖内核转发与 NAT（那两样在 macOS 上不成立）。
package easytier

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/log"
	tun "github.com/metacubex/sing-tun"
)

const (
	// ingressQueueSize 是注入队列长度。满时丢包：TCP 会重传，绝不能让 overlay 侧阻塞。
	ingressQueueSize = 64
	// ingressBufferSize 覆盖 IPv4 理论上限，避免读真设备时截断。
	ingressBufferSize = 65535
)

var ingressBufferPool = sync.Pool{
	New: func() any {
		buffer := make([]byte, ingressBufferSize)
		return &buffer
	},
}

// IngressSink 是宿主 TUN 的注入口；Inject 在队列满时丢弃并返回 false。
type IngressSink interface {
	Inject(packet []byte) bool
}

// 宿主 TUN 的注入口登记。用互斥锁而不是 atomic.Value：存储的是接口值，
// atomic.Value 对不一致的具体类型会 panic，而登记/清理都很低频。
var (
	ingressSinkMu sync.Mutex
	hostIngress   IngressSink
)

// PublishIngressSink 登记宿主 TUN 的注入口，供入向搬运使用。
func PublishIngressSink(sink IngressSink) {
	if sink == nil {
		return
	}
	ingressSinkMu.Lock()
	hostIngress = sink
	ingressSinkMu.Unlock()
}

// ClearIngressSink 只清理仍属于自己的那条登记（避免把后登记的实例清掉）。
func ClearIngressSink(sink IngressSink) {
	if sink == nil {
		return
	}
	ingressSinkMu.Lock()
	if hostIngress == sink {
		hostIngress = nil
	}
	ingressSinkMu.Unlock()
}

// CurrentIngressSink 返回当前宿主 TUN 的注入口；没有可用 TUN 时返回 nil。
func CurrentIngressSink() IngressSink {
	ingressSinkMu.Lock()
	defer ingressSinkMu.Unlock()
	return hostIngress
}

type tunReadResult struct {
	buffer *[]byte
	length int
	err    error
}

// injectableTunBase 是多路复用的本体：写与关闭透传，读在"注入队列"与"真设备"之间二选一。
//
// 交给栈用的具体类型由按 build tag 拆分的包装器提供：
// sing-tun 的 gvisor 栈会**类型断言**要求设备额外实现 GVisorTun（WritePacket + NewEndpoint），
// 只内嵌 tun.Tun 接口会让断言失败 → "gVisor stack is unsupported on current platform" → TUN 起不来。
type injectableTunBase struct {
	tun.Tun
	injected      chan []byte
	reads         chan tunReadResult
	done          chan struct{}
	closeOne      sync.Once
	pumpAllowed   atomic.Bool // gvisor 路径下由真 endpoint 独占读 fd，pump 必须停
	injectedCount atomic.Uint64
	consumedCount atomic.Uint64
	droppedCount  atomic.Uint64
}

// newInjectableTunBase 造好多路复用本体并起协程；由按 tag 定义的包装器调用。
func newInjectableTunBase(device tun.Tun) *injectableTunBase {
	base := &injectableTunBase{
		Tun:      device,
		injected: make(chan []byte, ingressQueueSize),
		reads:    make(chan tunReadResult, ingressQueueSize),
		done:     make(chan struct{}),
	}
	base.pumpAllowed.Store(true)
	go base.pump()
	go base.report()
	log.Debugln("[EasyTier] host TUN wrapped for overlay ingress")
	return base
}

// report 周期上报入向投喂计数（Debug 级且仅在真的有流量时），用于排障：
// injected>0 而 consumed==0 表示栈没有读走投喂的包。
func (t *injectableTunBase) report() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			injected, consumed, dropped := t.injectedCount.Load(), t.consumedCount.Load(), t.droppedCount.Load()
			if injected == 0 && consumed == 0 && dropped == 0 {
				continue
			}
			log.Debugln("[EasyTier] overlay ingress: injected=%d consumed=%d dropped=%d", injected, consumed, dropped)
		}
	}
}

// Inject 把一个裸 IP 包排进注入队列。
func (t *injectableTunBase) Inject(packet []byte) bool {
	if t == nil || len(packet) == 0 {
		return false
	}
	copied := make([]byte, len(packet))
	copy(copied, packet)
	select {
	case t.injected <- copied:
		t.injectedCount.Add(1)
		return true
	case <-t.done:
		return false
	default:
		t.droppedCount.Add(1)
		return false
	}
}

// Dropped 返回因队列满被丢弃的注入包数（排障用）。
func (t *injectableTunBase) Dropped() uint64 {
	if t == nil {
		return 0
	}
	return t.droppedCount.Load()
}

// pump 把真设备的读搬到 reads，使 Read 能在两个来源之间 select。
// stopPump 停掉"从真设备读"的协程：gvisor 路径下真 endpoint 才是 fd 的唯一读者。
func (t *injectableTunBase) stopPump() {
	if t.pumpAllowed.CompareAndSwap(true, false) {
		log.Debugln("[EasyTier] ingress: real endpoint owns the TUN fd; pump stopped")
	}
}

func (t *injectableTunBase) pump() {
	defer close(t.reads)
	for {
		if !t.pumpAllowed.Load() {
			return
		}
		buffer := ingressBufferPool.Get().(*[]byte)
		length, err := t.Tun.Read(*buffer)
		if err != nil {
			ingressBufferPool.Put(buffer)
			select {
			case t.reads <- tunReadResult{err: err}:
			case <-t.done:
			}
			return
		}
		select {
		case t.reads <- tunReadResult{buffer: buffer, length: length}:
		case <-t.done:
			ingressBufferPool.Put(buffer)
			return
		}
	}
}

func (t *injectableTunBase) Read(p []byte) (int, error) {
	select {
	case packet := <-t.injected:
		t.consumedCount.Add(1)
		return copy(p, packet), nil
	default:
	}
	select {
	case packet := <-t.injected:
		t.consumedCount.Add(1)
		return copy(p, packet), nil
	case result, ok := <-t.reads:
		if !ok {
			return 0, io.EOF
		}
		if result.err != nil {
			return 0, result.err
		}
		length := copy(p, (*result.buffer)[:result.length])
		ingressBufferPool.Put(result.buffer)
		return length, nil
	case <-t.done:
		return 0, io.EOF
	}
}

// Close 关掉多路复用并透传关闭真设备。
func (t *injectableTunBase) Close() error {
	t.closeOne.Do(func() { close(t.done) })
	return t.Tun.Close()
}
