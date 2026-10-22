# 功能 × 设置 交互矩阵（改任何一项前先查这张表 ✓）

> 目的：这套链路有**五个功能**，它们共用同一份配置，**任何一项设置都可能顺带弄坏另一项** ✗。
> 本表列出：每个功能**依赖什么**、**哪些设置会破坏它**、以及**一条能一分钟内证伪的回归判据**。

## 一、五个功能与依赖

| # | 功能 | 必须成立（依赖） | 破坏它的典型改法 ✗ | 回归判据（一条命令） |
| --- | --- | --- | --- | --- |
| ① | **现场直连内网**（公司/家 LAN） | 分组 `<内网分组> = DIRECT`；A 类域名在 `fake-ip-filter`；B 类域名在 `nameserver-policy` | 现场把分组切到入口节点；删 `fake-ip-filter` | 终端 `curl <内网服务>` ⇒ 2xx/3xx 且 **<100ms** |
| ② | **离网进内网**（经家侧） | 家侧**公告内网网段** + 家侧镜像=**本 fork 构建** + 分组切到入口节点 | 家侧用 `:latest`（会跑到官方构建 ⇒ 公告不生效 ✗）；家侧规则把 underlay 收进隧道 | `curl /proxies/<入口节点>/delay?url=<内网服务>` 有毫秒数 ✓ |
| ③ | **家侧借本机机场**（出口） | 家侧 `MATCH → et-mac`；本机出口节点 `dialer-proxy=<机场组>`；`purpose=4` 走 forward dialer | 给入口节点配 `dialer-proxy`；把 `interface-name` 指向 overlay 设备 | 家侧终端 `curl https://www.google.com` ⇒ 200；家侧日志含 `using et-mac` |
| ④ | **本机自己上网** | TUN 开、机场组可用 | 内网规则误吞公网；TUN 关 | 经 7897 `curl https://www.google.com` ⇒ 200 |
| ⑤ | **集群内 DNS** | `et-k8s-pdd` 节点 + `nameserver-policy` | 删该节点 / 改 policy | `dscacheutil -q host -a name kubernetes.default.svc.cluster.local` 能解析 |

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
| 镜像版本 | **固定版本号**（`GW_TAG=vX.Y.Z-fa.NNNN`），永不用 `latest` ✗ | ② | 用 `latest` ⇒ 悄悄跑到官方构建 ✗ |

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
proxy-networks: ["10.0.0.0/24", "10.0.5.0/24", "0.0.0.0/0"]
```

```yaml
# ② 入口侧：按域名/网段选择走这个分组(规则引擎是域名感知的 ✓)
rules:
  - DOMAIN-SUFFIX,<只有家侧能访问的域名>,<内网分组>
```

为什么两件都要 ✓：只有 ①，规则不会把流量送进隧道；只有 ②，guest 里没有到公网 IP 的路由，
包会被**写回宿主 TUN**（hairpin）⇒ 宿主规则引擎再按 GeoIP 直连 ⇒ 超时 ✗（现象很像是"网络环路" ✗）。

安全性（实测 ✓）：guest 都是 `no_tun`，学到 `0.0.0.0/0` **不会改任何操作系统的路由表**
（家侧 `ip route | grep ^default` 仍 1 条）；只是允许对端把任意目标交给该出口 ✓。

代价与前置条件：
- 公网流量绕经家侧（延迟 + 家侧带宽），家侧若配 `dialer-proxy` 则等于用家侧机场替对端出网；
- **家侧必须稳定**：家侧容器被 OOM 杀掉会周期性断会话（见 `home/docker-compose.yml` 的 `mem_limit` 说明）；
- 判据：入口侧 `curl <只有家侧能访问的域名>` ⇒ 2xx；`dig` 拿到的是公网 IP 也算通过 ✓。

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
   | **我们这层** | ① 对外发布：`client/mihomo-profile.template.yaml`（自包含）② 本机 overlay：`~/.local/state/clash-group/et-apply/overlay.yaml`（唯一手改 ✓）③ 本机订阅 profile | ✓ 唯一可改；改完用 `et-config apply` |
   | 生成物 | 候选 `config.yaml`、CVR `clash-verge.yaml`、`/Library/.../runtime/config.yaml`（内核 `-f`） | ✗ 禁止手改，只由 `et-config` / CVR 产生 |
   ```
   ① grep -rl "<值>" "$CVR_DIR"                看命中哪个文件（排除 .bak / backup）
   ② profiles.yaml 的 current                   命中机场 profile ⇒ 上游层 ⇒ 不改
   ③ grep -rn "<值>" mihomo/{config,constant}   命中 ⇒ 内核默认层 ⇒ 不改
   ```
   **对外分享只有一件东西** ✓：`client/mihomo-profile.template.yaml` —— 使用者「装 Clash + 导入这一份」，
   不改任何全局设置。本机两处是运行态，不对外；与模板**语义漂移即视为缺陷** ✗。
   ⚠️ 本机运行态 = **CVR 当前产物 + `overlay.yaml`（~1.3 KB）叠加** ✓，由 `et-config apply` 生成；
   不再维护整份快照 ✗（实测整份快照会因机场轮换域名而冻结旧数据 ✗）。叠加是幂等的 4 种操作
   （`+`/`=`/`prepend`/`remove`），删掉一条即消失 ✓，基座缺失则安全失败不落盘 ✓。
   例：`nameserver` 里的明文 `119.29.29.29` / `223.5.5.5` 属于机场订阅与内核默认 ⇒ **不动**；
   我们只守 **A/B/C 三类内网解析**、`et-*` 节点、`内网入口` 分组与规则头部。
