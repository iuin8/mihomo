// FORK(easytier-tun): TUN 模式的 TOML flags 变换与地址解析。
//
// 上游只支持 no-TUN（ApplyRequiredFlags 强制 no_tun = true，见 toml.go）。
// 本文件不修改 toml.go：ApplyTunFlags 复用上游的规范化算法（创建 [flags] 段、
// 覆盖 no_tun/bind_device、处理带引号键与段注释），只把 no_tun 的取值翻过来。
//
// 规格与验收标准见 docs/easytier_tun_spec.md。
package easytier

import (
	"fmt"
	"net/netip"
	"strings"
)

// ApplyTunFlags 与 ApplyRequiredFlags 相反：强制 no_tun = false（宿主代管 TUN 设备），
// bind_device = false 保持不变。输入无 [flags] 段时会补一个。
func ApplyTunFlags(configTOML string) string {
	// ApplyRequiredFlags 会把 no_tun 规范成唯一一行 `no_tun = true`，
	// 因此这里只需翻转该行；其余处理（注入、引号键、段注释）全部沿用。
	return strings.Replace(ApplyRequiredFlags(configTOML), "no_tun = true", "no_tun = false", 1)
}

// TunPrefix 解析 TUN 模式下的设备地址，必须是 IPv4 CIDR（例如 10.144.0.2/24）。
// 注意：设备地址要保留主机位（10.144.0.2/24），掩码成网络地址会装错接口地址。
func TunPrefix(ipv4 string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(ipv4))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("easytier: tun mode requires ipv4 in CIDR form (e.g. 10.144.0.2/24): %w", err)
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("easytier: tun mode supports IPv4 only, got %q", ipv4)
	}
	return prefix, nil
}

// TunRoutes 解析 tun-routes：pin 进这块 TUN 的前缀列表（空列表合法，家侧网关无需路由）。
func TunRoutes(routes []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(routes))
	for _, route := range routes {
		route = strings.TrimSpace(route)
		if route == "" {
			return nil, fmt.Errorf("easytier: tun-routes contains an empty entry")
		}
		prefix, err := netip.ParsePrefix(route)
		if err != nil {
			return nil, fmt.Errorf("easytier: tun-routes entry %q is not a CIDR: %w", route, err)
		}
		if !prefix.Addr().Is4() {
			return nil, fmt.Errorf("easytier: tun-routes supports IPv4 only, got %q", route)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}
