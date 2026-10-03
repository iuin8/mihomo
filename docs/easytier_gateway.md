# EasyTier 回家网关（本机只装 Clash → 家里内网）

> fork 专属文档。目标：**本机只装 Clash**，用与在家时相同的真实内网 IP 访问家里服务器。
> 所有结论都来自本仓库实验室实测（日期标注在数据旁），不是推测。
> 规格与验收标准：`easytier_tun_spec.md`；示例文件地图：`examples/easytier/README.md`。

## 一、快速开始（三步）

### 前提

1. **家侧内核必须是本 fork 构建的**（`iuin8/mihomo`）。上游内核会**静默忽略** `tun: true`：
   配置校验照样通过，但你又回到了 no-TUN 模式——表现为代理子网 TCP 不通、ICMP 不可用，且没有任何报错。
2. **两端要能会合**，三选一（详见§三.3）：
   - 自建共享节点（官方推荐：任意有公网 IP 的机器上 `easytier-core` **不带参数**启动即可，无需 root）；
   - 家侧有公网 IPv6 → 直接把家侧监听地址写进客户端 `peers`；
   - 家宽可做端口映射 → 映射 11010/tcp+udp 后家侧直连。
   - 社区节点：官方文档只有占位符，没有公布真实地址（实测 `public.easytier.top` / `public.easytier.cn` 均为 NXDOMAIN）。

### Step 1：家侧起 mihomo（TUN 模式，推荐）

**一键版见 [easytier_gateway_sop.md](easytier_gateway_sop.md)**。仓库已把三件硬性要求与内核编译
都封装进 `examples/easytier/gateway/docker-compose.yml` + 已发布镜像 `ghcr.io/iuin8/mihomo`
（镜像=纯 mihomo ✓，特权与转发由 compose 提供 ✓；不再需要入口脚本或 NAT ✓，见 SOP §6.12–§6.13）：

```bash
cd docs/examples/easytier
# 改 gateway.yaml 里标了「改这里」的三处：network-secret / peers / proxy-networks
docker compose up -d
docker compose logs -f mihomo     # 期望：tun mode enabled on 10.144.0.2/24
```

会合点（家侧无公网时必需）：`rendezvous/docker-compose.yml` 放到**有公网 IP 的机器**上 `docker compose up -d`，
两端 `peers` 都填它。三件硬性要求为什么缺一不可：

| 要求 | 为什么 |
| --- | --- |
| `--cap-add NET_ADMIN` + `--device /dev/net/tun:/dev/net/tun` | 建 TUN 设备；容器内 `/proc/sys` 只读，转发开关只能靠创建参数 |
| `--sysctl net.ipv4.ip_forward=1` | TUN 模式让家侧成为**真路由器**，要转发到内网 |
| `POSTROUTING MASQUERADE` | 内网主机看到的是 overlay 源地址，回包必须 NAT 回来 |

**成功的标志**：

```text
[EasyTier](et-gateway) tun mode enabled on 10.144.0.2/24          # 日志
$ ip -4 -o addr show | grep easytier0
13: easytier0    inet 10.144.0.2/24 brd 10.144.0.255 scope global easytier0
```

**不愿给家侧提权？** 走方案 B（零特权 native 容器）见§四——TCP/UDP 同样可用，但没有 ICMP、吞吐低一档。

### Step 2：本机 Clash 并入配置

把 `examples/easytier/client/subscription.yaml` 的 `proxies` 与 `rules` 并进 Clash 配置
（CVR 里放 merge profile 即可），改两处：`network-secret`、`peers`。

```yaml
rules:
  - IP-CIDR,10.144.0.0/24,home-overlay   # overlay 网段本身也要走隧道，否则落到 MATCH 直连（表现为 502）
  - IP-CIDR,<家里网段>,home-overlay
  - MATCH,DIRECT
```

