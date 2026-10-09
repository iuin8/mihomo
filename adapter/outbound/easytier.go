//go:build !no_easytier

package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/component/easytier"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/log"

	"crypto/rand"
	"encoding/hex"
	corehost "github.com/easytier/easytier/easytier-go"
	D "github.com/miekg/dns"
)

const (
	easyTierDefaultStateDir = "easytier"
	easyTierInstanceIDFile  = "instance_id"
	easyTierDNSTTL          = 60
	easyTierMinBackoff      = time.Second
	easyTierMaxBackoff      = 30 * time.Second
)

var errEasyTierClosed = errors.New("easytier outbound closed")

type EasyTier struct {
	*Base
	option     EasyTierOption
	configTOML string
	stateDir   string
	instanceID string
	zone       string
	ctx        context.Context
	cancel     context.CancelFunc
	loopOnce   sync.Once
	startMu    sync.Mutex
	closed     bool
	readyCh    chan struct{}
	mu         sync.Mutex
	host       *corehost.Host
	instance   *corehost.Instance
	unregister func()
	// FORK(easytier-guestlog): 转发 guest 的 stdout/stderr，Close 时冲刷残余
	guestLog *easytier.GuestLogWriter
	// FORK(easytier-tun): TUN 模式资源，见 docs/easytier_tun_spec.md
	tunBridge *easytier.TunBridge
	tunPrefix netip.Prefix
	tunRoutes []netip.Prefix
}

type EasyTierOption struct {
	BasicOption
	Name            string   `proxy:"name"`
	NetworkName     string   `proxy:"network-name,omitempty"`
	NetworkSecret   string   `proxy:"network-secret,omitempty"`
	Hostname        string   `proxy:"hostname,omitempty"`
	IPv4            string   `proxy:"ipv4,omitempty"`
	DHCP            bool     `proxy:"dhcp,omitempty"`
	Peers           []string `proxy:"peers,omitempty"`
	Listeners       []string `proxy:"listeners,omitempty"`
	NoListener      *bool    `proxy:"no-listener,omitempty"`
	MappedListeners []string `proxy:"mapped-listeners,omitempty"`
	ExitNodes       []string `proxy:"exit-nodes,omitempty"`
	// FORK(easytier-stun): 覆盖 STUN 列表 ✓ —— 留空则用 fork 默认（v6 关闭 ✓，见 component/easytier/toml.go）
	STUNServers         []string `proxy:"stun-servers,omitempty"`
	STUNServersV6       []string `proxy:"stun-servers-v6,omitempty"`
	ProxyNetworks       []string `proxy:"proxy-networks,omitempty"`
	InstanceName        string   `proxy:"instance-name,omitempty"`
	StateDir            string   `proxy:"state-dir,omitempty"`
	UDP                 bool     `proxy:"udp,omitempty"`
	AcceptDNS           *bool    `proxy:"accept-dns,omitempty"`
	EnableExitNode      *bool    `proxy:"enable-exit-node,omitempty"`
	EnableEncryption    *bool    `proxy:"enable-encryption,omitempty"`
	EncryptionAlgorithm string   `proxy:"encryption-algorithm,omitempty"`
	PrivateMode         *bool    `proxy:"private-mode,omitempty"`
	LatencyFirst        *bool    `proxy:"latency-first,omitempty"`
	DisableP2P          *bool    `proxy:"disable-p2p,omitempty"`
	EnableKCPProxy      *bool    `proxy:"enable-kcp-proxy,omitempty"`
	DisableKCPInput     *bool    `proxy:"disable-kcp-input,omitempty"`
	EnableQUICProxy     *bool    `proxy:"enable-quic-proxy,omitempty"`
	DisableQUICInput    *bool    `proxy:"disable-quic-input,omitempty"`
	MTU                 int      `proxy:"mtu,omitempty"`
	// FORK(easytier-tun): 宿主 TUN 模式；tun-routes 为 pin 进该设备的前缀列表。
	Tun       bool     `proxy:"tun,omitempty"`
	TunRoutes []string `proxy:"tun-routes,omitempty"`
	// FORK(easytier-prewarm): 服务端角色（如家里那台网关）不会有任何流量把这个出站当代理用，
	// 懒启动因此永远不会发生 —— 实例不启动、TUN 不创建，客户端根本连不进来。
	// 置 true 表示"构造完就把它起起来"。实现上只调用一次上游的 ensureStarted()，
	// 之后的健康检查与重建由上游 loop() 负责，不会出现重复 init/shutdown 式的泄漏。
	Prewarm         bool   `proxy:"prewarm,omitempty"`
	TLDDNSZone      string `proxy:"tld-dns-zone,omitempty"`
	SecureMode      *bool  `proxy:"secure-mode,omitempty"`
	LocalPrivateKey string `proxy:"local-private-key,omitempty"`
	LocalPublicKey  string `proxy:"local-public-key,omitempty"`
}

