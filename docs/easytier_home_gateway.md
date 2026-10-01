# EasyTier 回家网关（本机 Clash → 家里内网）

> fork 专属文档。目标：**本机只装 Clash**，即可用与在家时相同的真实内网 IP 访问家里服务器。
> 本文所有结论都来自本仓库实验室实测（2026-09-30，Docker 在本机跑双端），不是推测。

## 一、结论（选型先看这张表）

| 家侧形态 | 代理子网 TCP | 代理子网 UDP | ICMP | 吞吐 | 结论 |
| --- | --- | --- | --- | --- | --- |
| **mihomo + 本 fork 的 TUN 模式**（`tun: true`，见§二） | ✅ 200 | ✅ 0% 丢包 | ✅ ping 通 | **20MB @ 34.5 MB/s，校验一致** | **推荐（家侧只跑 mihomo）** |
| native `easytier-core`（`--no-tun`，零特权容器） | ✅ 200 | ✅ 0% 丢包 | ❌ | 20MB @ 8.4 MB/s，校验一致 | 备选（家侧进程最小、无需特权） |
| mihomo 容器（上游强制 no-TUN，无 TUN 模式时） | ❌ `i/o timeout` | ✅（小样本）/ 负载下崩塌 | ❌ | 极小请求可用 | 已被 TUN 模式取代 |
| mihomo 容器 + hysteria2 绕行（见§五） | ⚠️ 仅 <64KB | ⚠️ | ❌ | 1MB 卡在 ~122KB | 历史方案，仅验证用 |

**为什么上游的 mihomo 不能当家侧**：上游 `ApplyRequiredFlags` 强制 `no_tun = true`，节点没有 L3 接口，
只能把每条流交给宿主逐协议转发；实测 WASI↔WASI 的 TCP 通路建立不起来——失败时家侧宿主
**没有任何中继调用**（`ConnectTCP Purpose=DataPlane/PortForward` 各 0 次），家侧容器日志也无任何活动，
而同拓扑的 UDP 可通。根因在 guest/engine 的连接携带或路由同步，不在 mihomo 的宿主实现。

**本 fork 的 TUN 模式解决的正是这件事**：宿主用 sing-tun 建一块真 TUN 设备，再用
`SendPacket`/`ReceivePacket` 搬运裸 IP 包（规格与验收见 `easytier_tun_spec.md`）。TUN 模式下
家侧变成真正的 L3 节点，三条验收（TCP 20MB 校验、UDP 零丢包、ping 通）全部实测通过。

### 为什么会牵扯到协议层（TCP/UDP 的差别从哪来）

「打通网络」在**有 TUN** 时确实是协议无关的：overlay 就是一个 L3 接口，内核把 IP 包丢进去，
TCP / UDP / ICMP 一视同仁。差别出现在**没有 TUN** 的时候：

| 模式 | 数据面 | 谁负责把包送进屋 | 协议相关性 |
| --- | --- | --- | --- |
| 有 TUN（native core） | 内核 L3 转发 | 内核路由表 | 无（TCP/UDP/ICMP 一样） |
| 有 TUN（mihomo + 本 fork TUN 模式） | 宿主搬包，内核 L3 转发 | 内核路由表 | 无（TCP/UDP/ICMP 一样） |
| 无 TUN（native `--no-tun`） | 用户态逐流中继 | EasyTier 在进程内自己实现 | 有：TCP 要拨号/接流，UDP 要绑套接字 |
| 无 TUN（mihomo 内嵌 WASI 核） | 同上，但经宿主 ABI 代办 | 宿主 mihomo 提供 `ConnectTCP`/`BindUDP`/`ListenTCP` | 有：**每条协议路径都要宿主实现一次** |

也就是说，no-TUN 把「IP 层隧道」降级成了「**逐流的用户态代理**」：此时 TCP 与 UDP 走的是
**两套完全不同的代码路径**，任一条缺失或未实现，就表现为"某种协议不通"。本仓库实测正是如此：

| 家侧实现 | TUN | 代理子网 TCP | 代理子网 UDP | 依据 |
| --- | --- | --- | --- | --- |
| native | ✅ | ✅ 8.4 MB/s | ✅ 0% 丢包 | 阶段 1 lab |
| native | ❌（`--no-tun`） | ✅ 8.4 MB/s | ✅ 0% 丢包 | 阶段 1 lab（B'） |
| mihomo 内嵌 WASI | ❌（强制） | ❌ 只有 UDP 通 | ⚠️ 小样本可用 | 阶段 0 / 变体 D / 插桩 |

结论：==这不是"隧道天然限制协议"，而是"没有 TUN 时宿主必须逐协议实现转发，而 mihomo 的
WASI 宿主在这条路径上没实现完整"==——native 在同样 `--no-tun` 下 TCP 正常，正好反证这一点。
另一层必然的协议耦合：overlay 自己的**承载层**要么 UDP 要么 TCP（`udp://` / `tcp://` peer），
属于"隧道套隧道"，会带来 TCP-over-TCP、MTU 分片、拥塞控制嵌套等物理问题——这部分任何实现都躲不掉。

## 二、推荐方案 A：家侧也用 mihomo（本 fork TUN 模式）

示例：`docs/examples/easytier-home-gateway/home-mihomo-tun.yaml` + `docker-compose.tun.yml`。

```yaml
# 家侧 mihomo 片段（关键就三行）
proxies:
  - name: et-home
    type: easytier
    ipv4: 10.144.0.2/24          # TUN 模式必填，且必须带前缀
    tun: true                    # ← 建宿主 TUN + 接包面（上游没有的能力）
    tun-routes: []               # 家侧网关不需要额外路由（内网直连）
    proxy-networks: ["192.168.1.0/24"]   # 改成你家里的网段
```

