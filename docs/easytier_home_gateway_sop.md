# SOP：打通「本机 Clash → 家里内网」（精简版）

> 一条主线：**会合点（公网）→ 家侧一键起网关 → 本机 CVR 加规则 → 三条命令验证**。
> 前置：① 一台**有公网 IP 的机器**（VPS 即可，1 核 512MB 够用；家侧没有公网时这是两端相遇的唯一前提）；
> ② 家侧服务器能拉 Docker 镜像、能 clone 本 fork 仓库。
> 详细原理与实测数据见 [easytier_home_gateway.md](easytier_home_gateway.md)，
> 验收标准见 [easytier_tun_spec.md](easytier_tun_spec.md)。

## 0. 会合点（只做一次，约 5 分钟）

**先说调研结论（2026-10-01，用 DoH 复核，绕开本机 DNS 劫持）**：

| 结论 | 依据 |
| --- | --- |
| **没有可用的官方/社区公共节点** | `public.easytier.cn`、`public.easytier.top` 均为 **NXDOMAIN**；官方文档里出现的地址（`tcp://1.2.3.4:11010` 之类）全是占位符 |
| 官方模型 = **自建** | 官方文档《搭建共享节点》教的就是"用你自己的公网机器给别人/给自己做共享节点"，并给了 fail2ban 防滥用配置 |
| ⇒ 你必须自备一台公网机器 | 1 核 512MB 云服务器足够；只跑一个容器、只占 11010 端口 |

**推荐用「私有模式」而不是公共共享节点**：只允许你自己的网络（同名 + 同密钥）连接，不会被陌生人白嫖、
不需要 fail2ban、中继流量也只属于你自己的网络。用仓库里的
`examples/easytier-home-gateway/rendezvous/docker-compose.yml`：

```bash
# 在云服务器上（把文件拷过去，改一行密钥，然后一条命令）
mkdir -p /root/easytier-rendezvous
scp 本仓库/mihomo/docs/examples/easytier-home-gateway/rendezvous/docker-compose.yml \
    root@<云服务器>:/root/easytier-rendezvous/
ssh root@<云服务器>
cd /root/easytier-rendezvous
vi docker-compose.yml          # 改 network-secret 一处（与家侧/客户端一致）
docker compose up -d
docker compose logs --tail=20  # 期望看到一堆 new listener added
```

**云安全组 / 防火墙放行 `11010` 的 tcp 与 udp**（udp 用于 P2P 探测与中继）。
记下公网 IP，家侧与客户端都填 `peers: ["tcp://<公网IP>:11010"]`。

> 不 clone 仓库、也不想用 compose 的话，等价的一条命令是：
> `docker run -d --name easytier-rendezvous --restart unless-stopped -p 11010:11010/tcp -p 11010:11010/udp easytier/easytier:latest`
> —— 但那是**公共共享节点**（任何网络的节点都能连），要给社区做贡献再用它，并按官方文档配 fail2ban。
> 想两者兼顾：公共模式 + `--relay-network-whitelist --relay-all-peer-rpc`（只帮忙打洞、不转发别人的数据）。

## 1. 家侧部署（约 10 分钟）

```bash
git clone <本 fork 仓库> && cd mihomo/docs/examples/easytier-home-gateway

# 改 home-mihomo-tun.yaml 里标了「改这里」的三处：
#   network-secret  与客户端一致的强密钥
#   peers           ["tcp://<会合点公网IP>:11010"]
#   proxy-networks  ["<家里网段>"]

docker compose up -d          # 首次自动编译本 fork 内核（实测约 1m40s），之后秒起
docker compose logs -f mihomo # 期望依次看到下面三行
```

> **国内网络**：Dockerfile 已默认走 `goproxy.cn`（官方代理 `proxy.golang.org` 在国内会 i/o timeout）
> 与中科大 Alpine 源。要换源：`GOPROXY=https://mirrors.aliyun.com/goproxy/,direct docker compose build`。
> 完全不想在容器里编译（也不依赖 Go 代理）→ 用纯打包路径，构建只要几秒：
>
> ```bash
> # 在能出网的机器上（例如你的 Mac）：交叉编译出内核
> GOOS=linux GOARCH=<uname -m 对应：x86_64→amd64, aarch64→arm64> CGO_ENABLED=0 \
>     go build -tags with_gvisor -trimpath -ldflags '-w -s' \
>     -o docs/examples/easytier-home-gateway/mihomo-linux .
> # 把 mihomo-linux 放到同一目录后：
> HOME_DOCKERFILE=docs/examples/easytier-home-gateway/Dockerfile.prebuilt docker compose up -d --build
> ```

```text
[entrypoint] added MASQUERADE on eth0                                        # NAT 自动配好
[entrypoint] starting mihomo (NAT interface: eth0, ip_forward: 1)            # 转发已注入
[entrypoint] easytier TUN is up: easytier0 10.144.0.2/24                     # 网关就绪
```

> 出站是**懒启动**的：没人用它时 EasyTier 实例不会启动、TUN 也不会创建。entrypoint 已自动触发
> （通过 `TRIGGER_PROXY`，默认 `et-home`）；如果你改了出站名，记得同步这个环境变量。
> 家侧必须开机就位——这里存在死锁：客户端要连进来要求家侧已在 overlay 上，而家侧自己没有流量
> 会把该出站当代理用，所以它**永远不会自启**。客户端侧相反：保持懒启动更好（首次访问家里时才建实例）。
> `docker stop` 是**优雅退出**（实测 0.12s，不会等 10s 被 SIGKILL）。

