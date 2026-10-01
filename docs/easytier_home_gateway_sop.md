# SOP：打通「本机 Clash → 家里内网」（精简版）

> 一条主线：**会合点（公网）→ 家侧一键起网关 → 本机 CVR 加规则 → 三条命令验证**。
> 前置：① 一台**有公网 IP 的机器**（VPS 即可，1 核 512MB 够用；家侧没有公网时这是两端相遇的唯一前提）；
> ② 家侧服务器能拉 Docker 镜像、能 clone 本 fork 仓库。
> 详细原理与实测数据见 [easytier_home_gateway.md](easytier_home_gateway.md)，
> 验收标准见 [easytier_tun_spec.md](easytier_tun_spec.md)。

## 0. 会合点（只做一次，约 2 分钟）

在**有公网 IP 的机器**上（VPS）：

```bash
docker run -d --name easytier-shared --restart unless-stopped \
    -p 11010:11010/tcp -p 11010:11010/udp easytier/easytier:latest
```

不带任何参数 = 官方共享节点模式，无需 root、无需配置文件。记下这台机器的公网 IP，
下面两端都填 `tcp://<公网IP>:11010`。

> 仓库里也有等价文件：`examples/easytier-home-gateway/rendezvous/docker-compose.yml`（`docker compose up -d` 即可）。
> 防火墙只需放行 11010 的 **tcp 和 udp**。

## 1. 家侧部署（约 10 分钟）

```bash
git clone <本 fork 仓库> && cd mihomo/docs/examples/easytier-home-gateway

# 改 home-mihomo-tun.yaml 里标了「改这里」的三处：
#   network-secret  与客户端一致的强密钥
#   peers           ["tcp://<会合点公网IP>:11010"]
#   proxy-networks  ["<家里网段>"]

docker compose up -d          # 首次自动编译本 fork 内核（实测约 1m45s），之后秒起
docker compose logs -f mihomo # 期望依次看到下面三行
```

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
