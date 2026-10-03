//go:build !no_easytier

package outbound

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

// 红→绿：hostname 未设置时必须按出站兜底 ✗✓
// 否则**多个 easytier 出站**会共用宿主默认 hostname ✓，在 overlay 里被当成同一个节点 →
// 表现为"同一时刻只有一个出站能用"，且哪个能用取决于注册顺序（实测：重新激活 profile 后反过来 ✓）。
func TestNewEasyTierDefaultsHostnamePerOutbound(t *testing.T) {
	homeDir := t.TempDir()
	C.SetHomeDir(homeDir) // ← state 目录按内核 home 解析 ✓，不是进程 cwd ✗

	build := func(name, hostname, state string) string {
		option := EasyTierOption{
			Name:          name,
			Hostname:      hostname,
			NetworkName:   "n",
			NetworkSecret: "s",
			Peers:         []string{"tcp://192.0.2.1:11010"},
			StateDir:      state,
		}
		proxy, err := NewEasyTier(option)
		if err != nil {
			t.Fatalf("NewEasyTier(%s): %v", name, err)
		}
		return proxy.configTOML
	}
	hostOf := func(toml string) string {
		const marker = `hostname = "`
		i := strings.Index(toml, marker)
		if i < 0 {
			t.Fatalf("配置里没有 hostname:\n%s", toml)
		}
		rest := toml[i+len(marker):]
		return rest[:strings.Index(rest, `"`)]
	}

	a := hostOf(build("et-a", "", "state-a"))
	b := hostOf(build("et-b", "", "state-b"))
	// ① 同机不同出站必须不同 ✓（本次故障的成因 ✓）
	if a == b {
		t.Fatalf("同机两个出站同名: %q", a)
	}
	// ② 跨机必须不同 ✓（默认 state-dir 每机一份 ✓ → 换 state 目录 = 换机器 ✓）
	if !strings.Contains(a, "-et-a-") {
		t.Fatalf("兜底名未含出站名: %q", a)
	}
	if strings.ContainsAny(a, " ._:/") {
		t.Fatalf("兜底名含非法字符: %q", a)
	}
	// ③ 同一个 state（同一台机器 + 同一个出站）重复构造必须**稳定** ✓
	if again := hostOf(build("et-a", "", "state-a")); again != a {
		t.Fatalf("重复构造不稳定: %q → %q", a, again)
	}
	// ④ 后缀已持久化 ✓
	if _, err := os.Stat(filepath.Join(homeDir, "state-a", "host-id")); err != nil {
		t.Fatalf("host-id 未持久化: %v", err)
	}
	// ⑤ 显式配置优先 ✓
	custom := hostOf(build("et-c", "custom-host", "state-c"))
	if custom != "custom-host" {
		t.Fatalf("显式 hostname 被覆盖: %q", custom)
	}
}
