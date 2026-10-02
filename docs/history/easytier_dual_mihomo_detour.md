# 历史：双 mihomo + hysteria2 绕行（已废弃）

> **状态：废弃，不要用于生产。** 本文件只保留"为什么当时那样做、为什么放弃、证据在哪"，
> 可执行的配置示例已从仓库删除，需要时用下面的提交引用取回。
> 现行方案见 [easytier_tun_spec.md](../easytier_tun_spec.md) 与
> [easytier_home_gateway.md](../easytier_home_gateway.md)（家侧 mihomo 开 TUN 模式）。

## 当时的背景

上游 mihomo 的 EasyTier 出站强制 `no_tun = true`，节点没有 L3 接口：
作为客户端可用，但**作为被访问端不可用**——WASI↔WASI 的 TCP 通路建立不起来
（插桩证据：失败时家侧宿主 `ConnectTCP Purpose=DataPlane/PortForward` 各 0 次，
家侧日志无任何活动；同拓扑 UDP 可通）。

为了在"家侧只有 mihomo"的约束下把 TCP 送过去，当时的思路是：**既然 UDP 是两端唯一可靠的
通路，就把 TCP 封装进 QUIC/UDP**。家侧 mihomo 起 `hysteria2` 入站，客户端用 `hysteria2`
出站并加 `dialer-proxy: <easytier 出站>`，让 QUIC 的 UDP 包经 overlay 送达家侧。

## 实测结论（为什么放弃）

| 项目 | 结果 |
| --- | --- |
| 小请求（`/version` 级） | ✅ `200`，12–14ms；直连 peer 与经共享节点会合都一样通 |
| 64KB 传输 | ❌ `502` |
| 1MB 传输 | ❌ 卡在 ~122KB / 40s（给 hysteria2 限速 2Mbps 无改善） |
| 负载后的自愈 | ❌ 隧道整体失效且**不自愈**，必须重启内核（与上游 issue #MetaCubeX/mihomo#3214 同类现象） |
| 原始 overlay UDP（小样本 40 包） | ✅ 0% 丢包 |
| 原始 overlay UDP（限速 200 包） | ❌ 客户端侧只记录到 **2** 条命中 → 瓶颈在 WASI 的 UDP 数据面（持续/突发负载下崩塌），既不是 MTU 也不是拥塞控制 |

⇒ 只能做"连通性验证"，不能承载实际工作负载。

## 被什么取代

**家侧 mihomo 开 TUN 模式**（本 fork 新增 `tun: true`）：宿主用 sing-tun 建一块真 TUN，
用 `SendPacket`/`ReceivePacket` 搬运裸 IP 包，家侧因此成为真正的 L3 网关。同一拓扑实测：
TCP 20MB 校验一致（34.5–42.5 MB/s）、UDP 40 包 0% 丢包、`ping` 内网机器 3/3 通。
规格与验收标准见 [easytier_tun_spec.md](../easytier_tun_spec.md)。

## 提交引用（取回历史实现）

| 提交 | 内容 |
| --- | --- |
| `52a7970b` | 首版回家网关文档 + 推荐 native 家侧；含 `docs/examples/easytier-home-gateway/alt-dual-mihomo/{home-mihomo.yaml,client-clash.yaml}` 两个绕行配置 |
| `89809f26` | 追加"为什么会牵扯到协议层"（TUN vs 无 TUN 的数据面差异） |
| `dcb322d9` | E2E 证据补全（当时的文档尚未移除绕行方案） |

取回示例配置：

```bash
git show 52a7970b:docs/examples/easytier-home-gateway/alt-dual-mihomo/home-mihomo.yaml
git show 52a7970b:docs/examples/easytier-home-gateway/alt-dual-mihomo/client-clash.yaml
```
