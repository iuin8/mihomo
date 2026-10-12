package easytier

import (
	"context"
	"testing"

	apiconfig "github.com/easytier/easytier/easytier-go/proto/api/config"
)

// 契约：`proxy-networks` 必须**真的**下发到运行中的实例。只写创建期 TOML 是不够的
// （guest 的反序列化路径不调用 add_proxy_cidr），所以这里钉住两件事：
// 空配置不产生任何 RPC；配了就必须调用 PatchConfig，且解析拒绝非法输入。
func TestApplyProxyNetworks(t *testing.T) {
	ctx := context.Background()

	// 记录是否被调用，以及被调用了几次。
	var calls int
	var lastPatch *apiconfig.InstanceConfigPatch

	// 空列表 ⇒ 不调 RPC（行为与没有这个功能时完全一致）。
	if err := ApplyProxyNetworks(ctx, recorder{&calls, &lastPatch}, nil); err != nil {
		t.Fatalf("empty list must be a no-op, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("empty list must not call the guest, got %d calls", calls)
	}

	// 非法 CIDR ⇒ 报错，且不调 RPC。
	if err := ApplyProxyNetworks(ctx, recorder{&calls, &lastPatch}, []string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid cidr must be reported")
	}
	if calls != 0 {
		t.Fatalf("invalid cidr must not reach the guest, got %d calls", calls)
	}

	// 合法输入 ⇒ 调一次，且带上正确的 cidr 与 ADD 动作。
	if err := ApplyProxyNetworks(ctx, recorder{&calls, &lastPatch}, []string{"0.0.0.0/0", "192.168.0.0/24"}); err != nil {
		t.Fatalf("valid list must be applied, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("valid list must call the guest exactly once, got %d", calls)
	}
	patches := lastPatch.GetProxyNetworks()
	if len(patches) != 2 {
		t.Fatalf("want 2 proxy network patches, got %d", len(patches))
	}
	if got := patches[0].GetCidr(); got.GetNetworkLength() != 0 || got.GetAddress().GetAddr() != 0 {
		t.Fatalf("0.0.0.0/0 encoded wrong: %+v", got)
	}
	// 192.168.0.0/24 ⇒ 0xC0A80000
	if got := patches[1].GetCidr(); got.GetNetworkLength() != 24 || got.GetAddress().GetAddr() != 0xC0A80000 {
		t.Fatalf("192.168.0.0/24 encoded wrong: len=%d addr=%#x", got.GetNetworkLength(), got.GetAddress().GetAddr())
	}
	for _, patch := range patches {
		if patch.GetAction() != apiconfig.ConfigPatchAction_ADD {
			t.Fatalf("want ADD action, got %v", patch.GetAction())
		}
	}
}

type recorder struct {
	calls *int
	last  **apiconfig.InstanceConfigPatch
}

func (r recorder) PatchConfig(_ context.Context, patch *apiconfig.InstanceConfigPatch) error {
	*r.calls++
	*r.last = patch
	return nil
}
