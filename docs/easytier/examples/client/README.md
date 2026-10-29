# 客户端（client）：接入内网

**它是什么**：一台离网机器上的 mihomo，通过 EasyTier 入口节点把内网目标送进 overlay；
分组在现场选 `DIRECT`、离网选入口节点。终端与浏览器都靠 TUN 自动生效。

| 文件 | 用途 |
|---|---|
| [`complete.template.yaml`](complete.template.yaml) | **完整独立配置模板**：mixed-port / TUN / DNS / proxy / group / rules |
| [`complete.example.yaml`](complete.example.yaml) | 完整独立配置的**全脱敏示例**（假值，结构可直接参考） |
| [`render.sh`](render.sh) | 用环境变量渲染模板；缺任一变量拒绝输出，渲染后做 YAML 自检 |

## 使用

```bash
# 直接在模板上改值，或一键渲染（示例值都是假的）
LAN_NODE=et-home NET_NAME=example-overlay NET_SECRET=CHANGE_ME \
RENDEZVOUS=10.200.0.10 EXIT_ADDR=10.200.0.2 PHYS_NIC=en0 GROUP=内网入口 \
OVERLAY=10.200.0.0/24 HOME_CIDR=192.168.1.0/24 CORP_CIDR=10.0.0.0/8 \
CORP_DOMAIN=corp.example.com CLUSTER_DOMAIN=cluster.local DNS=10.96.0.10 \
sh render.sh > my-profile.yaml
```

- `PHYS_NIC`：持有本机 LAN 地址、设备名不是 `Meta/tun/utun/easytier*` 的物理网卡；
- `NET_SECRET`：overlay 入网资格，不能公开；
- 本 fork 内核才可以识别 `type: easytier` 和 `tun: true`；
- 若已有订阅，不必把整份配置覆盖过去，把本文件的内网段合并进现有 profile 即可。

## 验收

```bash
et-net-mode check easytier        # 若装了 workspace 工具
# 期望：utun1024 在；内网 403；pdd 200；gitlab 302
```

## 安全

真实 `NET_SECRET` 只放本机/secret gist；不要提交到 repo，不要贴到 Issue/日志/截图。
对外分享请另建 overlay + secret；模板本身不含任何真实值。