> ⚠️ 本机与家里若同属 `10/8`（**举例**：某次实验时本机在 `10.0.4.0/22`、家里 `10.0.0.0/24` ——
> ⚠️ **本机网段会变** ✗，别把这里的数字当成现状 ✓：以本机 `ipconfig getifaddr en0` + 掩码、
> 家侧 `ip addr` 为准 ✓），
> **只能精确写家里网段，绝不能用 `IP-CIDR,10.0.0.0/8`**，否则会把本机局域网一起劫持。
> 想按域名/进程分流就用 mihomo 规则；想把某个网段交给内核直通才用 `tun-routes`（pin 的前缀会绕过 mihomo 策略）。

### Step 3：验证（三条命令）

```bash
# ① TCP：走 SOCKS/混合入口访问家里某台机器的 HTTP（换成你的端口/路径）
curl -sS -o /dev/null -w '%{http_code} %{size_download}B %{speed_download}B/s\n' \
    -x http://127.0.0.1:7891 http://<内网IP>:<端口>/<路径>

# ② UDP：SOCKS5 UDP ASSOCIATE 探针（Docker/端口映射场景要带 --relay）
python3 docs/examples/easytier/tools/socks5-udp-probe.py \
    --socks 127.0.0.1:7891 --relay 127.0.0.1:7891 --target <内网IP> --port <UDP端口> --payload test

# ③ ICMP：需客户端侧有 TUN（mihomo 的 SOCKS 入站不代理 ICMP；native 客户端或 B 方案客户端 TUN）
ping -c 3 <内网IP>
```

判据：① `200`；② 打印 `PASS[udp]`；③ 3/3 通。三条都过 = 打通完成。

## 二、怎么选（先看这张表）

| 家侧形态 | 代理子网 TCP | 代理子网 UDP | ICMP | 吞吐（实测） | 结论 |
| --- | --- | --- | --- | --- | --- |
| **mihomo + 本 fork TUN 模式**（`tun: true`） | ✅ 200 | ✅ 0% 丢包 | ✅ ping 通 | **20MB @ 34.5–42.5 MB/s，校验一致** | **推荐（家侧只跑 mihomo）** |
| native `easytier-core`（`--no-tun`，零特权容器） | ✅ 200 | ✅ 0% 丢包 | ❌ | 20MB @ 8.4 MB/s，校验一致 | 备选（家侧不便提权时） |
| mihomo 容器（上游强制 no-TUN，无 TUN 模式时） | ❌ `i/o timeout` | ✅（小样本）/ 负载下崩塌 | ❌ | 极小请求可用 | 已被 TUN 模式取代 |
| mihomo 容器 + hysteria2 绕行 | ⚠️ 仅 <64KB | ⚠️ | ❌ | 1MB 卡在 ~122KB | 历史方案，见`history/` |

**为什么上游 mihomo 不能当家侧**：上游 `ApplyRequiredFlags` 强制 `no_tun = true`，节点没有 L3 接口，
只能把每条流交给宿主逐协议转发；实测 WASI↔WASI 的 TCP 通路建立不起来（失败时家侧宿主
`ConnectTCP Purpose=DataPlane/PortForward` 各 0 次，家侧日志无任何活动，而同拓扑 UDP 可通）。
**本 fork 的 TUN 模式解决的正是这件事**：宿主用 sing-tun 建一块真 TUN，再用 `SendPacket`/`ReceivePacket`
搬运裸 IP 包，家侧因此成为真正的 L3 节点。

### 为什么会牵扯到协议层（TCP/UDP 的差别从哪来）

「打通网络」在**有 TUN** 时确实是协议无关的：overlay 就是一个 L3 接口，内核把 IP 包丢进去，
TCP / UDP / ICMP 一视同仁。差别出现在**没有 TUN** 的时候：