**家侧的三个硬性要求**（缺一不可，实测踩过）：

1. **权限**：`--cap-add NET_ADMIN --device /dev/net/tun:/dev/net/tun`
   （容器内 `/proc/sys` 只读，转发开关必须用创建参数注入）；
2. **转发**：`--sysctl net.ipv4.ip_forward=1`；
3. **NAT**：`iptables -t nat -A POSTROUTING -o <内网网卡> -j MASQUERADE`
   —— 内网主机看到的是 overlay 源地址，回包必须 NAT 回来。

启动成功的标志（日志 + 接口）：

```text
[EasyTier](et-home) tun mode enabled on 10.144.0.2/24
$ ip -4 -o addr show | grep easytier0
easytier0    inet 10.144.0.2/24 ...
```

**实测（2026-10-01，Docker 实验室）**：

| 项目 | 结果 |
| --- | --- |
| TCP 20MB 下载 | `200`，0.61s，**34.5 MB/s**，两侧 SHA-256 相同 |
| UDP 回环 | 收到 `echo:tun-e2e`，端到端可用 |
| `ping` 内网机器 | 3/3 通（0.9–6.8 ms）；对照组（家侧 `FORWARD DROP`）→ 100% 丢包，证明流量穿家侧 TUN |

客户端侧**不需要改动**：仍是"应用 → mihomo 代理 TUN → 规则 → `easytier` 出站 → overlay"，
所以域名/进程级分流照旧可用（见§五的取舍说明）。

## 三、备选方案 B：家侧一个零特权 native 容器

家在 `docs/examples/easytier-home-gateway/` 下给出三个文件：

- `home-easytier-core.toml` —— 家侧 native 节点（`no_tun = true`，发布家里网段）
- `docker-compose.yml` —— 家侧容器（**无 cap_add、无 /dev/net/tun、无 host 网络、无 iptables**）
- `client-clash.yaml` —— 本机 Clash 片段（一个 `easytier` 出站 + 精确网段规则）

### 家侧

```bash
cd <家侧目录>
# 1) 填 home-easytier-core.toml 里的 network_secret
# 2) 按需改 compose 的 -n <家里网段>
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml logs -f --tail=50   # 期望看到 new listener added / new peer added
```

会合点（`peers`）三选一：

1. **自建共享节点**（推荐，官方文档：`easytier-core` **不带任何参数**启动即为共享节点，无需 root）
   —— 任何有公网 IP 的机器上 `easytier-core`，两端 `peers: ["tcp://<IP>:11010"]`；
2. 家侧有公网 IPv6 时直接把家侧监听地址当 peer（真正零第三方）；
3. 社区节点：**官方文档只给出占位符**（`tcp://<共享节点 IP或域名>:1010`），
   没有公布任何真实地址；实测 `public.easytier.top` / `public.easytier.cn` 均为 **NXDOMAIN**，不要照抄。

### 本机（Clash）

把 `client-clash.yaml` 的 proxies/rules 并进 Clash 配置（CVR 里放 merge profile 即可）：

```yaml
rules:
  - IP-CIDR,10.144.0.0/24,home-overlay   # overlay 网段本身也要走隧道，否则落到 MATCH 直连
  - IP-CIDR,<家里网段>,home-overlay
  - MATCH,DIRECT
```

> 本机所在网段若与家里同属 `10/8`（本项目就是这样：本机 `10.0.4.0/22`、家里 `10.0.0.0/24`），
> **只能精确写家里网段，绝不能用 `IP-CIDR,10.0.0.0/8`**，否则会把本机局域网一起劫持。

## 四、备选方案 B 的实测数据（native 家侧，可在本机复现）

家侧 = `easytier/easytier:latest` 容器，`--no-tun`、零特权、bridge 网络 + 端口映射；
客户端 = 本仓库 sidecar（mihomo）独立实例，规则强制走 overlay（命中即无直连回退）。

| 项目 | 结果 |
| --- | --- |
| 64KB / 1MB / 20MB 下载 | `200`；1MB @ 7.8 MB/s、20MB @ 8.4 MB/s，**SHA-256 与源一致** |
| UDP（800B × 40 包） | `40/40`，0% 丢包 |
| 家侧虚拟 IP / 家侧 LAN IP / 内网另一台机器 | 三者均 `200` |
| 客户端内核特性 | 无需 TUN、无需 root、无需改 `ip_forward`、无需 MASQUERADE |

排错时注意两个**容易误判**的坑（都实际踩过）：

- mihomo 的 **external-controller 有 DNS-rebinding 保护**：`Host: <内网IP>:9090` 会被静默丢弃并超时，
  换 `-H 'Host: 127.0.0.1:9090'` 立刻 200。别把它当成隧道不通。
- 用 Docker 做实验时，SOCKS5 的 UDP 中继地址若返回容器内网 IP，需用可路由地址（本仓库
  `tools/socks5-udp-probe.py --relay <host:port>`）；且**务必发布 UDP 端口**（`-p x:y/udp`），
  只发 TCP 会让一切 UDP 测试假失败。

## 五、历史方案（已废弃）

双 mihomo + hysteria2 绕行（把 TCP 封装进 QUIC/UDP）实测只能跑通极小请求、64KB 起失败、
负载后不自愈，已在 TUN 模式落地后废弃；配置文件已从仓库删除。

原因、实测数据与取回用的提交引用都在 [history/easytier_dual_mihomo_detour.md](history/easytier_dual_mihomo_detour.md)。

## 六、回退与清理

```bash
docker compose -f docker-compose.yml down        # 家侧
# 本机：移除 merge profile 里的 easytier 出站与两条规则
```
