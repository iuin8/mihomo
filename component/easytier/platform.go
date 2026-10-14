package easytier

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"

	"github.com/easytier/easytier/easytier-go/platform"
	D "github.com/miekg/dns"
)

// ServicesWithForward 区分两个出网口（FORK(easytier-exit-egress) ✓）：
//
//   - d：隧道**自身**的流量（会合点、打洞、STUN 探测、数据面会话的连接、DNS 环境）—— 一律直连 ✓，
//     绝不因为"代理不可用"把 overlay 一起拖死 ✗；
//   - forward：**替 overlay 对端拨的内层连接** —— 也就是"家侧借本机出口"真正的出网口 ✓，
//     它应当尊重 mihomo 的 `dialer-proxy` ✓。
//
// **判据是量出来的，不是猜的** ✓（2026-10-14 实验室，带 purpose 日志 ✓）：
//
//	借方:  purpose=5(STUNProbe) → *:3478      ／ purpose=3(Manual) → 出口:11110（数据面会话）
//	出口:  purpose=5(STUNProbe) → *:3478      ／ **purpose=4(ProxyNAT) → 目标:端口  ← 替对端拨的那条**
//
// ⇒ 只有 `TCPConnectProxyNAT` 是"替对端拨号" ✓。先前误用 `TCPConnectDataPlane` ✗：
// 它同时覆盖数据面**会话自身**的连接 ⇒ 一代理就把会话打死（实测：整条出站起不来/全部目标 503 ✗）。
func ServicesWithForward(d, forward C.Dialer) platform.Services {
	return platform.Services{
		Sockets:     SocketFactory{Dialer: d, ForwardDialer: forward},
		DNS:         DNSResolver{},
		Environment: ConnectorEnvironment{Dialer: d},
	}
}

// SocketFactory creates sockets through mihomo's dialer.
//
// EasyTier BindDevice/SocketMark/reuse options are ignored so hole punching
// can bind local UDP ports. Interface, routing-mark, and dialer-proxy stay on
// the mihomo dialer. Internal TCP reservations bind locally even when
// dialer-proxy is set. FakeTCP is not supported.
type SocketFactory struct {
	Dialer C.Dialer
	// ForwardDialer 只用于 `TCPConnectProxyNAT`（替对端转发的内层连接）；为 nil 时回落到 Dialer。
	ForwardDialer C.Dialer
}

// connectDialer 按用途选出网口：**只有替对端拨号**（ProxyNAT）走 ForwardDialer ✓。
func (s SocketFactory) connectDialer(purpose platform.TCPConnectPurpose) C.Dialer {
	if purpose == platform.TCPConnectProxyNAT && s.ForwardDialer != nil {
		return s.ForwardDialer
	}
	return s.Dialer
}

// connectPurposeLog 每个 purpose 只记前几条 —— 用来回答"谁在拨号"：
// 出口替对端转发走哪个 purpose、隧道自身的连接又是哪个（2026-10-14 实测 ✓，见 user-visible 台账）。
var connectPurposeLog sync.Map // platform.TCPConnectPurpose -> *atomic.Uint64

func logConnectPurpose(purpose platform.TCPConnectPurpose, remote *net.TCPAddr) {
	counter, _ := connectPurposeLog.LoadOrStore(purpose, new(atomic.Uint64))
	if count := counter.(*atomic.Uint64).Add(1); count <= 10 {
		log.Debugln("[EasyTier] host TCP connect purpose=%d -> %v (#%d)", purpose, remote, count)
	}
}

