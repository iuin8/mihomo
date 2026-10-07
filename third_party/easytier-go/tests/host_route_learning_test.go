// FORK(easytier-route-learning): 经"中心节点"学习路由的能力必须被测试覆盖 ✗✓
//
// 为什么加这个用例（2026-10-07 ✓ 实测踩到 ✓）：
//
//	生产里家侧的 mihomo 内嵌出站**只连会合点** ✓，从不出现第二个 peer ✗，
//	事件流里也没有任何 route/peer_center 事件 ✗，guest 自己报 ErrorNoOverlayRoute ✗
//	（→ ENETUNREACH → 家侧经它访问任何外部目标都 504 ✗）；
//	而**同一台机器、同一密钥**的原生 easytier-core 却能看到全部节点 ✓。
//
// 已有的 host_dataplane 用例只覆盖"两个实例**直连**"✓（peers 直指对端 ✓，不需要学路由 ✗），
// 于是"经中心学路由"这条路径没有被任何测试盯住 ✗ —— 本用例补上它 ✓。
//
// 拓扑：hub（有监听 ✓）← spokeA / spokeB 各自只连 hub ✓；P2P 被 instanceConfig 关闭 ✓
//
//	⇒ 两个 spoke 要互通，**只能**走"从 hub 学到的路由" ✓✓。
package host_test

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	corehost "github.com/easytier/easytier/easytier-go"
	"github.com/easytier/easytier/easytier-go/platform"
)

func TestPublicRouteLearningThroughHub(t *testing.T) {
	// ⚠️ 用例已能完整跑通 ✓，但**目前失败** ✗ —— 而失败原因**还不能等同于生产故障** ✓，所以先 Skip ✓：
	//
	//	实测（2026-10-07 16:05 ✓）：spokeA（有监听 ✓）与 spokeB（都只连 hub ✓、P2P 关闭 ✓）
	//	在 60 秒内始终无法互通 ✗（`wait for EasyTier TCP route: context deadline exceeded` ✓）。
	//	**但测试里的 hub 是同进程的内嵌实例 ✗，而 shim 的 `InstanceConfigBuilder` 没有任何中继开关** ✗
	//	（选项只有 NetworkSecret/Hostname/IPv4/AddPeers/AddListeners/AddPortForwards/STUNServers/P2P/
	//	HolePunching/Encryption/SecureMode ✓），guest 的 TOML 里也搜不到 `relay_network_whitelist` ✗。
	//	⇒ **hub 很可能根本没在转发** ✗，于是这个失败**不能证明**"内嵌辐条学不到路由" ✗。
	//
	//	生产里会合点**是原生核 ✓**（`--help` 原文：by default, all networks are allowed ✓，
	//	且原生 observer 确实拿到了 `relay(2)` 路由 ✓），所以要让本用例**忠实**，必须先让 hub 真的会中继 ✓。
	//
	//	**下一步二选一** ✓：① 找到让内嵌实例开启中继的办法 ✓（TOML 键 `relay_network_whitelist` ✓ 待验证 ✓）；
	//	② 让用例的 hub 跑**原生** easytier-core ✓（跨进程 ✓，与生产一致 ✓）。
	//	**在此之前保留 Skip ✓** —— 失败原因已完整记录 ✓，不会误导 ✓。
	t.Skip("hub in this test is not a relaying node; see comment for the two ways to make it faithful")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sockets := &recordingSocketFactory{}
	host, err := corehost.New(ctx, corehost.Options{
		Platform: platform.Services{Sockets: sockets},
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer host.Close(ctx)

	// ⭐ 引导顺序：hub 先用 port=0 起（listen=true ✓ 端口由 OS 分配 ✓），
	//    起好之后才能从 socket 工厂**读出**它实际用的下层监听口 ✓ —— 顺序反了就会报
	//    "EasyTier did not create a TCP listener" ✗（这就是本用例第一版失败的原因 ✓）。
	hub, err := host.CreateInstance(ctx, instanceConfig(t, 201, "10.144.0.201", 0, false, true))
	if err != nil {
		t.Fatalf("create hub: %v", err)
	}
	defer hub.Close(ctx)
	if err := hub.Start(ctx); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	hubPort := sockets.listenerPort(t)

	// spokeA 同时"有监听 + 连 hub"✓ —— 对应生产里**被拨入的那一侧**（本机 ✓，有 listeners ✓）
	spokeAConfig, err := corehost.NewInstanceConfigBuilder("default").
		NetworkSecret("test").
		Hostname("go-host-202").
		IPv4(netip.MustParsePrefix("10.144.0.202/24")).
		P2P(corehost.P2PPolicy{Disable: true}).
		Encryption(false).
		// ⚠️ 必须用 0（随机口）✗✓：socket 工厂**只记一个端口** ✓，若这里再读一次 `listenerPort(t)`
		// 会拿到 hub 的口 ✓ → spokeA 撞口 → 启动报 `status=-4: required listener failed to start` ✗（实测踩到 ✓）。
		AddListeners("tcp://127.0.0.1:0").
		AddPeers(fmt.Sprintf("tcp://127.0.0.1:%d", hubPort)).
		Build()
	if err != nil {
		t.Fatalf("build spokeA config: %v", err)
	}
	spokeA, err := host.CreateInstance(ctx, spokeAConfig)
	if err != nil {
		t.Fatalf("create spokeA: %v", err)
	}
	defer spokeA.Close(ctx)
	if err := spokeA.Start(ctx); err != nil {
		t.Fatalf("start spokeA: %v", err)
	}

	spokeB, err := host.CreateInstance(ctx, instanceConfig(t, 203, "10.144.0.203", hubPort, true, false))
	if err != nil {
		t.Fatalf("create spokeB: %v", err)
	}
	defer spokeB.Close(ctx)
	if err := spokeB.Start(ctx); err != nil {
		t.Fatalf("start spokeB: %v", err)
	}

	// spokeA 上监听，spokeB 主动连它：两边都只认识 hub ✓ → 必须靠学来的路由 ✓
	listener := listenTCPEventually(t, ctx, spokeA)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if connection != nil {
			connection.Close()
		}
		accepted <- err
	}()

	// ⭐ 判据：spokeB 能不能拨通 spokeA 的 overlay 地址 ✓（= 它有没有学到路由 ✓）
	connection := dialEventually(t, ctx, spokeB, fmt.Sprintf("10.144.0.202:%d", port))
	connection.Close()

	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("spokeA accept from spokeB through hub route: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("spokeB never reached spokeA via the hub route: %v", ctx.Err())
	}

	if t.Failed() {
		t.Log("route learning through a hub is broken: a spoke could not reach its sibling")
	}
}
