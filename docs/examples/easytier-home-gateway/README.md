# 示例文件地图（EasyTier 回家网关）

操作步骤见 [`../../easytier_home_gateway.md`](../../easytier_home_gateway.md)；
规格与验收标准见 [`../../easytier_tun_spec.md`](../../easytier_tun_spec.md)。

| 文件 | 用途 | 什么时候用 |
| --- | --- | --- |
| `home-mihomo-tun.yaml` | 家侧 mihomo，**TUN 模式**（`tun: true`） | **推荐**：家侧只跑 mihomo，TCP/UDP/ICMP 全通、吞吐最高 |
| `docker-compose.tun.yml` | 上面那份的家侧容器 | 同上；含 `NET_ADMIN` / `/dev/net/tun` / `ip_forward`，NAT 需起容器后执行一次 |
| `home-easytier-core.toml` | 家侧 **native** EasyTier 节点（`no_tun = true`） | 备选：家侧不便提权（不给容器 cap/TUN、不改内核转发） |
| `docker-compose.yml` | 上面那份的家侧容器（零特权） | 同上 |
| `client-clash.yaml` | 本机 Clash 片段（easytier 出站 + 两条精确网段规则） | 两种家侧方案通用 |
| `tools/probe.sh` | TCP + UDP 一次跑完的探针 | 验证数据面 |
| `tools/socks5-udp-probe.py` | SOCKS5 UDP ASSOCIATE 探针（含 `--relay`） | 单独验 UDP；Docker/端口映射场景必需 |
| `tools/udp-echo-server.py` | UDP 回声目标 | 放在"内网另一侧"当被测目标 |

## 两条路线的取舍

| | 方案 A（TUN，推荐） | 方案 B（native 零特权） |
| --- | --- | --- |
| 家侧要求 | `NET_ADMIN` + `/dev/net/tun` + `ip_forward` + MASQUERADE | 无（普通容器即可） |
| 代理子网 TCP | ✅ 20MB @ 34.5–42.5 MB/s | ✅ 20MB @ 8.4 MB/s |
| 代理子网 UDP | ✅ 0% 丢包 | ✅ 0% 丢包 |
| ICMP（`ping`） | ✅ | ❌ |
| 内核/镜像 | **必须是本 fork 构建**（上游会静默忽略 `tun: true`） | 上游/官方 `easytier/easytier` 镜像即可 |

## 使用前务必替换

- `network-secret`（家侧与客户端必须一致）
- `proxy-networks` / 客户端规则里的内网网段
- 会合点：`peers: ["tcp://<IP>:11010"]`（自建共享节点 / 家侧公网地址）

> 本机与家里同属 `10/8` 时，客户端规则**只能精确写家里网段**，不要用 `10.0.0.0/8`。