// FORK(easytier-identity) 的兜底实现：hostname 必须**跨机唯一** ✗✓
//
// 为什么不能只拼"宿主名-出站名" ✗：宿主机名会重名 ✓（两台 MacBook-Pro ✓、从同一模板克隆的
// VM ✓、容器里的 localhost ✓）→ 同一份订阅发到多台机器就会全部同名 ✓ → overlay 视作同一
// 节点 ✓（症状：同一时刻只有一个能用，且重新激活后翻转 ✓）。
//
// 所以再拼一段**持久化的随机后缀** ✓，写在 <state-dir>/host-id ✓：
//
//	· 默认 state-dir 是 easytier/<出站名> ✓ → 每个「主机 × 出站」各有一份 ✓ → 天然唯一 ✓；
//	· **由本 fork 拥有** ✓，刻意不去碰 guest 自己的 machine_id 文件 ✗（避免两边争写 ✓）；
//	· 名字问题**绝不能让出站起不来** ✓：任何一步失败都退回不带后缀的形态 ✓。

// safeEasyTierStateDir 解析并校验 state 目录 ✓ —— 两处调用共用同一套规则 ✓：
//
//	· 构造出站时：不安全**直接报错** ✓（不放过）；
//	· 生成兜底 hostname 时：不安全就**退化为不带随机后缀** ✓（不让命名问题拖垮出站 ✓）。
func safeEasyTierStateDir(o EasyTierOption) (string, bool) {
	dir := o.StateDir
	if dir == "" {
		dir = filepath.Join(easyTierDefaultStateDir, o.Name)
	}
	dir = C.Path.Resolve(dir)
	return dir, C.Path.IsSafePath(dir)
}

func defaultEasyTierHostname(name, stateDir string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "host"
	}
	base := sanitizeHostnameLabel(host + "-" + name)
	if suffix := persistedHostIDSuffix(stateDir); suffix != "" {
		return base + "-" + suffix
	}
	return base
}

func persistedHostIDSuffix(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	path := filepath.Join(stateDir, "host-id")
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); len(s) == 8 {
			return s
		}
	}
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	suffix := hex.EncodeToString(buf)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return ""
	}
	if err := os.WriteFile(path, []byte(suffix), 0o644); err != nil {
		return ""
	}
	return suffix
}

// hostname 只允许 [A-Za-z0-9-] 与有限长度 ✓（宿主名/出站名里可能有空格、点、中文 ✓）
func sanitizeHostnameLabel(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "node"
	}
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

func (o EasyTierOption) structuredConfig() easytier.Config {
	instanceName := o.InstanceName
	if instanceName == "" {
		instanceName = o.Name
	}
	// FORK(easytier-identity): hostname 未设置时必须兜底，且兜底值要**同时**满足两个唯一性 ✗✓：
	//   · **每台机器唯一** ✓ —— 否则同一份订阅被多台机器导入后全都同名 ✓（跨机冲突，比不兜底更糟 ✗）；
	//   · **同机每个出站唯一** ✓ —— 否则同一进程里的两个出站会被 overlay 当成同一个节点 ✗，
	//     表现为"同一时刻只有一个能用"，且谁活取决于注册顺序（实测：重新激活后翻转 ✓）。
	// 所以取二者组合：<宿主主机名>-<出站名> ✓。取值仅在 hostname 未显式配置时生效 ✓。
	hostname := o.Hostname
	if hostname == "" {
		// 兜底里的随机后缀要落在**同一个 state 目录**里 ✓；目录不安全时退化为不带后缀 ✓
		// （名字问题绝不能让出站起不来 ✓，安全性则由同一个 helper 的校验保证 ✓）。
		if dir, ok := safeEasyTierStateDir(o); ok {
			hostname = defaultEasyTierHostname(o.Name, dir)
		} else {
			hostname = defaultEasyTierHostname(o.Name, "")
		}
	}
	return easytier.Config{
		NetworkName:         o.NetworkName,
		NetworkSecret:       o.NetworkSecret,
		Hostname:            hostname,
		IPv4:                o.IPv4,
		DHCP:                o.DHCP,
		Peers:               o.Peers,
		Listeners:           o.Listeners,
		NoListener:          o.NoListener,
		MappedListeners:     o.MappedListeners,
		ExitNodes:           o.ExitNodes,
		STUNServers:         o.STUNServers,
		STUNServersV6:       o.STUNServersV6,
		STUNServersV6Set:    o.STUNServersV6 != nil, // FORK(easytier-stun): 区分"没配"与"配成空" ✓
		ProxyNetworks:       o.ProxyNetworks,
		InstanceName:        instanceName,
		AcceptDNS:           o.AcceptDNS,
		EnableExitNode:      o.EnableExitNode,
		EnableEncryption:    o.EnableEncryption,
		EncryptionAlgorithm: o.EncryptionAlgorithm,
		PrivateMode:         o.PrivateMode,
		LatencyFirst:        o.LatencyFirst,
		DisableP2P:          o.DisableP2P,
		EnableKCPProxy:      o.EnableKCPProxy,
		DisableKCPInput:     o.DisableKCPInput,
		EnableQUICProxy:     o.EnableQUICProxy,
		DisableQUICInput:    o.DisableQUICInput,
		MTU:                 o.MTU,
		TLDDNSZone:          o.TLDDNSZone,
		SecureMode:          o.SecureMode,
		LocalPrivateKey:     o.LocalPrivateKey,
		LocalPublicKey:      o.LocalPublicKey,
	}
}