| 模式 | 数据面 | 谁负责把包送进屋 | 协议相关性 |
| --- | --- | --- | --- |
| 有 TUN（native core） | 内核 L3 转发 | 内核路由表 | 无（TCP/UDP/ICMP 一样） |
| 有 TUN（mihomo + 本 fork TUN 模式） | 宿主搬包，内核 L3 转发 | 内核路由表 | 无（TCP/UDP/ICMP 一样） |
| 无 TUN（native `--no-tun`） | 用户态逐流中继 | EasyTier 在进程内自己实现 | 有：TCP 要拨号/接流，UDP 要绑套接字 |
| 无 TUN（mihomo 内嵌 WASI 核） | 同上，但经宿主 ABI 代办 | 宿主 mihomo 提供 `ConnectTCP`/`BindUDP`/`ListenTCP` | 有：**每条协议路径都要宿主实现一次** |

结论：这不是"隧道天然限制协议"，而是"没有 TUN 时宿主必须逐协议实现转发，而 WASI 宿主在这条路径上
没实现完整"——native 在同样 `--no-tun` 下 TCP 正常，正好反证这一点。另一层躲不掉的耦合：overlay 自己的
**承载层**要么 UDP 要么 TCP（`udp://` / `tcp://` peer），属于"隧道套隧道"，会带来 TCP-over-TCP、MTU 分片、
拥塞控制嵌套等物理问题。

## 三、方案 A 详解（家侧 mihomo TUN 模式）

### 1. 关键配置

```yaml
proxies:
  - name: et-gateway
    type: easytier
    ipv4: 10.144.0.2/24          # TUN 模式必填，必须是带前缀的 IPv4 CIDR
    tun: true                    # ← 本 fork 的能力：建宿主 TUN + 接包面
    tun-routes: []               # 家侧是网关、内网直连，无需额外路由
    proxy-networks: ["192.168.1.0/24"]   # 改成你家里的网段
    mtu: 1380                    # 同时作为 TUN 设备 MTU
    udp: true
```

`tun-routes` 只在需要"某网段交给内核直通"时才填；填了的前缀不走 mihomo 策略（域名/进程分流失效），
且**不要与 mihomo 自己 TUN 的 `route-address` 重复**、不要 pin 本机所在网段。

### 2. 实测数据（2026-10-01，Docker 实验室）

拓扑：本机 Clash 客户端（easytier 出站，无 TUN）→ overlay → 家侧 mihomo（`tun: true`，发布内网网段）→ 内网目标。

| 项目 | 结果 |
| --- | --- |
| TCP 20MB | `200`，0.49–0.61s（34.5–42.5 MB/s），两侧 SHA-256 一致 |
| UDP 40 包 × 800B | `sent=40 recv=40 loss=0%` |
| `ping` 内网机器 | 3/3 通；**对照组**：家侧 `FORWARD DROP` → 100% 丢包，恢复后 2/2 通（证明流量穿家侧 TUN） |

### 3. 会合点（两端怎么找到彼此）

| 方式 | 做法 | 适用 |
| --- | --- | --- |
| **自建会合点（推荐）** | 有公网 IP 的机器上跑 `rendezvous/docker-compose.yml`：**私有模式**（`--private-mode true` + 网络名/密钥），只有你自己的网络能连 | 通用、无需改家宽；不用 fail2ban、不会被陌生人白嫖 |
| 公共共享节点 | `easytier-core` 不带参数即公共共享节点；官方文档要求配 fail2ban 防滥用，并可用 `--relay-network-whitelist --relay-all-peer-rpc` 只帮忙打洞不转发 | 想为社区做贡献时 |
| 家侧公网 IPv6 | 客户端 `peers` 直接写家侧监听地址 | 家宽有 v6 时零第三方 |
| 家宽端口映射 | 映射 11010/tcp+udp 到家侧 | 有公网 IPv4 时最短路径 |

> **调研结论（2026-10-01，DoH 复核绕开本机 DNS 劫持）**：==官方与社区都没有公布可用的公共节点地址==。
> `public.easytier.cn` / `public.easytier.top` 均为 **NXDOMAIN**；官方文档里出现的地址（`tcp://1.2.3.4:11010` 等）
> 全是占位符。官方模型是"用户自建公共共享节点给社区用"——所以无公网环境下**必须自备一台公网机器**。
> 部署步骤见 [easytier_gateway_sop.md](easytier_gateway_sop.md) §0。

