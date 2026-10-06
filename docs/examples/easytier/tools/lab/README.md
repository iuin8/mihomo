# 端到端实验台（Docker 隔离网络）

**用途**：在不碰生产的前提下复现「家侧网关 ↔ 移动客户端」的双向访问问题 ✓。
两次把生产网络弄断的教训就是**在生产上直接试** ✗ —— 所有结论必须先在这里跑出来 ✓。

## 拓扑（三段独立网段，全部避开常见内网 ✓）

```
lab-wan 10.99.0.0/24          会合点 ← gw / client 都连它
lab-lan-home 10.99.1.0/24     家侧局域网：gw + home-host(nginx)
lab-lan-client 10.99.2.0/24   客户端所在局域网：client + lan-host(nginx)
```

## 怎么跑

```bash
cd docs/examples/easytier/tools/lab
docker compose up -d
sleep 25
docker exec lab-rendezvous easytier-cli peer      # 期望看到 lab-gw(.2) 与 lab-client(.3) 两个节点 ✓

# ① 客户端 → 家侧局域网（用出口节点 ✓）
docker run --rm --network container:lab-client curlimages/curl:latest \
  -s -m 12 -o /dev/null -w '%{http_code}\n' -x http://127.0.0.1:7891 http://10.99.1.2/
# ② 家侧 → 客户端所在网段（本次要修的方向 ✓）
docker run --rm --network container:lab-gw curlimages/curl:latest \
  -s -m 12 -o /dev/null -w '%{http_code}\n' -x http://127.0.0.1:7890 http://10.99.2.3/
docker compose down -v
```

## 已用它证明的结论（2026-10-06）

| 结论 | 证据（网关日志原文 ✓） |
| --- | --- |
| **规则指向的出站若没有 `exit-nodes`，拨号会落回本地** ✗✓ | `dial et-gateway (match IPCIDR/10.99.2.0/24) → 10.99.2.3:80 **network is unreachable**` |
| **把规则改指向"带 `exit-nodes: [<客户端 overlay 地址>]`"的出站，流量就真的进 overlay** ✓✓ | 同一请求的错误变成 `dial et-to-client (match IPCIDR/10.99.2.0/24) → 10.99.2.3:80 **i/o timeout**` —— 错误类型的变化就是"有没有进 overlay"的判据 ✓ |
| **no-TUN（`no_tun = true`）模式下，内嵌核无法把局域网流量送达** ✗（与 SOP §6.13 的 A/B 结论一致 ✓） | 两个方向都 `i/o timeout` ✓ —— 所以**要测真实能力必须用 TUN** ✓ |

## 限制与下一步

- ⚠️ **本实验台当前是 no-TUN 模式** ✗：macOS 的 Docker 里加 `--device /dev/net/tun` + `cap_add: NET_ADMIN` 常因权限失败 ✓；
  若要复现真实能力 ✓，在 **Linux 宿主**上跑同样这份 compose ✓ 并把两个 mihomo 服务的 `tun: true` 打开 ✓（compose 里已留好注释位置 ✓）。
- 判据口诀 ✓：**"`network is unreachable` = 没进 overlay ✗" / "`i/o timeout` = 进了 overlay 但数据面没送达 ✗"** ✓✓ ——
  这两句能把"路由没配对"与"数据面不通"**分开** ✓（此前一直是靠猜 ✗）。
