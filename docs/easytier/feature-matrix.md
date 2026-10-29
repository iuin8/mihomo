# 功能 × 设置 交互矩阵（改任何一项前先查这张表 ✓）

> 目的：这套链路有**五个功能**，它们共用同一份配置，**任何一项设置都可能顺带弄坏另一项** ✗。
> 本表列出：每个功能**依赖什么**、**哪些设置会破坏它**、以及**一条能一分钟内证伪的回归判据**。

## 一、五个功能与依赖

| # | 功能 | 必须成立（依赖） | 破坏它的典型改法 ✗ | 回归判据（一条命令） |
| --- | --- | --- | --- | --- |
| ① | **现场直连内网**（公司/家 LAN） | 分组 `<内网分组> = DIRECT`；A 类域名在 `fake-ip-filter`；B 类域名在 `nameserver-policy` | 现场把分组切到入口节点；删 `fake-ip-filter` | 终端 `curl <内网服务>` ⇒ 2xx/3xx 且 **<100ms** |
| ② | **离网进内网**（经家侧） | 家侧**公告内网网段** + 家侧镜像=**本 fork 构建** + 分组切到入口节点 | 家侧用 `:latest`（会解析到旧/未验证 fork 构建 ⇒ 不可复现、公告可能不生效 ✗）；家侧规则把 underlay 收进隧道 | `curl /proxies/<入口节点>/delay?url=<内网服务>` 有毫秒数 ✓ |
| ③ | **家侧借本机机场**（出口） | 家侧 `MATCH → et-mac`；本机出口节点 `dialer-proxy=<机场组>`；`purpose=4` 走 forward dialer | 给入口节点配 `dialer-proxy`；把 `interface-name` 指向 overlay 设备 | 家侧终端 `curl https://www.google.com` ⇒ 200；家侧日志含 `using et-mac` |
| ④ | **本机自己上网** | TUN 开、机场组可用 | 内网规则误吞公网；TUN 关 | 经 7897 `curl https://www.google.com` ⇒ 200 |
| ⑤ | **集群内 DNS** | `<集群 DNS 节点>` + `nameserver-policy` | 删该节点 / 改 policy | `dscacheutil -q host -a name kubernetes.default.svc.cluster.local` 能解析 |

## 二、共享设置清单（谁动谁受影响）

| 设置 | 正确取值 | 影响 | 常见错误 ✗ |
| --- | --- | --- | --- |
| `interface-name` | **宿主物理网卡**（持有本机 LAN 地址、且设备名不是 `Meta/tun/utun/easytier*`） | ②③（underlay 出网） | 绑到 overlay 设备（= 绑到自己 ✗）；用 `ip route get 1.1.1.1` 发现网卡（被自家 TUN 劫持 ✗） |
| `dialer-proxy` | **只有出口节点**配（值为机场组） | ①③ 互斥 | 给入口节点也配 ⇒ 内网流量被送去机场 ✗ |
| `exit-nodes` | 入口节点指**家侧网关**；**同一个节点不要既当出口又指 exit** | ②（环） | 出口节点也写 `exit-nodes` ⇒ 504 环 ✗ |
| `proxy-networks` | 家侧公告**要开放给 overlay 的网段** | ② | 家侧镜像不是本 fork ⇒ 公告静默失效 ✗ |
| `udp` | 默认 `true`；**仅当所在网络封锁外网 UDP** 时设 `false` | ②（打洞能力） | 把它当成通用做法 ✗（这是环境事实，不是修复 ✗） |
| `disable-p2p` | **默认不要用** ✗ | ② | 用它去绕"规则回环" ⇒ 治标、自废性能 ✗（回环应修 underlay 绑定/规则 ✗） |
| 规则顺序 | overlay 自身（会合点/对端）**必须最先 DIRECT** | ②③ | 把公网/私有网段的兜底规则放到最前 ⇒ 自环/自锁 ✗ |
| 镜像版本 | **固定版本号**（`GW_TAG=vX.Y.Z-fa.NNNN`），永不用 `latest` ✗ | ② | 用 `latest` ⇒ 解析到旧/未验证 fork 构建，无法用 `mihomo -v` 回溯 ✗ |

