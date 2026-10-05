# 双向 mesh：客户端 ↔ 家侧（本机同时当「被访问的网段」与「家侧出口」）

## 这个目录解决什么

一台**移动中的机器**（笔记本 / 工作站，下面叫「本机」）与**家侧网关**互通：

| 方向 | 目标 | 本机需要什么 | 家侧需要什么 |
| --- | --- | --- | --- |
| **本机 → 家侧** | 访问家里内网 / 集群 | 现有 client 出站即可 ✓ | 家侧网关（见 `../gateway/`）✓ |
| **家侧 → 本机所在网段** | 家里那台能访问本机所在的局域网 | `tun: true` + `ipv4` + **`proxy-networks: [本机网段]`** ✓ | **`tun-routes: [本机网段]`** ✓ |
| **家侧 → 外网（经本机）** | 家侧任何时候都能借本机的线路出网 | **`enable-exit-node: true`** ✓ | 一个**指向本机的出站** + 规则 ✓ |

> 本机需要 `tun: true` 的**只有中间那一行** ✓；出网那一行只需 `enable-exit-node: true` ✓。
> 两个都要就用下面这份「全量版」✓ —— 注意 `tun: true` 要求 **root / CAP_NET_ADMIN**（CVR 服务模式天然满足 ✓）。

## 为什么不再用「DDNS + HTTP 代理」✗

旧做法是家侧 mihomo 里写死 `type: http, server: <DDNS 名称>, port: 7897` ✓ —— 它有三个硬伤 ✗：

1. **只在同一局域网可用** ✗：DDNS 解析到本机的**当前**地址，本机一离开家就断 ✓（实测 ✓）；
2. **HTTP 代理承载不了 UDP** ✗：QUIC / 游戏 / 部分 DNS 会静默失败 ✓；
3. **多一跳嵌套代理** ✗：家侧 → 本机代理 → 再出网，规则与排障都更绕 ✓。

改用 overlay 后 ✓：目标用**本机在 overlay 里的固定地址**表达 ✓（不需要 DDNS ✗、不需要端口映射 ✗），
UDP/ICMP 一视同仁 ✓，而且**本机侧改一行开关**即可 ✓。

## 本机侧（`workstation.yaml`）

把下面这段并进客户端出站 ✓（本文与 `../client/subscription-k8s.yaml` 同构 ✓）：

```yaml
- name: et-fa
  type: easytier
  network-name: private-overlay
  network-secret: "<与家侧一致>"
  # ── 让家侧能访问本机所在网段 ✓（需要 root，CVR 服务模式满足 ✓）
  tun: true
  ipv4: 10.144.0.6/24              # ← TUN 模式**必填**且全网唯一 ✗✓（改前先用旁观者节点看是否被占 ✓）
  proxy-networks: ["192.168.0.0/24"]   # ← 本机所在网段（**别写本机看不到的网段，也别与家侧重叠** ✗）
  tun-routes: []                   # 本机网段是直连的 ✓，无需 pin ✗（pin 会绕开 mihomo 策略 ✓）
  # ── 让家侧能借本机出网 ✓
  enable-exit-node: true
  # ── 原有
  no-listener: true
  peers: ["tcp://<会合点>:11010"]
  udp: true
  exit-nodes: ["<家侧网关的 overlay 地址>"]   # 本机→家侧方向仍走它 ✓
```

**系统开关（只与"家侧访问本机网段"有关 ✓）**：

```bash
sysctl net.inet.ip.forwarding              # 默认 0 ✗ —— 本 fork **不会**替你打开 ✓
sudo sysctl -w net.inet.ip.forwarding=1    # 打开 ✓（重启失效，要持久写 /etc/sysctl.conf ✓）
```

## 家侧侧（`home-gateway.yaml` 片段）

家侧网关**新增一个出站**指向本机 ✓（不要改它原有的出站 ✗）：

```yaml
- name: et-mac
  type: easytier
  network-name: private-overlay
  network-secret: "<同上>"
  no-listener: true
  peers: ["tcp://<会合点>:11010"]
  udp: true
  exit-nodes: ["<本机的 overlay 地址，如 10.144.0.6>"]   # ⭐ 家侧拨出去的"外网"流量落到本机 ✓
  # tun / prewarm 都不需要 ✓ —— 它只是家侧的"出口拨号线"✓
```

配套规则 ✓（保持家侧**原有的** DIRECT 规则在前 ✗ 不要动 ✓，只在最后按需改写兜底 ✓）：

```yaml
rules:
  # …家侧原有的：内网网段 / cluster.local / 等 保持 DIRECT ✓
  - GEOSITE,cn,DIRECT
  - GEOIP,cn,DIRECT
  - MATCH,et-mac          # ← 原来的 MATCH,GLOBAL-ROUTER（HTTP 代理）换成这个 ✓
```

**另加一行让家侧能访问本机所在网段** ✓（放进家侧网关自身的 easytier 出站里 ✓）：

```yaml
  tun-routes: ["192.168.0.0/24"]    # 从 [] 改成这个 ✓；原因见下
```

> 为什么家侧需要它 ✗：访问本机网段的包是**从家侧局域网进来的** ✓，家侧内核默认没有到该网段的路由 ✗ →
> 必须显式指到 easytier 的 TUN ✓。反方向（家侧主动拨出）不需要 ✓，因为目标走的是出口节点 ✓。

## 验收（三个方向都要过 ✓）

```bash
# ① 本机 → 家侧（不能回归 ✗）
curl -x http://127.0.0.1:7897 http://<家侧内网地址>/          # 期望 200 ✓

# ② 家侧 → 本机所在网段（在家侧执行 ✓）
ping -c2 192.168.0.142                                        # 本机 ✓
ping -c2 <本机网段里另一台机器>                                 # 同网段的其它主机 ✓（这才是"访问网段"✓）

# ③ 家侧 → 外网经本机（在家侧执行 ✓）
curl -s -o /dev/null -w '%{http_code}\n' https://www.google.com   # 家侧本机上应失败 ✗，经 et-mac 应 200 ✓
```

## 坑（都踩过 ✓）

| 坑 | 处置 |
| --- | --- |
| **两个出站写同一 `proxy-networks`** ✗ | 家侧会出现两条等价路由 ✓ → 走哪条随机 ✓；**只在一个出站上公告** ✓ |
| **本机网段与家侧/集群/overlay 重叠** ✗ | 症状是"时通时不通"✓，极难查 ✓；先核对再改 ✓ |
| **`ipv4` 忘了填** ✗ | `tun: true` 时构造期直接报错 ✓（fork 校验 ✓） |
| **静态地址撞车** ✗ | 用 `../tools/probe.sh` 或旁观者节点先看 peer 表 ✓；高位取址（如 .6 / .30 ✓），别用 DHCP 低位 ✓ |
| **同机多个 easytier 出站同名** ✗ | 内核已自动用 `<主机名>-<出站名>-<随机后缀>` ✓（含 `host-id` 修复的内核 ✓）→ 用旧内核会出现"只有一个出站能用" ✓ |
| **UDP 想要但走了 HTTP 代理** ✗ | 本方案不经过 HTTP 代理 ✓，UDP 原生可用 ✓（这正是换掉旧做法的理由之一 ✓） |