func NewEasyTier(option EasyTierOption) (*EasyTier, error) {
	configTOML, err := option.structuredConfig().RenderTOML()
	if err != nil {
		return nil, err
	}
	configTOML = easytier.ApplyRequiredFlags(configTOML)
	// FORK(easytier-tun): TUN 模式改为 no_tun=false，并在构造期校验参数（配置错误快速失败）
	var (
		tunPrefix netip.Prefix
		tunRoutes []netip.Prefix
	)
	if option.Tun {
		configTOML = easytier.ApplyTunFlags(configTOML)
		if tunPrefix, err = easytier.TunPrefix(option.IPv4); err != nil {
			return nil, err
		}
		if tunRoutes, err = easytier.TunRoutes(option.TunRoutes); err != nil {
			return nil, err
		}
	}

	stateDir, safe := safeEasyTierStateDir(option)
	if !safe {
		return nil, C.Path.ErrNotSafePath(stateDir)
	}

	addr := option.NetworkName
	if addr == "" {
		addr = "easytier"
	}
	ctx, cancel := context.WithCancel(context.Background())
	outbound := &EasyTier{
		Base: NewBase(BaseOption{
			Name:         option.Name,
			Addr:         addr,
			Type:         C.EasyTier,
			ProviderName: option.ProviderName,
			UDP:          option.UDP,
			Interface:    option.Interface,
			RoutingMark:  option.RoutingMark,
			Prefer:       option.IPVersion,
		}),
		option:     option,
		configTOML: configTOML,
		stateDir:   stateDir,
		tunPrefix:  tunPrefix,
		tunRoutes:  tunRoutes,
		zone:       easytier.NormalizeZone(option.TLDDNSZone),
		ctx:        ctx,
		cancel:     cancel,
	}
	outbound.dialer = option.NewDialer(outbound.DialOptions())
	outbound.unregister = dns.RegisterEasyTierDnsClient(option.Name, easyTierDNSTransport{easytier: outbound})
	if option.Prewarm {
		// FORK(easytier-prewarm): 只调一次；阻塞在这个 goroutine 里等就绪，失败/中断都由上游 loop() 处理
		go func() {
			if err := outbound.ensureStarted(context.Background()); err != nil {
				log.Warnln("[EasyTier](%s) prewarm: %v", option.Name, err)
			}
		}()
	}
	return outbound, nil
}

func (e *EasyTier) ensureStarted(ctx context.Context) error {
	e.loopOnce.Do(func() {
		go e.loop()
	})
	for {
		if err := e.ctx.Err(); err != nil {
			return errEasyTierClosed
		}
		e.startMu.Lock()
		closed := e.closed
		readyCh := e.readyCh
		e.startMu.Unlock()
		if closed {
			return errEasyTierClosed
		}
		e.mu.Lock()
		instance := e.instance
		e.mu.Unlock()
		if instance != nil && instance.State() == corehost.StateRunning {
			return nil
		}
		if readyCh == nil {
			e.startMu.Lock()
			if e.readyCh == nil {
				e.readyCh = make(chan struct{})
			}
			readyCh = e.readyCh
			e.startMu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.ctx.Done():
			return errEasyTierClosed
		case <-readyCh:
		}
	}
}