## 二·补、域名规则的**作用域**陷阱（一条规则吃掉整个后缀 ✗）

`DOMAIN-SUFFIX,<公司域名>,<内网分组>` 会把**该后缀下所有名字**送进同一个节点。若这个后缀里既有
**内网服务**、又有**公网服务**（很常见：同一个域下 SPA 走公网入口、内部面板走内网地址），
公网那部分就会被一起塞进内网入口 ⇒ 家侧收到的是"去某个公网 IP"的请求，而它的转发拨号是
**直连**（不经它自己的机场）⇒ 超时 ✗，而你在家侧用浏览器/终端却能打开（那是走它自己的 TUN/机场）。

实测症状（本项目踩过 ✓）：`内网入口 = 入口节点` 时 `<公网服务>` 超时、`<内网服务>` 正常；
两侧 `dig` 解析结果一致（一个公网 IP、一个内网 IP）⇒ **不是分离解析**，也不是网络环路 ✗。

**通用写法**（按"真实 IP 落在哪"判定，而不是按名字判定）：

```yaml
dns:
  fake-ip-filter:            # ① 让这个后缀拿到**真实** IP（否则只能是 198.18.x 假地址 ✗）
    - "+.<公司域名>"
rules:
  # ② 用 IP 规则判定；**不要写 no-resolve** ✗ —— 写了就只对"纯 IP 连接"生效，
  #    域名连接永远匹配不到它，于是被迫用 DOMAIN-SUFFIX 一条打死整个后缀 ✗
  - IP-CIDR,<内网网段>,<内网分组>
  # ③ 只在"确定整段都是内网名字"时才保留 DOMAIN-SUFFIX
```

判据（两条都要 ✓）：`<内网服务>` 经分组 ⇒ 2xx/3xx；`<公网服务>` 的命中链路出现在**公网组**
（如 `match GeoIP(cn) using 🇨🇳 国内网站[DIRECT]`），而不是内网入口。

## 二·补二、全出口（可选）：让"家侧能到的一切"都能经隧道访问

默认形态是**子网代理**：出口只转发它 `proxy-networks` 里公告的网段。想让某个域名（哪怕解析到公网 IP、
且只有家侧能访问）也走家侧，需要两件事同时成立：

```yaml
# ① 家侧出口节点：公告"我能到任何地方"
proxy-networks: ["10.0.0.0/24", "10.0.5.0/24", "0.0.0.0/1", "128.0.0.0/1"]
```

```yaml
# ② 入口侧：按域名/网段选择走这个分组(规则引擎是域名感知的 ✓)
rules:
  - DOMAIN-SUFFIX,<只有家侧能访问的域名>,<内网分组>
```

为什么两件都要 ✓：只有 ①，规则不会把流量送进隧道；只有 ②，guest 里没有到公网 IP 的路由，
包会被**写回宿主 TUN**（hairpin）⇒ 宿主规则引擎再按 GeoIP 直连 ⇒ 超时 ✗（现象很像是"网络环路" ✗）。

**推荐仍写成两个半区 `0.0.0.0/1` + `128.0.0.0/1`**（保守写法），但"前缀 0 不生效"**不是已证实的结论**：

- 2026-10-22 生产现场曾观测：家侧公告 `0.0.0.0/0` 时离网访问目标 000/5s，改成两个 `/1` 后 302 ✓；
- 2026-10-24 **本组受控实验（本地两实例，均 `tun:false`）未能复现**（各 3 次样本，非统计结论）：
  `0/0` 单条 ⇒ 200×3；两个 `/1` ⇒ 200×3；A、B **同时**公告 `0/0` ⇒ 200×3
  （`tun:false` 只验证 guest 虚拟路由表，**不覆盖 OS/TUN 层双默认路由**）；
  重启出口、重启入口后各自复测仍 200×3；
