//go:build !no_easytier

package outbound

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/metacubex/mihomo/component/easytier"
	C "github.com/metacubex/mihomo/constant"
)

type EasyTierOption struct {
	BasicOption
	Name          string   `proxy:"name"`
	NetworkName   string   `proxy:"network-name,omitempty"`
	NetworkSecret string   `proxy:"network-secret,omitempty"`
	Hostname      string   `proxy:"hostname,omitempty"`
	IPv4          string   `proxy:"ipv4,omitempty"`
	DHCP          bool     `proxy:"dhcp,omitempty"`
	Peers         []string `proxy:"peers,omitempty"`
	Listeners     []string `proxy:"listeners,omitempty"`
	NoListener    *bool    `proxy:"no-listener,omitempty"`
	// FORK(easytier-logging): guest 内部日志级别（off/error/warn/warning/info/debug/trace）✓
	// 注意：guest 行是经 log.Debugln 转发到宿主日志的 ⇒ **内核 log-level 也必须是 debug**
	// 才看得到它们；只设本选项而内核是 info 时仍然看不到 guest 输出。
	LogLevel        string   `proxy:"log-level,omitempty"`
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
	return easyTierHostname(host, name, persistedHostIDSuffix(stateDir))
}

// easyTierHostname keeps the outbound name visible even when the host name is
// long: truncating the combined label from the right would drop the outbound
// name on hosts with long names (macOS CI does), and then two outbounds would
// look like the same node again.
func easyTierHostname(host, name, suffix string) string {
	hostPart := sanitizeHostnameLabel(host)
	namePart := sanitizeHostnameLabel(name)
	limit := 63
	if suffix != "" {
		limit -= len(suffix) + 1
	}
	if limit < 1 {
		limit = 1
	}
	if len(namePart) > limit {
		namePart = strings.Trim(namePart[:limit], "-")
		if namePart == "" {
			namePart = "node"
		}
	}
	remain := limit - len(namePart) - 1
	var base string
	if remain < 1 {
		base = namePart
	} else {
		if len(hostPart) > remain {
			hostPart = hostPart[:remain]
		}
		hostPart = strings.Trim(hostPart, "-")
		if hostPart == "" {
			base = namePart
		} else {
			base = hostPart + "-" + namePart
		}
	}
	if suffix != "" {
		base += "-" + suffix
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

// parseEasyTierLogLevel maps the node-level string to the guest logger level.
// The accepted words are the tracing filter names the guest understands, plus
// "warning" to match the wording mihomo uses for its own log level. Unknown
// names are rejected here so a typo fails the outbound at construction instead
// of silently logging at the default level or entering the retry loop.
func parseEasyTierLogLevel(name string) (corehost.LogLevel, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return corehost.LogLevelInfo, nil
	case "off":
		return corehost.LogLevelOff, nil
	case "error":
		return corehost.LogLevelError, nil
	case "warn", "warning":
		return corehost.LogLevelWarn, nil
	case "info":
		return corehost.LogLevelInfo, nil
	case "debug":
		return corehost.LogLevelDebug, nil
	case "trace":
		return corehost.LogLevelTrace, nil
	default:
		return corehost.LogLevelInfo, fmt.Errorf("easytier: invalid log-level %q (off|error|warn|warning|info|debug|trace)", name)
	}
}
