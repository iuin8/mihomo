# 本机（local）

**它是什么**：你自己的机器。它有两种角色，按需二选一或同时使用：

1. **客户端**：接入内网 —— 通过 EasyTier 入口节点把内网目标送进 overlay；
2. **出口**：把家侧的包用自己的网络送出去 —— 本机作为落地/出口节点。

| 文件 | 用途 |
|---|---|
| [`client.template.yaml`](client.template.yaml) | 客户端完整配置模板（mixed-port / TUN / DNS / proxy / group / rules） |
| [`client.example.yaml`](client.example.yaml) | 客户端完整脱敏示例（假值，结构可直接参考） |
| [`exit.example.yaml`](exit.example.yaml) | 出口完整脱敏示例（TUN、出口节点、DNS、规则） |
| [`render.sh`](render.sh) | 渲染 `client.template.yaml`；缺任一变量拒绝输出，渲染后做 YAML 自检 |

## 客户端：渲染

```bash
LAN_NODE=et-home NET_NAME=example-overlay NET_SECRET=CHANGE_ME \
RENDEZVOUS=10.200.0.10 EXIT_ADDR=10.200.0.2 PHYS_NIC=en0 GROUP=内网入口 \
OVERLAY=10.200.0.0/24 HOME_CIDR=192.168.1.0/24 CORP_CIDR=10.0.0.0/8 \
CORP_DOMAIN=corp.example.com CLUSTER_DOMAIN=cluster.local DNS=10.96.0.10 \
sh render.sh > my-profile.yaml
```

- `PHYS_NIC`：持有本机 LAN 地址、设备名不是 `Meta/tun/utun/easytier*` 的物理网卡；
- `NET_SECRET`：overlay 入网资格，不能公开；
- 本 fork 内核才可以识别 `type: easytier` 和 `tun: true`。

## 出口

直接用 [`exit.example.yaml`](exit.example.yaml)，替换：

- `network-name` / `network-secret`；
- `ipv4`（本机在 overlay 的唯一地址，带前缀）；
- `proxy-networks`（本机所在网段，不能与家侧/集群/overlay 重叠）；
- `interface-name`（物理网卡）；
- `peers`（会合点）；
- `dialer-proxy`（出口替对端拨号要走的代理组；示例里的 `exit-proxy` 需替换）。

**硬要求**：`dialer-proxy` 必须指向可用代理组，`dns.nameserver-policy` 必须经该代理组；
否则国内通、国外 503/504。

## 验收

```bash
et-net-mode check easytier        # 若装了 workspace 工具
# 期望：utun1024 在；内网 403；pdd 200；gitlab 302
```

出口侧：

```bash
curl -s --unix-socket <内核 socket> http://localhost/proxies/et-fa | grep -o '"dialer-proxy":"[^"]*"'
curl -s -o /dev/null -w '%{http_code}\n' https://www.google.com/       # ⇒ 200
# 日志应出现 forwarded conn purpose=4 ... wrote=... read=... 且 read>0
```

## 安全

真实 secret 只放本机/secret gist；不要提交到 repo，不要贴到 Issue/日志/截图。
对外分享请另建 overlay + secret；模板本身不含任何真实值。