func (s SocketFactory) ConnectTCP(ctx context.Context, options platform.TCPConnectOptions) (net.Conn, error) {
	if options.Purpose == platform.TCPConnectFake {
		return nil, fmt.Errorf("easytier: FakeTCP is not supported")
	}
	logConnectPurpose(options.Purpose, options.RemoteAddr)
	// 明确记一条"这条走了哪个出网口、用的是哪个具体 dialer" ✓ —— 一句话定性 ✓。
	if purpose := options.Purpose; purpose == platform.TCPConnectProxyNAT {
		chosen := s.connectDialer(purpose)
		log.Debugln("[EasyTier] connect purpose=%d -> dialer %T (forward=%T)", purpose, chosen, s.ForwardDialer)
	}
	if options.RemoteAddr == nil {
		return nil, fmt.Errorf("easytier: TCP connect is missing a remote address")
	}
	network := tcpNetwork(options.Bind)
	dialer := s.connectDialer(options.Purpose)
	conn, err := dialer.DialContext(ctx, network, options.RemoteAddr.String())
	if err == nil && options.Purpose == platform.TCPConnectProxyNAT {
		// 替对端拨号这条链全程在用户态（不进 /connections、不过 TUN），只能靠自己记字节数判回程 ✓
		conn = wrapConnWithStats(conn, fmt.Sprintf("purpose=%d", options.Purpose), options.RemoteAddr.String())
	}
	if err != nil {
		// 记下"哪个出网口、什么错误" ✓ —— 排查"替对端拨号到底走没走代理"时，这一行就是判据 ✓
		log.Debugln("[EasyTier] TCP connect purpose=%d -> %s via %T failed: %v", options.Purpose, options.RemoteAddr, dialer, err)
	}
	return conn, err
}

func (s SocketFactory) BindUDP(ctx context.Context, options platform.UDPBindOptions) (net.PacketConn, error) {
	network, address := udpBind(options)
	return s.Dialer.ListenPacket(ctx, network, address, netip.AddrPort{})
}

func (s SocketFactory) ListenTCP(ctx context.Context, options platform.TCPListenOptions) (net.Listener, error) {
	if options.Bind.Context.NetNS != nil {
		return nil, fmt.Errorf("easytier: network namespaces are not supported")
	}
	address := ":0"
	if options.Bind.LocalAddr != nil {
		address = options.Bind.LocalAddr.String()
	}
	network := tcpNetwork(options.Bind)
	reuse := options.Bind.ReusePort || options.Bind.ReuseAddr != nil && *options.Bind.ReuseAddr
	listenOpts := []dialer.Option{dialer.WithAddrReuse(reuse)}
	if direct, ok := s.Dialer.(dialer.Dialer); ok {
		listener, err := dialer.Listen(ctx, network, address, append(listenOpts, dialer.WithOption(direct.Opt))...)
		return logHostListener(network, address, options.Purpose, listener, err)
	}
	// FORK(easytier-tun): **监听口永远绑在宿主上，与出站用哪个 dialer 无关** ✗✓。
	// 原来这里对 externalTCPListen 的用途直接报错 —— 而 dialer-proxy 一设，出站 dialer 就变成
	// proxydialer（不满足 dialer.Dialer），于是**整条出站起不来** ✗：
	//   `[EasyTier](x) start failed: … status=-4: required listener failed to start`（实测 ✓）
	// 这正是本函数下面那句注释的原意（"ProxyNAT、端口租约、打洞预留必须绑在宿主上，
	// 即使对端流量走 dialer-proxy" ✓）—— 监听口不需要出站 dialer，也不该受它影响 ✓。
	listener, err := dialer.Listen(ctx, network, address, listenOpts...)
	return logHostListener(network, address, options.Purpose, listener, err)
}

// logHostListener 记录宿主侧 TCP 监听的结果 —— guest 的监听口（数据面会话、端口转发）
// 都落在这里，而失败以前是**静默**的：只有在包永远等不到 ACK 时才看得出问题。
// 另外，绑定在哪个地址上也必须留证：绑错地址（例如只绑物理网卡）时监听是"存在但收不到包"。
func logHostListener(network, address string, purpose platform.TCPListenPurpose, listener net.Listener, err error) (net.Listener, error) {
	if err != nil {
		log.Warnln("[EasyTier] host TCP listener %s %q (purpose=%v) failed: %v", network, address, purpose, err)
		return nil, err
	}
	log.Debugln("[EasyTier] host TCP listener %s %q (purpose=%v) -> bound %s", network, address, purpose, listener.Addr())
	return listener, nil
}

func externalTCPListen(purpose platform.TCPListenPurpose) bool {
	switch purpose {
	case platform.TCPListenDirect, platform.TCPListenManual:
		return true
	default:
		return false
	}
}

func tcpNetwork(options platform.TCPBindOptions) string {
	if options.LocalAddr != nil && options.LocalAddr.IP.To4() != nil {
		return "tcp4"
	}
	if options.OnlyV6 || options.Context.IPVersion == platform.IPVersionV6 {
		return "tcp6"
	}
	if options.Context.IPVersion == platform.IPVersionV4 {
		return "tcp4"
	}
	return "tcp"
}