- 代码侧：**在本层（`ProxyCidrTable`）**未见 prefix 0 特殊处理（`Ipv4Cidr::new(…, 0)` 成功、
  `contains` 全命中）；**路由安装（`peers/route/*`、`peer_ospf_route`）与 smoltcp 默认网关路径尚未逐一审计**。

⇒ 结论：`0/0` 在**上述受控实验（`tun:false`）**里可用；生产家侧是 `tun:true`，该路径**未覆盖**。
旧现场失败与当时环境（双默认路由、家侧 OOM 抖动、会话重建）耦合，未能复现。

> ⚠️ 本条只修正文档结论，**不改变生产口径**：全出口在 #1b（当前未定性）修复前仍不用于生产；
> 恢复全出口时，对 `0/0` 与两个 `/1` 做一次受控复验，判据：
> (a) 取一个**不在 `proxy-networks` 内**的静态公网 IP 直连测通；
> (b) 在**生产 `tun:true` 拓扑**下，出口、入口各重启一次后复测；
> (c) 内核运行 10 分钟后复测；
> (d) #1b 未修复前不用于生产。

安全性（实测 ✓）：guest 都是 `no_tun`，学到这些前缀 **不会改任何操作系统的路由表**
（家侧 `ip route | grep ^default` 仍 1 条）；只是允许对端把任意目标交给该出口 ✓。

代价与前置条件：
- 公网流量绕经家侧（延迟 + 家侧带宽），家侧若配 `dialer-proxy` 则等于用家侧机场替对端出网；
- **家侧必须稳定**：家侧容器被 OOM 杀掉会周期性断会话（见 `../examples/easytier/home/docker-compose.example.yml` 的 `mem_limit` 说明）；
- 判据：入口侧 `curl <只有家侧能访问的域名>` ⇒ 2xx；`dig` 拿到的是公网 IP 也算通过 ✓。

> ⚠️ **当前不达生产标准（2026-10-22 实测）**：`fa.1027` 上让家侧公告默认路由后，
> 经隧道转发**公网目标**会按连接泄漏内存：实测 12 个请求内 `836 MiB → 1.22 GiB → 1.68 GiB`，
> 约 **+28 MiB/s**，2 GiB 上限 45 秒内被 OOM 杀掉（家侧 `RestartCount` 连续上涨）✗。
> 只转发**内网目标**时稳态 ~765 MiB、数小时不增长 ✓。
> ⇒ 该能力在泄漏修好前**不要用于生产**；离网访问"只有出口能到的公网服务"暂时不具备。

## 二·补三、按域名走家侧出口（2026-10-25）

**场景**：某个内网域名（及子域）在**本机出口**被阻断（TCP 能连、TLS ClientHello 后被重置），
而**家侧出口可达**。L3 隧道只能按 CIDR 转发；把公网 IP 写死会随 DNS 漂移失效 ✗。
正确做法是按**域名**把这条流量交给**家侧的 mihomo**（它本来就在跑，不新增容器）：

```yaml
# 本机 overlay（et-config）
proxies:
  - name: et-home
    type: http
    server: <家侧 overlay IP>      # 稳定 overlay 地址，不是动态公网 IP
    port: 7890
    username: <user>
    password: <pass>
    dialer-proxy: et-lan           # 跳点走隧道；否则直连家侧 overlay IP 会被物理网卡绑定而超时
rules_prepend:
  - DOMAIN-SUFFIX,<内网域名后缀>,et-home
sniffer:                           # 最小集；见下方 A/B
  enable: true
  sniff:
    TLS:  {ports: [443]}
    HTTP: {ports: [80]}
```

**为什么必须有 sniffer**：本机系统解析器可能绕过 mihomo DNS（实测 `dig` 得 fake-ip、`getaddrinfo`
仍得真 IP）。TUN 连接只有真实 IP，`DOMAIN-SUFFIX` 匹配不到 ⇒ 必须从 TLS SNI / HTTP Host 还原域名。

**A/B 对照（2026-10-25）**：

| sniffer | TUN 直连 | mixed-port |
| --- | --- | --- |
| `enable: false` | **000** | 302 |
| `enable: true`（仅 443/80） | **302 (0.46s)** | 302 |

