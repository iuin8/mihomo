package easytier

import (
	"strings"
	"testing"
)

// TDD（红）：ApplyTunFlags 是 TUN 模式下的 required flags 变体——
// 与 ApplyRequiredFlags 相反，它强制 no_tun = false，但同样强制 bind_device = false。
// 之所以新增函数而不是改 ApplyRequiredFlags 签名：后者是上游共享文件里的导出函数，
// 改签名会给上游同步制造冲突（fork 约定：共享文件改动越小越好）。

func TestApplyTunFlagsInjectsNoTunFalse(t *testing.T) {
	got := ApplyTunFlags("[network_identity]\nnetwork_name = \"n\"\n")
	if !strings.Contains(got, "[flags]") {
		t.Fatalf("did not add flags section:\n%s", got)
	}
	if !strings.Contains(got, "no_tun = false") {
		t.Fatalf("did not inject no_tun = false:\n%s", got)
	}
	if !strings.Contains(got, "bind_device = false") {
		t.Fatalf("missing bind_device:\n%s", got)
	}
}

func TestApplyTunFlagsReplacesNoTunTrue(t *testing.T) {
	got := ApplyTunFlags("[flags]\nno_tun = true\nmtu = 1200\n")
	if !strings.Contains(got, "no_tun = false") {
		t.Fatalf("did not replace no_tun:\n%s", got)
	}
	if strings.Contains(got, "no_tun = true") {
		t.Fatalf("stale no_tun = true remains:\n%s", got)
	}
	if !strings.Contains(got, "mtu = 1200") {
		t.Fatalf("dropped unrelated flag:\n%s", got)
	}
}

func TestApplyTunFlagsQuotedKey(t *testing.T) {
	got := ApplyTunFlags("[flags]\n\"no_tun\" = true\n")
	if !strings.Contains(got, "no_tun = false") {
		t.Fatalf("did not rewrite quoted key:\n%s", got)
	}
}

func TestApplyTunFlagsSectionComment(t *testing.T) {
	got := ApplyTunFlags("[flags] # tun flags\nno_tun = true\n")
	if !strings.Contains(got, "no_tun = false") {
		t.Fatalf("did not rewrite under commented section:\n%s", got)
	}
}

func TestApplyTunFlagsDoesNotDuplicateBindDevice(t *testing.T) {
	got := ApplyTunFlags("[flags]\nno_tun = true\nbind_device = false\n")
	if strings.Count(got, "bind_device") != 1 {
		t.Fatalf("bind_device duplicated:\n%s", got)
	}
}

// TUN 设备地址必须是合法 IPv4 CIDR；v1 不支持 IPv6。
// 设备地址保留主机位（10.144.0.2/24）——掩码成网络地址会装错接口地址（E2E 实测踩到）。
func TestTunPrefixRequiresIPv4CIDR(t *testing.T) {
	prefix, err := TunPrefix("10.144.0.2/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefix.String() != "10.144.0.2/24" {
		t.Fatalf("device address must keep its host bits, got %s", prefix)
	}

	for _, bad := range []string{"", "10.144.0.2", "fd00::1/64", "not-an-ip"} {
		if _, err := TunPrefix(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

// tun-routes：空列表合法（家侧网关不需要路由）；非法 CIDR / IPv6 必须报错。
func TestTunRoutesValidation(t *testing.T) {
	routes, err := TunRoutes(nil)
	if err != nil {
		t.Fatalf("empty routes should be valid: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("expected no routes, got %v", routes)
	}

	routes, err = TunRoutes([]string{"10.0.0.0/24", "192.168.1.0/24"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(routes) != 2 || routes[0].String() != "10.0.0.0/24" {
		t.Fatalf("unexpected routes: %v", routes)
	}

	for _, bad := range [][]string{{"10.0.0.1"}, {"fd00::/64"}, {"nope"}} {
		if _, err := TunRoutes(bad); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}