func udpBind(options platform.UDPBindOptions) (network, address string) {
	if options.LocalAddr != nil {
		address = options.LocalAddr.String()
		if options.LocalAddr.IP.To4() != nil {
			return "udp4", address
		}
		if len(options.LocalAddr.IP) != 0 {
			if options.OnlyV6 || options.LocalAddr.IP.To16() != nil && options.LocalAddr.IP.To4() == nil {
				return "udp6", address
			}
			return "udp", address
		}
		if options.OnlyV6 || options.Context.IPVersion == platform.IPVersionV6 {
			return "udp6", address
		}
		if options.Context.IPVersion == platform.IPVersionV4 {
			return "udp4", address
		}
		return "udp", address
	}
	switch {
	case options.OnlyV6 || options.Context.IPVersion == platform.IPVersionV6:
		return "udp6", "[::]:0"
	case options.Context.IPVersion == platform.IPVersionV4:
		return "udp4", "0.0.0.0:0"
	default:
		return "udp", ":0"
	}
}

// DNSResolver resolves EasyTier control-plane names through the proxy-server resolver.
type DNSResolver struct{}

func (DNSResolver) LookupIP(ctx context.Context, query platform.DNSQuery) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(query.Host); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	switch query.IPVersion {
	case 4:
		return resolver.LookupIPv4WithResolver(ctx, query.Host, resolver.ProxyServerHostResolver)
	case 6:
		return resolver.LookupIPv6WithResolver(ctx, query.Host, resolver.ProxyServerHostResolver)
	default:
		return resolver.LookupIPWithResolver(ctx, query.Host, resolver.ProxyServerHostResolver)
	}
}

func exchangeDNS(ctx context.Context, host string, qtype uint16) (*D.Msg, error) {
	r := resolver.ProxyServerHostResolver
	if r == nil || !r.Invalid() {
		r = resolver.SystemResolver
	}
	if r == nil {
		return nil, fmt.Errorf("easytier: DNS resolver is unavailable")
	}
	request := new(D.Msg)
	request.SetQuestion(D.Fqdn(host), qtype)
	reply, err := r.ExchangeContext(ctx, request)
	if err != nil {
		return nil, err
	}
	if reply == nil {
		return nil, fmt.Errorf("easytier: empty DNS response for %q", host)
	}
	if reply.Rcode != D.RcodeSuccess {
		return nil, fmt.Errorf("easytier: DNS query for %q returned %s", host, D.RcodeToString[reply.Rcode])
	}
	return reply, nil
}

func (DNSResolver) LookupTXT(ctx context.Context, query platform.DNSQuery) (string, error) {
	reply, err := exchangeDNS(ctx, query.Host, D.TypeTXT)
	if err != nil {
		return "", err
	}
	for _, answer := range reply.Answer {
		if txt, ok := answer.(*D.TXT); ok {
			return strings.Join(txt.Txt, ""), nil
		}
	}
	return "", fmt.Errorf("easytier: DNS TXT query for %q returned no records", query.Host)
}

func (DNSResolver) LookupSRV(ctx context.Context, query platform.DNSQuery) ([]*net.SRV, error) {
	reply, err := exchangeDNS(ctx, query.Host, D.TypeSRV)
	if err != nil {
		return nil, err
	}
	var records []*net.SRV
	for _, answer := range reply.Answer {
		if srv, ok := answer.(*D.SRV); ok {
			records = append(records, &net.SRV{Target: srv.Target, Port: srv.Port, Priority: srv.Priority, Weight: srv.Weight})
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("easytier: DNS SRV query for %q returned no records", query.Host)
	}
	return records, nil
}

// ConnectorEnvironment reports the local address used toward a remote UDP peer.
type ConnectorEnvironment struct {
	Dialer C.Dialer
}

func (e ConnectorEnvironment) LocalAddrForRemote(ctx context.Context, remote *net.UDPAddr, _ platform.SocketContext) (net.Addr, error) {
	if remote == nil {
		return nil, fmt.Errorf("easytier: missing remote address")
	}
	network := "udp"
	if remote.IP.To4() != nil {
		network = "udp4"
	} else if remote.IP.To16() != nil {
		network = "udp6"
	}
	conn, err := e.Dialer.DialContext(ctx, network, remote.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr(), nil
}
