# 示例文件地图（EasyTier 回家网关）

**先读 SOP**：[`../../easytier_home_gateway_sop.md`](../../easytier_home_gateway_sop.md)（照做即可，约 20 分钟）。
原理与实测数据：[`../../easytier_home_gateway.md`](../../easytier_home_gateway.md)；
规格与验收：[`../../easytier_tun_spec.md`](../../easytier_tun_spec.md)。

## 方案 A（推荐）：家侧 mihomo 开 TUN —— 直接用镜像，一键启动

```bash
# 不需要仓库源码、不需要本地 Go：镜像已由 CI 构建并推到 GHCR
mkdir -p ~/easytier-home-gateway && cd ~/easytier-home-gateway
# 从仓库取两个文件：docker-compose.yml + home-mihomo-tun.yaml
# 改 home-mihomo-tun.yaml 里三处「改这里」：network-secret / peers / proxy-networks
docker compose up -d
```

镜像：`ghcr.io/iuin8/mihomo`（多架构 amd64/arm64 ✓，由 `.github/workflows/mihomo-image.yml` 构建）。

> 镜像就是**纯 mihomo**：里面没有任何配置、也没有入口脚本，行为全部来自挂载的 YAML。
> 家侧需要的那点"路由器"能力不在镜像里：`NET_ADMIN` + `/dev/net/tun`（建 TUN）与 `ip_forward=1`
> 由本目录的 `docker-compose.yml` 提供；SNAT 也不需要 —— 实测 EasyTier 自己会做（SOP §6.13）。

想自己编译内核（改了代码、或拉不到 GHCR）：

```bash
cd <mihomo 仓库>/docs/examples/easytier-home-gateway
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

| 文件 | 用途 |
| --- | --- |
| `docker-compose.yml` | **一键盘（拉镜像）**：`NET_ADMIN`/`/dev/net/tun` + `ip_forward` + 自动 NAT + 健康检查 |
| `docker-compose.build.yml` | 自编译覆盖文件（`-f docker-compose.yml -f docker-compose.build.yml … --build`）|
| `Dockerfile` / `Dockerfile.prebuilt` | 多阶段构建：源码编本 fork 内核（含 TUN 模式）+ 预装 iptables；prebuilt 为纯打包路径 |
| `home-mihomo-tun.yaml` | 家侧配置（`tun: true`，发布 `proxy-networks`；API 只绑 `127.0.0.1`） |
| `state/` | 运行时生成：**overlay 节点身份**，别删（换了它 = 换一个节点 ✗）|

## 会合点（家侧无公网 / 不能端口映射时必需）

| 文件 | 用途 |
| --- | --- |
| `rendezvous/docker-compose.yml` | 放到**有公网 IP 的机器**上 `docker compose up -d`；等价于 `docker run … easytier/easytier:v2.6.4`（无参数=共享节点） |

## 本机侧（客户端）

**当前生产形态**（见 SOP §6.9）：客户端**一个内核搞定** —— 在 CVR profile 里加一个 `type: easytier`
出站 + 指向它的 `🏠 家里` 组 + 家里网段规则即可，不需要额外进程。

> ⚠️ 客户端内核必须是**本 fork 的构建**：服务模式下 wazero 的 JIT 页会被 macOS 判非法
> （官方内核同样中招），本 fork 用解释器运行时绕开，见 SOP §6.4 / §6.8。
> ⚠️ profile 里用**原生 `proxies:` / `proxy-groups:` / `rules:` 段** —— 多订阅合并会把这些段前置合并；
> `prepend-proxies` / `prepend-rules` 那种写法在本 fork 里**不生效**（只是测试夹具）。

| 文件 | 用途 |
| --- | --- |
| `client-clash.yaml` | **备选（历史方案）**：客户端走用户态网关（socks5 → 网关）；只在不能用本 fork 内核时使用 |

## 方案 B（备选）：家侧零特权 native 容器

| 文件 | 用途 |
| --- | --- |
| `alt-native/docker-compose.yml` + `alt-native/home-easytier-core.toml` | 家侧不便提权时用（无 `NET_ADMIN`、无 TUN、无 iptables）。代价：没有 ICMP，吞吐低一档。镜像已钉 `easytier/easytier:v2.6.4`（**不要用 latest**）|

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
| 家侧内核 | **必须本 fork 构建**（上游静默忽略 `tun: true`）→ 用 GHCR 镜像即可 | 官方 `easytier/easytier:v2.6.4` 镜像 |
| 代理子网 TCP | ✅ 20MB @ 34.5–42.5 MB/s | ✅ 20MB @ 8.4 MB/s |
| 代理子网 UDP | ✅ 0% 丢包 | ✅ 0% 丢包 |
| ICMP（`ping`） | ✅ | ❌ |

> 本机与家里同属 `10/8` 时，客户端规则**只能精确写家里网段**，不要用 `10.0.0.0/8`。