⇒ sniffer 是 **TUN 路径的必要条件**（关掉后域名规则匹配不到，回落到 GeoIP→DIRECT→000）。

**它是全局开关，不是"只对该后缀生效"**：`enable`（以及可选的 `parse-pure-ip`/`force-dns-mapping`）
影响**所有**经过 443/80 的 TLS/HTTP 连接（以及纯 IP 连接），会参与嗅探/规则匹配/DNS mapping；
`sniffer` 在 overlay 里是**整体替换**（不是合并）。

- **默认值必须知道**：mihomo 的 `RawSniffer` 默认就是
  `force-dns-mapping: true`、`parse-pure-ip: true`、`override-destination: true`
  （`config/config.go` 的 `DefaultRawConfig()`；`UnmarshalRawConfig` 在默认值上做 unmarshal）。
  ⇒ overlay 里**省略这些字段 = 继承 true**，不是关闭；当前运行态仍包含这三项。
- 上面的 A/B **只证明 `enable` 是必要条件**；它不证明那三项可选（省略 ≠ false）。
- 本次回归：baidu/google/github/<内网服务> 200、内网探针 403、13 组选择器不变 ✓
- 真要把全局副作用关小：必须在 overlay 里**显式**写 `parse-pure-ip: false` /
  `force-dns-mapping: false` / `override-destination: false` 并重新 apply 复测；
  预计关掉 `parse-pure-ip`/`override-destination` 会让真 IP 的 TUN 连接无法按域名匹配（回落到 IP 规则），
  本项目未采用。
- 更窄的替代方案：只把该后缀从 `fake-ip-filter` 移除、不用 sniffer —— 副作用更小，
  但**本机实测不可靠**（`dig` 得 fake-ip、`getaddrinfo` 仍得真 IP，TUN 流量按真 IP 匹配不到）。

**硬约束**：
- 家侧 mixed-port **必须开认证**（`authentication`）。家侧容器是 **host 网络**，7890 监听 `*`
  （LAN 可达；实测无凭据 **407/000**、带凭据 200/302；变更时 0 条 established、仅 mihomo 自身占用）
- 只对 TCP + TLS/HTTP 生效：**QUIC/UDP、ECH、不在上表端口内的流量（如 22/25/53 及未列端口）、
  纯 IP 直连都不覆盖**（QUIC 场景浏览器会回退 TCP，先失败一次）
- 依赖家侧 mihomo 与其凭据：家侧重启/认证错 ⇒ 该后缀黑洞，**无 fallback**
- 与 `#1b` 的"全出口 EasyTier 转发"无关（后者仍未定性、不用于生产）

**判据**：同一域名经 TUN 与 mixed-port 两路都 200/302；`/rules` 首条为该域名规则、旧
`→内网入口` 规则 0 条；家侧 `ss` 监听 7890 且无凭据 407；回归：内网目标 403/200、
`IP-CIDR,<overlay 网段>→DIRECT` 与 `IP-CIDR,<peer>/32→DIRECT` 仍命中、其它公网 200 不变。

**验收入口**：用 `et-config apply` 的 **mixed-port 探针**（`probes.conf`）；
`et-net-mode check easytier` 的第三探针走 **DIRECT**，办公网出口故障时仍 ✗ —— 不要拿它当本变更的通过判据。

## 三、改配置前的 5 条回归（每条都应有读数，不许"看着像通"）

```bash
SOCK=/var/run/clash-verge-service/users/501/verge-mihomo.sock
# ① 现场内网
curl -s -o /dev/null -m 10 -w 'site: %{http_code} %{time_total}s\n' --noproxy '*' https://<内网服务>/
# ② 离网入口（节点级，必须用内网目标 ✓）
curl -s -m 20 --unix-socket $SOCK "http://localhost/proxies/<入口节点>/delay?url=https%3A%2F%2F<内网服务>%2F&timeout=8000"
# ③ 家侧借网（在家侧执行）
ssh <家侧> "curl -s -o /dev/null -m 15 -w 'borrow: %{http_code} %{time_total}s\n' https://www.google.com/"
# ④ 本机机场
curl -s -o /dev/null -m 10 -x http://127.0.0.1:7897 -w 'airport: %{http_code}\n' https://www.google.com/
# ⑤ 镜像自证（家侧，必须命中 1 ✓）
ssh <家侧> "docker exec mihomo-gateway sh -c 'strings /proc/1/exe | grep -c \"forwarded dials go through dialer-proxy\"'"
```

