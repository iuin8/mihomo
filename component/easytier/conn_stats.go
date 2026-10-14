// FORK(easytier-exit-egress): 替对端拨号那条连接的**字节计数**。
//
// 为什么需要它（2026-10-14 ✓）：出口替对端拨号的路径是
//
//	家侧 → overlay → 本机 guest → shim ConnectTCP(ProxyNAT) → dialer-proxy → 目标
//
// 全程在用户态 ✓，既不出现在 `/connections`、也不经过任何 TUN ✓ ⇒ 从外面完全看不到它 ✓。
// 于是"国外目标不通"到底卡在**去程**还是**回程**，没有任何判据 ✓。
// 包一层 `net.Conn` 记两个数就够了 ✓：
//
//	read=0        ⇒ 目标/节点那一侧根本没把数据送回来 ✗（问题在出口的上游 ✓）
//	read>0        ⇒ 本机拿到了回程数据 ✓ ⇒ 卡在"交给 guest / 数据面会话"这一段 ✗（问题在本 fork 与 shim ✓）
//
// 这是**排查用具** ✓：默认 Debug 级别、且只记前若干条连接 ✓ ⇒ 生产里不刷屏 ✗，
// 需要时把内核日志开到 debug 就能看到每条转发连接的来去字节数 ✓。
package easytier

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/log"
)

const connectStatsLogLimit = 20

var connectStatsLogged atomic.Uint64

// countingConn 记录一条连接上来回各多少字节；Close 时打一条日志。
type countingConn struct {
	net.Conn
	purpose   string
	remote    string
	startedAt time.Time
	read      atomic.Int64
	written   atomic.Int64
	once      sync.Once
}

func wrapConnWithStats(conn net.Conn, purpose, remote string) net.Conn {
	if conn == nil {
		return nil
	}
	wrapped := &countingConn{Conn: conn, purpose: purpose, remote: remote, startedAt: time.Now()}
	// 10 秒后无论连接关没关都打一次 ✓ —— 卡住的连接正是要看的那类（实测：失败时它一直挂着 ✗）
	time.AfterFunc(10*time.Second, wrapped.report)
	return wrapped
}

// report 打一条计数日志（幂等 ✓）。
func (c *countingConn) report() {
	c.once.Do(func() {
		if connectStatsLogged.Add(1) > connectStatsLogLimit {
			return
		}
		read, written := c.read.Load(), c.written.Load()
		verdict := "no data came back (problem is upstream of the exit)"
		if read > 0 {
			verdict = "reply reached the host (look downstream: shim/guest/session)"
		}
		log.Debugln("[EasyTier] forwarded conn %s -> %s: wrote=%d read=%d in %s; %s",
			c.purpose, c.remote, written, read, time.Since(c.startedAt).Round(time.Millisecond), verdict)
	})
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.read.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.written.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Close() error {
	err := c.Conn.Close()
	c.report()
	return err
}
