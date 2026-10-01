//go:build !no_easytier

package outbound

import (
	"strings"
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

// TDD（红）：TUN 模式在出站构造阶段就要快速失败（配置错误不要拖到运行时），
// 并且要把 no_tun 渲染成 false。规格见 docs/easytier_tun_spec.md 的 AC2/AC3。
func TestNewEasyTierTunRequiresIPv4CIDR(t *testing.T) {
	C.SetHomeDir(t.TempDir())

	option := EasyTierOption{
		Name:          "et-tun",
		NetworkName:   "n",
		NetworkSecret: "s",
		Peers:         []string{"tcp://192.0.2.1:11010"},
		StateDir:      "et-state",
		Tun:           true,
	}

	if _, err := NewEasyTier(option); err == nil {
		t.Fatal("expected an error when tun is enabled without ipv4")
	}

	option.IPv4 = "10.144.0.2" // 缺前缀
	if _, err := NewEasyTier(option); err == nil {
		t.Fatal("expected an error when ipv4 has no prefix in tun mode")
	}

	option.IPv4 = "10.144.0.2/24"
	proxy, err := NewEasyTier(option)
	if err != nil {
		t.Fatalf("valid tun option rejected: %v", err)
	}
	if !strings.Contains(proxy.configTOML, "no_tun = false") {
		t.Fatalf("tun mode must render no_tun = false:\n%s", proxy.configTOML)
	}
	if strings.Contains(proxy.configTOML, "no_tun = true") {
		t.Fatalf("tun mode must not render no_tun = true:\n%s", proxy.configTOML)
	}

	option.TunRoutes = []string{"10.0.0.1"} // 非法 CIDR
	if _, err := NewEasyTier(option); err == nil {
		t.Fatal("expected an error for a non-CIDR tun-routes entry")
	}
}

// 回归红线：tun 默认关闭时行为必须与上游一致（no_tun = true）。
func TestNewEasyTierWithoutTunKeepsNoTunTrue(t *testing.T) {
	C.SetHomeDir(t.TempDir())

	proxy, err := NewEasyTier(EasyTierOption{
		Name:          "et-notun",
		NetworkName:   "n",
		NetworkSecret: "s",
		Peers:         []string{"tcp://192.0.2.1:11010"},
		StateDir:      "et-state-notun",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(proxy.configTOML, "no_tun = true") {
		t.Fatalf("default mode must keep no_tun = true:\n%s", proxy.configTOML)
	}
}
