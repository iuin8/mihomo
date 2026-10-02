# 示例文件地图（EasyTier 回家）—— 按**三方**分目录

**先读 SOP**：[`../../easytier_gateway_sop.md`](../../easytier_gateway_sop.md)（照做即可，约 20 分钟）。
原理与实测数据：[`../../easytier_gateway.md`](../../easytier_gateway.md)；
规格与验收：[`../../easytier_tun_spec.md`](../../easytier_tun_spec.md)。

| 角色 | 目录 | 一句话 |
| --- | --- | --- |
| **客户端**（你的电脑/手机） | [`client/`](client/) | 一份订阅模板 ✓ —— **别人拿到直接用，不用改任何字段** ✓ |
| **家里**（L3 网关） | [`home/`](home/) | 方案 A：mihomo TUN 网关（用 GHCR 镜像 ✓）；[`home/alt-native/`](home/alt-native/) 是方案 B 零特权 |
| **公网节点**（会合点） | [`rendezvous/`](rendezvous/) | 有公网 IP 的机器上跑一个共享节点 ✓（家侧无公网时必需 ✓） |
| 三方共用 | [`tools/`](tools/) | 探针与回声服务（排障用 ✓） |

## 客户端 —— [`client/`](client/)

| 文件 | 用途 |
| --- | --- |
| [`client/subscription.yaml`](client/subscription.yaml) | **订阅模板**：内核内置 easytier 出站 + 家里网段规则 ✓。`ipv4`/`hostname` 故意不写 → 由 overlay DHCP 自动分配唯一地址 ✓（所以对方不用改 ✓）；含 A/B 两种并入写法、`state-dir` 陷阱、以及"做成订阅链接分享"的注意事项 ✓ |

> 分享要点：**密钥即凭据** ✗（只在私有位置托管 ✓；外泄就在所有节点同时换 `network-secret` ✓）；
> 对方用官方内核即可 ✓，只有"macOS + 服务模式"那种机器才需要本 fork 的解释器内核 ✓。

## 家里 —— [`home/`](home/)

| 文件 | 用途 |
| --- | --- |
| [`home/docker-compose.yml`](home/docker-compose.yml) | **一键盘（拉镜像）**：`NET_ADMIN`/`/dev/net/tun` + `ip_forward` + 健康检查 ✓（镜像=纯 mihomo ✓） |
| [`home/docker-compose.build.yml`](home/docker-compose.build.yml) | 自编译覆盖：`docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build` ✓ |
| [`home/Dockerfile`](home/Dockerfile) / [`home/Dockerfile.prebuilt`](home/Dockerfile.prebuilt) | 多阶段构建 / 纯打包（用别处交叉编译好的二进制 ✓） |
| [`home/gateway.yaml`](home/gateway.yaml) | 家侧配置（`tun: true` ✓、`prewarm: true` ✓ 服务端必需 ✓、`proxy-networks` ✓） |
| `home/state/` | 运行时生成：**overlay 节点身份**，别删 ✓（换了它就是新节点 ✗） |
| [`home/alt-native/`](home/alt-native/) | **方案 B**：零特权 native 容器（无 TUN/无 iptables ✓；代价是无 ICMP、吞吐低一档 ✓） |

## 公网节点 —— [`rendezvous/`](rendezvous/)

| 文件 | 用途 |
| --- | --- |
| [`rendezvous/docker-compose.yml`](rendezvous/docker-compose.yml) | 放到**有公网 IP 的机器**上 `docker compose up -d` ✓；等价于 `docker run … easytier/easytier:v2.6.4`（无参数=共享节点 ✓） |

## 共用工具 —— [`tools/`](tools/)

| 文件 | 用途 |
| --- | --- |
| [`tools/probe.sh`](tools/probe.sh) | TCP + UDP 一次跑完 ✓ |
| [`tools/socks5-udp-probe.py`](tools/socks5-udp-probe.py) | SOCKS5 UDP ASSOCIATE 探针（`--relay` 用于 Docker/端口映射场景 ✓） |
| [`tools/udp-echo-server.py`](tools/udp-echo-server.py) | UDP 回声目标，放在"内网另一侧"✓ |

## 两条家侧路线对照

| | 方案 A（TUN，[`home/`](home/)，推荐） | 方案 B（[`home/alt-native/`](home/alt-native/)） |
| --- | --- | --- |
| 家侧要求 | `NET_ADMIN` + `/dev/net/tun` + `ip_forward`（compose 已给 ✓） | 无 ✓ |
| 家侧内核 | **必须本 fork 构建**（上游会静默忽略 `tun: true` ✗）→ 用 GHCR 镜像 ✓ | 官方 `easytier/easytier:v2.6.4` ✓ |
| 代理子网 TCP | ✅ 20MB @ 34.5–42.5 MB/s | ✅ 20MB @ 8.4 MB/s |
| 代理子网 UDP | ✅ 0% 丢包 | ✅ 0% 丢包 |
| ICMP（`ping`） | ✅ | ❌ |

> 本机与家里同属 `10/8` 时，客户端规则**只能精确写家里网段** ✓，不要用 `10.0.0.0/8` ✗。