### 4. DNS（复用 mihomo，零代码）

```yaml
dns:
  nameserver-policy:
    "et.net.": "et://<easytier出站名>"    # overlay 节点名（A/PTR），出站创建时已自动注册
    "+.home.lan": ["10.0.0.1"]            # 家里 DNS（纯 IP），查询经规则/路由送达
  respect-rules: true
  proxy-server-nameserver: ["223.5.5.5"]  # respect-rules 必填，否则启动报错
```

**不要把 53 流量 pin 进 easytier TUN**：`dns-hijack` 只作用于 mihomo 自己的 TUN 入站，进 easytier TUN
的 DNS 不会被劫持，会丢掉 fake-ip/域名分流。

## 四、方案 B 详解（家侧零特权 native 容器）

适用：家侧不便给容器 `NET_ADMIN`/`/dev/net/tun`，或不想让家侧进程碰内核转发。文件在
`examples/easytier/gateway/alt-native/`：`easytier-core.toml`（native 节点，`no_tun = true`，
发布家里网段）、`docker-compose.yml`（**无 cap_add、无 /dev/net/tun、无 host 网络、无 iptables**）。

```bash
cd docs/examples/easytier/gateway/alt-native
# 1) 填 easytier-core.toml 的 network_secret  2) 改 compose 的 -n <家里网段>
docker compose up -d
docker compose logs -f --tail=50   # 期望 new listener added / new peer added
```

实测（2026-09-30）：64KB/1MB/20MB 下载均 `200`（20MB @ 8.4 MB/s，SHA-256 一致）、UDP 40 包 0% 丢包、
家侧虚拟 IP / 家侧 LAN IP / 内网另一台机器三者均 `200`；客户端无需 TUN/root，家侧无需改 `ip_forward`、无需 MASQUERADE。
**代价**：没有 ICMP（`ping` 不通），吞吐比 TUN 模式低一档。

## 五、排错（都是实际踩过的坑）

| 现象 | 真实原因 | 处置 |
| --- | --- | --- |
| 配了 `tun: true` 却仍不通 TCP、无 ICMP，日志无报错 | 内核是上游版，静默忽略 `tun: true` | 换成 fork 构建的内核；确认日志有 `tun mode enabled` |
| `502`，客户端日志显示命中 `MATCH,DIRECT` | overlay 网段本身没走隧道 | 加 `IP-CIDR,10.144.0.0/24,<easytier出站>` |
| 家侧 TUN 起来了，但内网目标回包收不到 | 缺 `ip_forward` 或 MASQUERADE | 见§一 Step 1 三个硬性要求 |
| `curl -H` 之外访问家侧 API 超时 | mihomo external-controller 的 DNS-rebinding 保护（只认 localhost Host） | 加 `-H 'Host: 127.0.0.1:9090'`，别当成隧道不通 |
| UDP 测试全失败 | Docker 只发布了 TCP 端口，或 SOCKS5 中继地址是容器内网 IP | `-p x:y/udp`；探针加 `--relay <可路由地址>` |
| 家侧虚拟 IP 拨不通 | 家侧还是 no-TUN 模式 | 同上，检查内核与 `tun mode enabled` 日志 |

## 六、历史方案与回退

- 双 mihomo + hysteria2 绕行（把 TCP 封装进 QUIC/UDP）已废弃：只能跑通极小请求、64KB 起失败、负载后不自愈。
  原因、实测数据与取回用的提交引用见 [history/easytier_dual_mihomo_detour.md](history/easytier_dual_mihomo_detour.md)。
- 回退：

```bash
docker compose down                       # 方案 A 家侧（在 examples/easytier/gateway/）
cd alt-native && docker compose down      # 方案 B 家侧
# 本机：移除 merge profile 里的 easytier 出站与那两条 IP-CIDR 规则
```
