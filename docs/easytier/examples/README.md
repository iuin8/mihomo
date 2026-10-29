# EasyTier 示例（脱敏）

本目录按角色分成三个文件夹；每个角色都有一份**完整、脱敏、可独立阅读**的示例配置。
这里只放假值/占位符，**不放真实 secret、真实内网域名或真实公网 IP**。

| 目录 | 角色 | 完整示例 |
|---|---|---|
| [`client/`](client/) | 客户端：接入内网 | `complete.template.yaml` / `complete.example.yaml` |
| [`exit/`](exit/) | 出口：把家侧的包用自己的网络送出去 | `complete.example.yaml` |
| [`home/`](home/) | 家侧网关：公告内网 + 可选接管宿主 | `gateway.example.yaml` / `docker-compose.example.yml` |

## 共同前提

- 内核必须是**本 fork 的 mihomo**：`type: easytier` 和 `tun: true` 是 fork 能力，上游会静默忽略；
- `NET_SECRET` 是 overlay 入网资格，不能公开；对外分享请另建 overlay + secret；
- TUN 需要 root / `CAP_NET_ADMIN`；CVR 服务模式会处理；
- 示例里的 `10.200.0.0/24`、`example-overlay`、`home-gateway`、`en0` 等都是假值。

## 校验

```bash
sh docs/easytier/check-templates.sh        # 不得有真实 secret/公网 IP/真实目标
mihomo -t -d /tmp/mihomo-validate -f docs/easytier/examples/client/complete.example.yaml
mihomo -t -d /tmp/mihomo-validate -f docs/easytier/examples/exit/complete.example.yaml
mihomo -t -d /tmp/mihomo-validate -f docs/easytier/examples/home/gateway.example.yaml
```

## 可直接用的私密完整配置

仓库只放脱敏示例。需要给**可信人**的、带真实 overlay 参数的完整配置放 **secret gist**，
不要复制进 repo，也不要改成公开 gist；分享链接外泄时轮换 overlay secret。
