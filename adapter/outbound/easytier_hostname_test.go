//go:build !no_easytier

package outbound

import (
	"strings"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

// 红→绿：hostname 未设置时必须按出站兜底 ✗✓
// 否则**多个 easytier 出站**会共用宿主默认 hostname ✓，在 overlay 里被当成同一个节点 →
// 表现为"同一时刻只有一个出站能用"，且哪个能用取决于注册顺序（实测：重新激活 profile 后反过来 ✓）。
func TestNewEasyTierDefaultsHostnamePerOutbound(t *testing.T) {
	C.SetHomeDir(t.TempDir())

	build := func(name, hostname string) string {
		option := EasyTierOption{
			Name:          name,
			Hostname:      hostname,
			NetworkName:   "n",
			NetworkSecret: "s",
			Peers:         []string{"tcp://192.0.2.1:11010"},
			StateDir:      "et-state-" + name,
		}
		proxy, err := NewEasyTier(option)
		if err != nil {
			t.Fatalf("NewEasyTier(%s): %v", name, err)
		}
		return proxy.configTOML
	}

	a, b := build("et-a", ""), build("et-b", "")
	if !strings.Contains(a, `hostname = "et-a"`) {
		t.Fatalf("未兜底为出站名，配置为:\n%s", a)
	}
	if !strings.Contains(b, `hostname = "et-b"`) {
		t.Fatalf("未兜底为出站名，配置为:\n%s", b)
	}
	if c := build("et-c", "custom-host"); !strings.Contains(c, `hostname = "custom-host"`) {
		t.Fatalf("显式 hostname 被覆盖，配置为:\n%s", c)
	}
}
