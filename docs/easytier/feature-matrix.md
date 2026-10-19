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
