# EasyTier 内网接入示例（脱敏）

本目录只放**仓库内的脱敏示例**，不要放真实 secret、真实内网域名或真实公网 IP。

| 文件 | 用途 |
|---|---|
| `complete.template.yaml` | 完整独立配置占位符模板；可直接编辑或用 `render.sh` 渲染 |
| `complete.example.yaml` | 完整独立配置脱敏示例；所有值都是假的 |

## 与 `client/` 短模板的区别

- `client/mihomo-profile.template.yaml`：给**已有机场订阅**的人做内网增量；
- 本目录 `complete.*.yaml`：**不依赖机场**的完整配置，包含 `mixed-port`、TUN、DNS、proxies、groups、rules。

## 渲染

```bash
TEMPLATE=docs/easytier/examples/complete.template.yaml \
LAN_NODE=et-home NET_NAME=example-overlay NET_SECRET=CHANGE_ME \
RENDEZVOUS=10.200.0.10 EXIT_ADDR=10.200.0.2 PHYS_NIC=en0 GROUP=内网入口 \
OVERLAY=10.200.0.0/24 HOME_CIDR=192.168.1.0/24 CORP_CIDR=10.0.0.0/8 \
CORP_DOMAIN=corp.example.com CLUSTER_DOMAIN=cluster.local DNS=10.96.0.10 \
sh docs/easytier/client/render.sh > my-profile.yaml
```

## 安全

- `<NET_SECRET>` 是 overlay 入网资格；分享配置 = 分享入网资格。
- 对外分享请另建 overlay + secret，或让对方填自己的 secret。
- 需要一份**可直接使用**的私密完整配置时，放**secret gist**，不要放公开 repo；不要把真实值贴到 Issue/日志/截图。
