// 运行时下发 proxy-networks。
//
// 背景（2026-10-12 实测 ✓）：`proxy-networks` 写进创建期的 TOML 并**不会**生效 ——
// guest 的 TOML 反序列化路径不调用 `add_proxy_cidr`，只有它的 CLI 与 config-patch RPC
// 那条路径才会（`easytier/src/core.rs` 的 `add_proxy_network_to_config` →
// `peers/route/peer_ospf_route` 的入口）。后果是：本节点公告不出去，对端学不到路由，
// 于是"借某个节点出公网"这件事静默失败。
//
// 这里做一次**对账**：创建期照旧声明（TOML 是权威描述），启动后再用 guest 自己的
// config-patch RPC 应用一次。guest 按 cidr 去重，所以重复应用是幂等的；将来 guest
// 修好 TOML 路径后，这一步退化为无害的 no-op。
//
// FORK(easytier-proxy-networks)
package easytier

import (
	"context"
	"fmt"
	"net/netip"

	apiconfig "github.com/easytier/easytier/easytier-go/proto/api/config"
	"github.com/easytier/easytier/easytier-go/proto/common"
)

// configPatcher 是 EasyTier 实例的运行时配置面；*corehost.Instance 天然满足该接口。
type configPatcher interface {
	PatchConfig(ctx context.Context, patch *apiconfig.InstanceConfigPatch) error
}

// ParseProxyNetworks 把配置里的 CIDR 字符串转成 guest 的 patch 条目。
// 只接受 IPv4（与本 fork 的 overlay 地址族一致），并要求是网段形式（"1.2.3.0/24"）。
func ParseProxyNetworks(networks []string) ([]*apiconfig.ProxyNetworkPatch, error) {
	patches := make([]*apiconfig.ProxyNetworkPatch, 0, len(networks))
	for _, network := range networks {
		prefix, err := netip.ParsePrefix(network)
		if err != nil {
			return nil, fmt.Errorf("easytier: proxy-network %q is not a CIDR: %w", network, err)
		}
		prefix = prefix.Masked()
		if !prefix.Addr().Is4() {
			return nil, fmt.Errorf("easytier: proxy-network %q is not IPv4", network)
		}
		addr := prefix.Addr().As4()
		patches = append(patches, &apiconfig.ProxyNetworkPatch{
			Action: apiconfig.ConfigPatchAction_ADD,
			Cidr: &common.Ipv4Inet{
				Address:       &common.Ipv4Addr{Addr: uint32(addr[0])<<24 | uint32(addr[1])<<16 | uint32(addr[2])<<8 | uint32(addr[3])},
				NetworkLength: uint32(prefix.Bits()),
			},
		})
	}
	return patches, nil
}

// ApplyProxyNetworks 让运行中的实例真正公告这些网段。空列表是 no-op，不产生任何 RPC。
//
// 失败会原样返回 —— 用户显式配了 `proxy-networks` 却静默不生效，正是这个字段以前的病，
// 不能再重复一次。调用方（outbound 的 init）据此让出站启动失败并说明原因。
func ApplyProxyNetworks(ctx context.Context, instance configPatcher, networks []string) error {
	if len(networks) == 0 {
		return nil
	}
	if instance == nil {
		return fmt.Errorf("easytier: apply proxy-networks without an instance")
	}
	patches, err := ParseProxyNetworks(networks)
	if err != nil {
		return err
	}
	if err := instance.PatchConfig(ctx, &apiconfig.InstanceConfigPatch{ProxyNetworks: patches}); err != nil {
		return fmt.Errorf("easytier: apply proxy-networks %v: %w", networks, err)
	}
	return nil
}