## 四、纪律（血换的）

1. **改一项，跑上面五条** ✓；读数不符就回滚，不要"再调一版试试" ✗。
2. **判"节点通不通"** ⇒ `/proxies/<节点>/delay`（且探测目标用**内网服务** ✗ 不能用 gstatic ✗）；
   **判"能不能用"** ⇒ 终端 `curl`（不带 `-x`）+ 真浏览器 ✓。
3. **部署镜像必须固定版本** ✓，并用**二进制标记串**自证是不是本 fork（不看 tag ✗）。
4. **不要为绕过某个症状去关功能** ✗（P2P/UDP/TUN 都是承重能力）；症状出在规则/绑定就改规则/绑定 ✓。

5. **不妥协、不兼容旧代码** ✗：症状出在代码就改代码、出在配置就改配置；
   不允许"关功能/加例外/留兼容分支"去绕过（P2P/UDP/TUN 都是承重能力）。
6. **通用化** ✓：不写随环境变化的硬编码（公网 IP ✗）；允许稳定的本机属性（网卡名）与配置里本就有的值。

7. **测哪条路就让流量走那条路** ✓：测入口节点 ⇒ 先切分组 + **回读** + 用 `/connections` 的 `chains`
   或规则命中计数**证明**它真的走了该节点 ✓；**现场直连的 2xx 不能当作入口节点的证据** ✗。

8. **改配置前先问"它属于谁"** ✓：四层归属 ——
   | 层 | 是谁的 | 怎么对待 |
   |---|---|---|
   | 上游订阅 | 机场 `profiles/<uid>.yaml`（`type: remote`） | ✗ 不编辑；要改行为用 merge/override 覆盖 |
   | 内核默认 | mihomo 源码默认值（如 `config/config.go` 的 `nameserver`） | ✗ 不追改 |
   | **我们这层** | ① 对外发布：`../examples/easytier/local/client.example.yaml`（自包含）② 本机 overlay：`~/.local/state/clash-group/et-apply/overlay.yaml`（唯一手改 ✓）③ 本机订阅 profile | ✓ 唯一可改；改完用 `et-config apply` |
   | 生成物 | 候选 `config.yaml`、CVR `clash-verge.yaml`、`/Library/.../runtime/config.yaml`（内核 `-f`） | ✗ 禁止手改，只由 `et-config` / CVR 产生 |
   ```
   ① grep -rl "<值>" "$CVR_DIR"                看命中哪个文件（排除 .bak / backup）
   ② profiles.yaml 的 current                   命中机场 profile ⇒ 上游层 ⇒ 不改
   ③ grep -rn "<值>" mihomo/{config,constant}   命中 ⇒ 内核默认层 ⇒ 不改
   ```
   **对外分享只有一件东西** ✓：`../examples/easytier/local/client.example.yaml` —— 使用者「装 Clash + 导入这一份」，
   不改任何全局设置。本机两处是运行态，不对外；与模板**语义漂移即视为缺陷** ✗。
   ⚠️ 本机运行态 = **CVR 当前产物 + `overlay.yaml`（~1.3 KB）叠加** ✓，由 `et-config apply` 生成；
   不再维护整份快照 ✗（实测整份快照会因机场轮换域名而冻结旧数据 ✗）。叠加是幂等的 4 种操作
   （`+`/`=`/`prepend`/`remove`），删掉一条即消失 ✓，基座缺失则安全失败不落盘 ✓。
   例：`nameserver` 里的明文 `119.29.29.29` / `223.5.5.5` 属于机场订阅与内核默认 ⇒ **不动**；
   我们只守 **A/B/C 三类内网解析**、`et-*` 节点、`内网入口` 分组与规则头部。