func (e *EasyTier) signalReady() {
	e.startMu.Lock()
	if e.readyCh != nil {
		close(e.readyCh)
		e.readyCh = nil
	}
	e.startMu.Unlock()
}

func (e *EasyTier) loop() {
	backoff := easyTierMinBackoff
	for {
		if e.ctx.Err() != nil {
			return
		}
		e.startMu.Lock()
		closed := e.closed
		e.startMu.Unlock()
		if closed {
			return
		}
		// FORK(easytier-resilience): 兜底——init 里任何 panic 都不能带走整个内核进程
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					log.Errorln("[EasyTier](%s) init panicked: %v", e.Name(), r)
					err = fmt.Errorf("easytier: init panicked: %v", r)
				}
			}()
			return e.init()
		}()
		if err != nil {
			log.Warnln("[EasyTier](%s) start failed: %v; retry in %s", e.Name(), err, backoff)
			_ = e.shutdown()
			timer := time.NewTimer(backoff)
			select {
			case <-e.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < easyTierMaxBackoff {
				backoff *= 2
				if backoff > easyTierMaxBackoff {
					backoff = easyTierMaxBackoff
				}
			}
			continue
		}
		backoff = easyTierMinBackoff
		e.signalReady()
		reason := e.serve()
		_ = e.shutdown()
		if e.ctx.Err() != nil {
			return
		}
		e.startMu.Lock()
		closed = e.closed
		e.startMu.Unlock()
		if closed {
			return
		}
		if reason == "" {
			reason = "instance stopped"
		}
		log.Warnln("[EasyTier](%s) %s; restarting in %s", e.Name(), reason, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < easyTierMaxBackoff {
			backoff *= 2
			if backoff > easyTierMaxBackoff {
				backoff = easyTierMaxBackoff
			}
		}
	}
}

func (e *EasyTier) init() error {
	if err := os.MkdirAll(e.stateDir, 0o755); err != nil {
		return fmt.Errorf("easytier: create state-dir: %w", err)
	}
	instanceID := loadInstanceID(e.stateDir)
	instanceName := e.option.InstanceName
	if instanceName == "" {
		instanceName = e.option.Name
	}
	// FORK(easytier-guestlog): hand the guest's stdout/stderr to mihomo's log. Without it the
	// WASI module's tracing output goes to io.Discard and an embedded instance is a black box.
	guestLog := easytier.NewGuestLogWriter(instanceName)
	host, err := corehost.New(e.ctx, corehost.Options{
		Platform:  easytier.Services(e.dialer),
		LogWriter: guestLog,
	})
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.host = host
	e.guestLog = guestLog
	e.mu.Unlock()

	instance, err := host.CreateInstanceTOML(e.ctx, instanceName, instanceID, e.configTOML)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.instance = instance
	e.instanceID = instance.ID()
	e.mu.Unlock()
	if err := writeInstanceID(e.stateDir, instance.ID()); err != nil {
		return err
	}
	if err := instance.Start(e.ctx); err != nil {
		return err
	}
	log.Infoln("[EasyTier](%s) instance %s running", e.Name(), instance.ID())
	// FORK(easytier-tun): 节点自述一次（端口/公告信息），用于回答"某个被拨的端口该由谁监听"。
	go func(diagCtx context.Context, diagInstance *corehost.Instance) {
		time.Sleep(20 * time.Second)
		if diagInstance == nil {
			return
		}
		if info, err := diagInstance.ShowNodeInfo(diagCtx); err != nil {
			log.Debugln("[EasyTier](%s) show node info failed: %v", e.Name(), err)
		} else {
			log.Debugln("[EasyTier](%s) node info: %+v", e.Name(), info)
		}
	}(e.ctx, instance)
	// FORK(easytier-tun): 接上宿主 TUN 设备与实例包面（需要 root / CAP_NET_ADMIN）
	if e.option.Tun {
		bridge, err := easytier.StartTunBridge(e.ctx, easytier.TunDeviceOptions{
			Prefix: e.tunPrefix,
			MTU:    e.option.MTU,
			Routes: e.tunRoutes,
		}, instance)
		if err != nil {
			return err
		}
		e.mu.Lock()
		e.tunBridge = bridge
		e.mu.Unlock()
		log.Infoln("[EasyTier](%s) tun mode enabled on %s", e.Name(), e.tunPrefix)
	}
	return nil
}

