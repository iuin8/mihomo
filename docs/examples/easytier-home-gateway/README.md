# 示例文件地图（EasyTier 回家网关）

**先读 SOP**：[`../../easytier_home_gateway_sop.md`](../../easytier_home_gateway_sop.md)（照做即可，约 20 分钟）。
原理与实测数据：[`../../easytier_home_gateway.md`](../../easytier_home_gateway.md)；
规格与验收：[`../../easytier_tun_spec.md`](../../easytier_tun_spec.md)。

## 方案 A（推荐）：家侧 mihomo 开 TUN —— 一键启动

```bash
cd <mihomo 仓库>/docs/examples/easytier-home-gateway
# 改 home-mihomo-tun.yaml 里三处「改这里」：network-secret / peers / proxy-networks
docker compose up -d      # 首次自动编译本 fork 内核；NAT 与转发由 entrypoint 自动配好
```

| 文件 | 用途 |
| --- | --- |
| `docker-compose.yml` | **一键盘**：构建内核 + `NET_ADMIN`/`/dev/net/tun` + `ip_forward` + 自动 NAT |
| `Dockerfile` | 多阶段构建：源码编本 fork 内核（含 TUN 模式）+ 预装 iptables |
| `entrypoint.sh` | 启动时幂等加 MASQUERADE，再 exec mihomo（`NAT_INTERFACE` / `SKIP_NAT` 可调） |
| `home-mihomo-tun.yaml` | 家侧配置（`tun: true`，发布 `proxy-networks`） |
| `state/` | 运行时生成：overlay 节点身份，**别删** |

## 会合点（家侧无公网 / 不能端口映射时必需）

| 文件 | 用途 |
| --- | --- |
| `rendezvous/docker-compose.yml` | 放到**有公网 IP 的机器**上 `docker compose up -d`；等价于 `docker run … easytier/easytier:latest`（无参数=共享节点） |

## 本机侧

| 文件 | 用途 |
| --- | --- |
| `client-clash.yaml` | Clash 片段；在 CVR 里用 `prepend-proxies` + `prepend-rules` 并进配置（客户端**不需要换内核**） |

## 方案 B（备选）：家侧零特权 native 容器

| 文件 | 用途 |
| --- | --- |
| `alt-native/docker-compose.yml` + `alt-native/home-easytier-core.toml` | 家侧不便提权时用（无 `NET_ADMIN`、无 TUN、无 iptables）。代价：没有 ICMP，吞吐低一档 |

## 验证工具

| 文件 | 用途 |
| --- | --- |
| `tools/probe.sh` | TCP + UDP 一次跑完 |
| `tools/socks5-udp-probe.py` | SOCKS5 UDP ASSOCIATE 探针（`--relay` 用于 Docker/端口映射场景） |
| `tools/udp-echo-server.py` | UDP 回声目标，放在"内网另一侧" |

## 两条路线对照

| | 方案 A（TUN，推荐） | 方案 B（native 零特权） |
| --- | --- | --- |
| 家侧要求 | `NET_ADMIN` + `/dev/net/tun` + `ip_forward` + MASQUERADE（compose 已自动处理） | 无 |
| 代理子网 TCP | ✅ 20MB @ 34.5–42.5 MB/s | ✅ 20MB @ 8.4 MB/s |
| 代理子网 UDP | ✅ 0% 丢包 | ✅ 0% 丢包 |
| ICMP（`ping`） | ✅ | ❌ |
| 家侧内核 | **必须本 fork 构建**（上游静默忽略 `tun: true`） | 官方 `easytier/easytier` 镜像即可 |

> 本机与家里同属 `10/8` 时，客户端规则**只能精确写家里网段**，不要用 `10.0.0.0/8`。
