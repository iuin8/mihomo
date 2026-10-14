// FORK(easytier-exit-egress): 惰性 dialer —— `dialer-proxy` 解析出来的 dialer 必须**等拨号时**再构造。
//
// 为什么需要它（2026-10-14 实验室实测 ✓）：
// `BasicOption.NewDialer` 在配了 `dialer-proxy` 时返回 `proxydialer.NewByName(name, tunnel)` ✓，
// 而那是**立即查 mihomo 代理表**的 ✓；easytier 出站在 `init()` 阶段就要把 SocketFactory 交给 shim ✗，
// 那一刻代理表**还没填好** ⇒ `NewByName` 得到 nil ✗ ⇒ 转发拨号**静默回落到直连** ✗。
//
// 症状（实测 ✓）：日志里明明打了 `connect purpose=4 uses the forward dialer` ✓、
// 而作为 dialer-proxy 的代理容器**一个连接都没收到** ✗、延迟却仍然正常 ✓（因为走的是直连 ✓）——
// 这正是"看着生效、其实没生效"的那类坑 ✓。
//
// 包一层惰性构造即可 ✓：第一次真正拨号时才 `build()` ✓。
package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"

	C "github.com/metacubex/mihomo/constant"
)

type lazyDialer struct {
	once  sync.Once
	build func() C.Dialer
	inner C.Dialer
}

func newLazyDialer(build func() C.Dialer) *lazyDialer { return &lazyDialer{build: build} }

func (l *lazyDialer) resolve() C.Dialer {
	l.once.Do(func() { l.inner = l.build() })
	return l.inner
}

func (l *lazyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if dialer := l.resolve(); dialer != nil {
		return dialer.DialContext(ctx, network, address)
	}
	return nil, errors.New("easytier: dialer-proxy is not available")
}

func (l *lazyDialer) ListenPacket(ctx context.Context, network, address string, rAddrPort netip.AddrPort) (net.PacketConn, error) {
	if dialer := l.resolve(); dialer != nil {
		return dialer.ListenPacket(ctx, network, address, rAddrPort)
	}
	return nil, errors.New("easytier: dialer-proxy is not available")
}

var _ C.Dialer = (*lazyDialer)(nil)