func (e *EasyTier) currentInstance() (*corehost.Instance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.instance == nil {
		return nil, errors.New("easytier instance is not ready")
	}
	return e.instance, nil
}

func (e *EasyTier) serve() string {
	e.mu.Lock()
	instance := e.instance
	e.mu.Unlock()
	if instance == nil {
		return "instance is not ready"
	}
	// Manual connectors retry inside core (reconnect_interval, default 1s).
	// Events() is best-effort: a full host queue drops the event and does not
	// stall the guest. Drain it for logs; recreate only when the stream closes.
	events := instance.Events()
	if events == nil {
		if err := instance.Wait(e.ctx); err != nil && e.ctx.Err() == nil {
			return err.Error()
		}
		return "instance stopped"
	}
	for {
		select {
		case <-e.ctx.Done():
			return ""
		case event, ok := <-events:
			if !ok {
				return "instance stopped"
			}
			switch event.Kind {
			case "peer_added", "peer_removed":
				log.Infoln("[EasyTier](%s) %s: %s", e.Name(), event.Kind, event.Message)
			default:
				log.Debugln("[EasyTier](%s) %s: %s", e.Name(), event.Kind, event.Message)
			}
		}
	}
}

func (e *EasyTier) overlayNodes(ctx context.Context) ([]easytier.Node, error) {
	instance, err := e.currentInstance()
	if err != nil {
		return nil, err
	}
	var nodes []easytier.Node
	info, err := instance.ShowNodeInfo(ctx)
	if err == nil && info != nil {
		node := easytier.Node{Hostname: info.GetHostname()}
		if ip, parseErr := easytier.ParseNodeIPv4(info.GetIpv4Addr()); parseErr == nil {
			node.IPv4 = ip
		}
		if node.Hostname != "" || node.IPv4.IsValid() {
			nodes = append(nodes, node)
		}
	}
	routes, err := instance.ListRoute(ctx)
	if err != nil {
		if len(nodes) == 0 {
			return nil, err
		}
		return nodes, nil
	}
	for _, route := range routes {
		if route == nil {
			continue
		}
		inet := route.GetIpv4Addr()
		if inet == nil || inet.GetAddress() == nil {
			continue
		}
		nodes = append(nodes, easytier.Node{
			Hostname: route.GetHostname(),
			IPv4:     easytier.IPv4FromUint32(inet.GetAddress().GetAddr()),
		})
	}
	return nodes, nil
}

func (e *EasyTier) resolveIPv4(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !ip.Is4() {
			return netip.Addr{}, fmt.Errorf("easytier: overlay dial supports IPv4 only")
		}
		return ip, nil
	}
	nodes, err := e.overlayNodes(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	if ip, ok := easytier.LookupOverlayHost(host, e.zone, nodes); ok {
		return ip, nil
	}
	if easytier.IsMagicDNS(host, e.zone) {
		return netip.Addr{}, fmt.Errorf("easytier: overlay hostname %q was not found", host)
	}
	// FORK(easytier-dns): resolve the target with the kernel's DNS, not with the
	// proxy-server resolver. This host is a dial target, not a proxy server address, and
	// resolving it through ProxyServerHostResolver bypassed dns.nameserver-policy - so a
	// domain routed into the overlay could not be resolved by a nameserver reachable only
	// through that same overlay.
	hostResolver := resolver.DefaultResolver
	if hostResolver == nil {
		hostResolver = resolver.ProxyServerHostResolver
	}
	ips, err := resolver.LookupIPv4WithResolver(ctx, host, hostResolver)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("easytier: resolve %q: no IPv4 address", host)
	}
	return ips[0], nil
}

func (e *EasyTier) DialContext(ctx context.Context, metadata *C.Metadata) (_ C.Conn, err error) {
	if err = e.ensureStarted(ctx); err != nil {
		return nil, err
	}
	host := metadata.Host
	if host == "" && metadata.DstIP.IsValid() {
		host = metadata.DstIP.String()
	}
	ip, err := e.resolveIPv4(ctx, host)
	if err != nil {
		return nil, err
	}
	instance, err := e.currentInstance()
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", metadata.DstPort))
	conn, err := instance.Dial(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, errors.New("conn is nil")
	}
	return NewConn(conn, e), nil
}

