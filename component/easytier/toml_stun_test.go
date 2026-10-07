// FORK(easytier-stun): 内嵌核的 STUN 引导必须可控 ✗✓
//
// 实测（2026-10-07）：guest 默认会去解析 `stun-v6.easytier.cn` ✓，拿到**空 TXT** ✓ 后死循环重试 ✓，
// **永远走不到打洞** ✗ → 节点只维持到会合点的 TCP ✓，"看到一个节点都没有"的直觉其实是对的 ✓。
// 症状：家侧经该节点访问其他节点 → 504 / `network is unreachable` ✓。
package easytier

import (
	"strings"
	"testing"
)

func TestRenderTOMLDisablesV6STUNByDefault(t *testing.T) {
	toml, err := Config{NetworkName: "example", Peers: []string{"tcp://127.0.0.1:11010"}}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, "stun_servers_v6 = []") {
		t.Fatalf("v6 STUN 未被默认关闭 ✗（这是内嵌核卡住的根因）:\n%s", toml)
	}
	if strings.Contains(toml, "stun_servers = [") {
		t.Fatalf("V4 STUN 不该在未配置时被写出:\n%s", toml)
	}
}

func TestRenderTOMLHonoursExplicitSTUNLists(t *testing.T) {
	toml, err := Config{
		NetworkName:      "example",
		Peers:            []string{"tcp://127.0.0.1:11010"},
		STUNServers:      []string{"stun.example.cn:3478"},
		STUNServersV6:    []string{"stun-v6.example.cn:3478"},
		STUNServersV6Set: true,
	}.RenderTOML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`stun_servers = ["stun.example.cn:3478"]`, `stun_servers_v6 = ["stun-v6.example.cn:3478"]`} {
		if !strings.Contains(toml, want) {
			t.Fatalf("缺少 %s：\n%s", want, toml)
		}
	}
}