**验收家侧（两条命令都必须过）**：

```bash
docker compose exec mihomo ip -4 -o addr show | grep easytier0
#   期望：easytier0  inet 10.144.0.2/24 ...
docker compose exec mihomo iptables -t nat -S POSTROUTING | tail -1
#   期望：-A POSTROUTING -o eth0 -j MASQUERADE
```

> 多网卡（容器接了内网/办公网两张网）时，把 `docker-compose.yml` 的 `NAT_INTERFACE` 改成连内网那张网卡名。
> 家侧不需要任何端口映射，也不需要公网 IP —— 它靠出站连会合点。

## 2. 本机 CVR 配置（约 5 分钟）

在 Clash Verge Rev 的 **merge profile** 里加下面内容（`prepend-*` 会插到订阅规则前面，
键名与 `clash-verge-rev/src-tauri/src/enhance/merge.rs` 一致），只改三处：密钥、会合点、家里网段。

```yaml
prepend-proxies:
  - name: home-overlay
    type: easytier
    network-name: home
    network-secret: "<与家侧一致>"
    hostname: mbp-clash
    instance-name: mbp-clash
    ipv4: 10.144.0.3/24
    no-listener: true
    peers: ["tcp://<会合点公网IP>:11010"]
    udp: true

prepend-rules:
  - IP-CIDR,10.144.0.0/24,home-overlay   # overlay 网段本身也要走隧道
  - IP-CIDR,<家里网段>,home-overlay       # 精确写！本机是 10.0.4.0/22，绝不要写 10.0.0.0/8
```

> 客户端**不需要换内核**：`easytier` 出站是 upstream 就有的，你现在的 alpha 内核即可。

## 3. 验证（约 1 分钟）

```bash
# ① TCP
curl -sS -o /dev/null -w '%{http_code} %{size_download}B %{speed_download}B/s\n' \
    -x http://127.0.0.1:7897 http://<内网IP>:<端口>/      # 端口改成你 CVR 的混合端口

# ② UDP（Docker/端口映射场景要带 --relay）
python3 docs/examples/easytier-home-gateway/tools/socks5-udp-probe.py \
    --socks 127.0.0.1:7897 --target <内网IP> --port <UDP端口> --payload t

# ③ ICMP（需要客户端也有 TUN；mihomo 的 SOCKS 入站不代理 ICMP）
ping -c 3 <内网IP>
```

三条都过 = 打通完成。之后直接用家里的真实内网 IP 访问即可（与在家时完全一致）。

## 4. 排错速查

| 现象 | 原因 | 处置 |
| --- | --- | --- |
| 家侧日志没有 `tun mode enabled` | 内核不是本 fork 构建的（上游会静默忽略 `tun: true`） | 用本仓库的 `docker compose up -d --build` 重建；别用官方镜像 |
| 两条验收命令任一不过 | 缺 `NET_ADMIN`/`/dev/net/tun`、`ip_forward`、或网卡名错 | 见 `entrypoint.sh` 的 WARN 日志；改 `NAT_INTERFACE` |
| 客户端日志出现 `match MATCH using DIRECT` | 规则没生效或网段写错 | 检查 `prepend-rules` 与网段是否精确 |
| 小请求通、大流量卡住 | 走的是旧的历史方案或 no-TUN 路径 | 确认家侧 `tun: true` 生效 |
| 两端一直不相遇 | 会合点不可达/端口没放开 | 家侧 `docker compose exec mihomo wget -qO- http://<会合点>:11010` 探活；确认 11010 tcp+udp 都放行 |
| 构建卡在 `proxy.golang.org … i/o timeout` | 国内访问 Go 官方代理不通（Dockerfile 已默认换 goproxy.cn，若你本地改过或用了旧版本才会遇到） | 换源重试：`GOPROXY=https://goproxy.cn,direct docker compose build`；或走上方纯打包路径 |
| 构建报 `COPY bin/ … not found` | `dockerfile:` 写成了裸 `Dockerfile`，命中了仓库根那个打包用的 Dockerfile | 保持默认（`docs/examples/easytier-home-gateway/Dockerfile`）；覆盖时也要带目录 |
| `apk add` 卡住/超时 | Alpine 官方源在国内慢 | 默认已用中科大源；换源：`APK_MIRROR=mirrors.aliyun.com docker compose build` |

## 5. 日常运维与回退

```bash
# 升级内核/配置后重建
git pull && docker compose up -d --build

# 看状态
docker compose ps && docker compose logs --tail=50 mihomo
docker compose exec mihomo ip -o -4 addr show | grep easytier   # 网关是否在

# 回退
docker compose down                 # 家侧（state/ 目录保留，证书身份不丢，下次起还是同一节点）
# 本机：删掉 merge profile 里的 prepend-proxies / prepend-rules 两段
```

> `./state` 目录保存 overlay 节点身份，**不要删**；删了会在 overlay 里变成新节点。
> 家侧不便提权时改用 `examples/easytier-home-gateway/alt-native/`（零特权 native 容器，无 ICMP、吞吐低一档）。