func (e *EasyTier) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (_ C.PacketConn, err error) {
	if err = e.ensureStarted(ctx); err != nil {
		return nil, err
	}
	if err = e.ResolveUDP(ctx, metadata); err != nil {
		return nil, err
	}
	instance, err := e.currentInstance()
	if err != nil {
		return nil, err
	}
	pc, err := instance.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	if pc == nil {
		return nil, errors.New("packetConn is nil")
	}
	return NewPacketConn(pc, e), nil
}

func (e *EasyTier) ResolveUDP(ctx context.Context, metadata *C.Metadata) error {
	if metadata.Host != "" {
		ip, err := e.resolveIPv4(ctx, metadata.Host)
		if err != nil {
			return fmt.Errorf("can't resolve ip: %w", err)
		}
		metadata.DstIP = ip
		return nil
	}
	if metadata.DstIP.IsValid() && !metadata.DstIP.Is4() {
		return fmt.Errorf("easytier: overlay dial supports IPv4 only")
	}
	return nil
}

func (e *EasyTier) ProxyInfo() C.ProxyInfo {
	info := e.Base.ProxyInfo()
	info.DialerProxy = e.option.DialerProxy
	return info
}

func (e *EasyTier) IsL3Protocol(*C.Metadata) bool {
	return true
}

func (e *EasyTier) Close() error {
	e.cancel()
	if e.unregister != nil {
		e.unregister()
	}
	e.startMu.Lock()
	e.closed = true
	if e.readyCh != nil {
		close(e.readyCh)
		e.readyCh = nil
	}
	e.startMu.Unlock()
	return e.shutdown()
}

func (e *EasyTier) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), C.DefaultTCPTimeout)
	defer cancel()
	e.mu.Lock()
	instance := e.instance
	host := e.host
	guestLog := e.guestLog
	e.instance = nil
	e.host = nil
	e.guestLog = nil
	e.mu.Unlock()
	e.mu.Lock()
	tunBridge := e.tunBridge
	e.tunBridge = nil
	e.mu.Unlock()
	// FORK(easytier-tun): 先停包面搬运并关设备，再关实例
	if tunBridge != nil {
		_ = tunBridge.Close()
	}
	var err error
	if instance != nil {
		err = instance.Close(ctx)
	}
	if host != nil {
		if hostErr := host.Close(ctx); err == nil {
			err = hostErr
		}
	}
	if guestLog != nil {
		// FORK(easytier-guestlog): 冲刷最后一行（半行也要留证）
		_ = guestLog.Close()
	}
	return err
}

type easyTierDNSTransport struct {
	easytier *EasyTier
}

func (t easyTierDNSTransport) Address() string {
	return "easytier://" + t.easytier.Name()
}

func (t easyTierDNSTransport) ResetConnection() {}

func (t easyTierDNSTransport) ExchangeContext(ctx context.Context, msg *D.Msg) (*D.Msg, error) {
	if len(msg.Question) == 0 {
		return nil, errors.New("should have one question at least")
	}
	if err := t.easytier.ensureStarted(ctx); err != nil {
		return nil, err
	}
	q := msg.Question[0]
	nodes, err := t.easytier.overlayNodes(ctx)
	if err != nil {
		return nil, err
	}
	reply := new(D.Msg)
	reply.SetReply(msg)
	reply.Authoritative = true
	reply.RecursionAvailable = true
	switch q.Qtype {
	case D.TypeA:
		ip, ok := easytier.LookupOverlayHost(q.Name, t.easytier.zone, nodes)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply, nil
		}
		reply.Answer = append(reply.Answer, &D.A{
			Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: easyTierDNSTTL},
			A:   ip.AsSlice(),
		})
	case D.TypePTR:
		ip, ok := easytier.ParsePTRIPv4(q.Name)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply, nil
		}
		name, ok := easytier.LookupOverlayPTR(ip, t.easytier.zone, nodes)
		if !ok {
			reply.Rcode = D.RcodeNameError
			return reply, nil
		}
		reply.Answer = append(reply.Answer, &D.PTR{
			Hdr: D.RR_Header{Name: q.Name, Rrtype: D.TypePTR, Class: D.ClassINET, Ttl: easyTierDNSTTL},
			Ptr: name,
		})
	default:
		reply.Rcode = D.RcodeSuccess
	}
	return reply, nil
}

func loadInstanceID(stateDir string) string {
	contents, err := os.ReadFile(filepath.Join(stateDir, easyTierInstanceIDFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(contents))
}

func writeInstanceID(stateDir, id string) error {
	if id == "" {
		return nil
	}
	path := filepath.Join(stateDir, easyTierInstanceIDFile)
	return os.WriteFile(path, []byte(id+"\n"), 0o600)
}
