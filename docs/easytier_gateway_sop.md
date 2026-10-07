# EasyTier 内网网关 SOP（示例场景：从外网访问家里的内网）

> 一条主线：**会合点（公网）→ 家侧一键起网关 → 本机 CVR 加规则 → 三条命令验证**。
> 前置：① 一台**有公网 IP 的机器**（VPS 即可，1 核 512MB 够用；家侧没有公网时这是两端相遇的唯一前提）；
> ② 家侧服务器能拉 Docker 镜像、能 clone 本 fork 仓库。
> 详细原理与实测数据见 [easytier_gateway.md](easytier_gateway.md)，
> 验收标准见 [easytier_tun_spec.md](easytier_tun_spec.md)。

> **关于章节编号** ✓：正文章节 0–4 是操作路径 ✓；**§5 已随退役内容（用户态网关）删除** ✗ ——
> 但 §6 及其 20 个子节（§6.1–§6.20）被全文与其它文档交叉引用 ✓，改号会产生 20+ 处引用改动且零收益 ✗，
> 故**保持 §6 编号不变** ✓。查内容请用 `grep -n '^### 6.15 内嵌核排障：先打开 debug，再谈别的 ✓

**故障现象** ✓：家侧（或任一侧）用 **mihomo 内嵌的 easytier 出站**时 ✓，
节点进度只停在"连上会合点"✗，`et-mac` 之类的出口出站一律 `504` ✓，
日志错误是 `network is unreachable` ✗ 或 `context canceled` ✗；
**而同一台机器上跑原生 `easytier-core` 却能看到全部节点** ✓✓。

**第一步永远是打开 debug** ✓ —— fork 已把 guest 的事件流接到 mihomo 日志 ✓（`adapter/outbound/easytier.go` ✓）：
```yaml
log-level: debug        # ← 关键的一行 ✓；为 info 时这些行不会出现 ✗
```
```bash
docker logs --since 2m <容器> | grep -aE '\[EasyTier\]'
```

**判读** ✓：

| 看到 | 含义 |
| --- | --- |
| 只有 `listener_added` / `connecting` / `peer_connection_added` / `peer_added`（1 条） | 只连上会合点 ✓，**没有任何路由公告** ✗ → 路由表为空 ✓ |
| 出现 `route` / `peer_center` / 其他 peer 的 `peer_added` | 路由已学到 ✓，问题在别处 ✓ |
| `ErrorNoOverlayRoute` / `ErrorPathNotReady` → `ENETUNREACH` | **guest 自报"没有 overlay 路由"** ✓（`internal/engine/dataplane.go:493` ✓）|

**常用对照** ✓：`ET_CONSOLE_LOG_LEVEL` **无效** ✗（开关只在 mihomo 侧 ✓）；
`stun.easytier.cn` 只有空 TXT 是常态 ✓（另有 `stun.225284.xyz` 正常 ✓），**不要据此判定 STUN 坏了** ✗。

## 6\.'` 或本页搜索 ✓。

## 0. 会合点（只做一次，约 5 分钟）

**先说调研结论（2026-10-01，用 DoH 复核，绕开本机 DNS 劫持）**：

| 结论 | 依据 |
| --- | --- |
| **没有可用的官方/社区公共节点** | `public.easytier.cn`、`public.easytier.top` 均为 **NXDOMAIN**；官方文档里出现的地址（`tcp://1.2.3.4:11010` 之类）全是占位符 |
| 官方模型 = **自建** | 官方文档《搭建共享节点》教的就是"用你自己的公网机器给别人/给自己做共享节点"，并给了 fail2ban 防滥用配置 |
| ⇒ 你必须自备一台公网机器 | 1 核 512MB 云服务器足够；只跑一个容器、只占 11010 端口 |

**推荐用「私有模式」而不是公共共享节点**：只允许你自己的网络（同名 + 同密钥）连接，不会被陌生人白嫖、
不需要 fail2ban、中继流量也只属于你自己的网络。用仓库里的
`examples/easytier/rendezvous/docker-compose.yml`：

```text
[EasyTier](et-gateway) instance 8b7e138a-... running          # 实例已起（prewarm 生效）
[EasyTier](et-gateway) tun mode enabled on 10.144.0.2/24      # TUN 已建 = 网关就绪
[EasyTier](et-gateway) peer_added: PeerAdded(...)             # 已发现对端
```

> 出站是**懒启动**的：没人用它时 EasyTier 实例不会启动、TUN 也不会创建 ✗。家侧因此**必须**在配置里写
> `prewarm: true` ✓（内核构造完就把实例起起来 ✓）—— 这是家侧配置里唯一的"服务端专属"设置 ✓，
> 不再有任何环境变量或入口脚本参与（见 §6.12）✓。
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

> 多网卡无需改任何东西 ✓（镜像不做 NAT，NAT_INTERFACE 已删除 ✓；TUN 由内核建 ✓）；
> 唯一需要的是 `cap_add NET_ADMIN` + `/dev/net/tun` 与 `ip_forward=1` ✓（compose 已给 ✓）。
> 家侧不需要任何端口映射，也不需要公网 IP —— 它靠出站连会合点。

## 1. 本机 CVR 配置（约 5 分钟）

**先搞清用哪种并入方式——段名不一样：**

| 你的用法 | 用什么段名 | 说明 |
| --- | --- | --- |
| 当成一个**订阅 / 本地配置**，与机场订阅一起「**多订阅合并**」（本 fork 的 multi_merge） | **原生段名** `proxies:` / `proxy-groups:` / `rules:` | 合并流水线是 `MERGE_STEPS: proxies → proxy-providers → rule-providers → proxy-groups → rules`，只认这些；`rules` 会被**前置插入**（优先于订阅的 MATCH 兜底） |
| CVR 的**带类型扩展条目**（`type: proxies` / `groups` / `rules`） | 条目文件里写 `prepend:` / `append:` / `delete:` | 这是 CVR 真正实现的 prepend 机制：`enhance/seq.rs::use_seq` + `enhance/mod.rs:349-357` 分别作用于 rules / proxies / proxy-groups |
| 老式 Merge profile 里写 `prepend-proxies:` / `prepend-rules:` | **不要用** | 「键名自带类型前缀」的写法在上游 2.4.7 里只剩 `enhance/merge.rs` 的测试 fixture，没有实现；本 fork 的多订阅合并路径也不认 |

用原生段名的完整片段见 `examples/easytier/client/subscription.yaml`（含代理页分组）：

```yaml
proxies:
  - name: et-core
    type: easytier
    network-name: private-overlay
    network-secret: "<与家侧一致>"
    no-listener: true
    peers: ["tcp://<会合点公网IP>:11010"]
    udp: true
    # ipv4 留空 = 自动 DHCP ✓（手填容易与别的节点撞 ✗）
    # 不要写 state-dir：CVR 的 home dir 是 ~/Library/Application Support/…clash-verge-rev
    # interface-name 通常不要填 ✗（仅"连得上但底层不稳"且本机也跑 TUN 时才试 ✓）

proxy-groups:                     # 代理页的卡片来自分组；不加分组你以为没生效
  - name: 🏠 内网
    type: select
    proxies: [home-overlay, DIRECT]

rules:
  - IP-CIDR,10.144.0.0/24,🏠 内网   # overlay 网段本身也要走隧道
  - IP-CIDR,<家里网段>,🏠 内网      # 精确写！与本机所在网段重叠的那条不要写
  - SRC-IP-CIDR,<本机网段>,DIRECT   # 在家/在同网段时直连，避免绕隧道
```

**应用后怎么确认真的生效**（比在 GUI 里翻页面快）：

```bash
CFG=~/Library/Application\ Support/io.github.clash-verge-rev.clash-verge-rev/clash-verge.yaml
grep -c "home-overlay" "$CFG"     # 期望 ≥3：代理 1 + 分组 1 + 规则 2 以上
grep -A3 "🏠 内网" "$CFG" | head  # 期望看到 type: select 与 home-overlay
```

> 客户端**不需要换内核**：`easytier` 出站是 upstream 就有的，你现在的 alpha 内核即可。

## 2. 验证（约 1 分钟）

```bash
# ① TCP（换成一个你确定在跑的 LAN 服务，例如路由器 Web 界面 / NAS）
curl -sS -o /dev/null -w '%{http_code} %{size_download}B\n' \
    -x http://127.0.0.1:7897 http://<内网IP>:<端口>/

# ② UDP：对端没有回声服务时，用真实 DNS 查询验证（收到合法 DNS 响应即 PASS）
python3 docs/examples/easytier/tools/socks5-udp-probe.py \
    --socks 127.0.0.1:7897 --target <家里DNS服务器IP> --port 53 --dns-query example.com

# ②' UDP：对端跑了回声服务时（tools/udp-echo-server.py）
python3 docs/examples/easytier/tools/socks5-udp-probe.py \
    --socks 127.0.0.1:7897 --target <内网IP> --port 18001 --payload t --relay 127.0.0.1:7897

# ③ ICMP（需要客户端也有 TUN；mihomo 的 SOCKS 入站不代理 ICMP）
ping -c 3 <内网IP>
```

三条都过 = 打通完成。之后直接用家里的真实内网 IP 访问即可（与在家时完全一致）。

> 💡 **服务模式下怎么看到内核日志**：内核以 root 经特权服务运行时，它的 stdout 由服务持有（经 IPC 给 GUI），磁盘上读不到历史。
> 但内核自己的 **`/logs` 流式 API** 可以直接连（服务模式 socket 在 `/var/run/clash-verge-service/users/<uid>/verge-mihomo.sock`）：
>
> ```bash
> S=/var/run/clash-verge-service/users/501/verge-mihomo.sock
> curl -sN --unix-socket "$S" "http://localhost/logs?level=info" | grep --line-buffered -E "EasyTier|\[TCP\]|error"
> ```
>
> 切到 **Sidecar 模式**后，内核输出会写进 GUI 的 `logs/latest.log`，那是可以直接读的全量日志。

> ⚠️ **不要拿家侧的 external-controller（9090）当测试目标**：mihomo 的 API 有 DNS-rebinding 保护，
> 非本机来源的连接会被直接关掉（表现为"连上但不回包"）；而且用 HTTP 代理时加 `-H 'Host: …'` 会让
> mihomo **按 Host 头拨号**，可能打到本机同端口的别的服务（实测踩到：本机 Proxyman 监听 `*:9090`）。
> 测 LAN 上的普通服务（路由器 / NAS / SSH 端口）才是可靠判据。

> ⚠️ **测延迟要用 LAN 地址，公网地址必然报 error**：overlay 只承载家侧 `proxy-networks` 里发布的网段，
> 家侧**不是**互联网出口。所以拿 `gstatic.com`/`1.1.1.1` 这类公网 URL 测这一条会失败——那是预期行为，
> 不代表链路坏了。实测：LAN 目标 `10.0.0.1` 延迟 91ms、`10.0.1.181:8080` 延迟 79ms 都正常。
> 我们的分组是 `select`（手动），本来也不需要自动测速；要让按钮有意义就把 CVR 的延迟测试 URL 换成内网地址。

> 💡 **服务返回 403 / 自动跳转不等于不通**：很多自建服务（面板、媒体库）访问 `/` 会 403 + JS 跳 `/login`。
> 先看响应头与 `/login` 是否 200，再下结论（实测 `10.0.1.181:8080/` → 403 且跳 `/login`，而 `/login` → 200）。

## 3. 排错速查

| 现象 | 原因 | 处置 |
| --- | --- | --- |
| 家侧日志没有 `tun mode enabled` | 内核不是本 fork 构建的（上游会静默忽略 `tun: true`） | 用本仓库的 `docker compose up -d --build` 重建；别用官方镜像 |
| 两条验收命令任一不过 | 缺 `NET_ADMIN`/`/dev/net/tun`，或缺 `ip_forward` | 看**内核日志**：`tun mode enabled` 没出现就是没起来 ✓（通常是设备或能力缺失 ✓）；`ip_forward` 由 compose 的 `sysctls` 注入 ✓ |
| 客户端日志出现 `match MATCH using DIRECT` | 规则没生效或网段写错 | 检查 `prepend-rules` 与网段是否精确 |
| 小请求通、大流量卡住 | 走的是旧的历史方案或 no-TUN 路径 | 确认家侧 `tun: true` 生效 |
| 两端一直不相遇 | 会合点不可达/端口没放开 | 家侧 `docker compose exec mihomo wget -qO- http://<会合点>:11010` 探活；确认 11010 tcp+udp 都放行 |
| 构建卡在 `proxy.golang.org … i/o timeout` | 国内访问 Go 官方代理不通（Dockerfile 已默认换 goproxy.cn，若你本地改过或用了旧版本才会遇到） | 换源重试：`GOPROXY=https://goproxy.cn,direct docker compose build`；或走上方纯打包路径 |
| 构建报 `COPY bin/ … not found` | `dockerfile:` 写成了裸 `Dockerfile`，命中了仓库根那个打包用的 Dockerfile | 保持默认（`docs/examples/easytier/gateway/Dockerfile`）；覆盖时也要带目录 |
| `apk add` 卡住/超时 | Alpine 官方源在国内慢 | 默认已用中科大源；换源：`APK_MIRROR=mirrors.aliyun.com docker compose build` |
| `path is not subpath of home directory or SAFE_PATHS: /tmp/...` | EasyTier 的 `state-dir` 必须在**内核自己的 home dir** 内。终端跑隔离实例时是 `-d /tmp/et-client`（所以 `/tmp/et-client/state` 合法），但 **CVR 的 home dir 是 `~/Library/Application Support/io.github.clash-verge-rev.clash-verge-rev`** → 放进 CVR 的配置里写 `/tmp/...` 必然被拒 | 放进 CVR 的配置**直接删掉 `state-dir` 那一行**（默认 `easytier/<代理名>`，就在 home dir 内，合法）。另外别把隔离用的整份配置当 CVR profile 导入——会顶掉你的订阅，正确做法是 merge profile 里的 `prepend-proxies` / `prepend-rules` |
| 测 `10.144.0.2:9090` 连上但不回包 | 家侧 API 的 DNS-rebinding 保护（非本机来源直接关闭连接） | 别拿 API 当测试目标；用 LAN 上的普通服务，或 `--dns-query` 验 UDP |
| 用 `-x` 代理测某个 IP，结果被本机别的服务接走 | HTTP 代理场景下 `-H 'Host: …'` 会让 mihomo **按 Host 头拨号** | 用 SOCKS（`nc -X 5 -x`）并把 Host 头放进报文里；或干脆别改 Host |
| 规则里的某个家网段与本机当前网段重叠 | 会把你**本机局域网**的流量吸进隧道 | 在新网络里删掉重叠那条；或加 `SRC-IP-CIDR,<本机网段>,DIRECT` 兜底 |
| 浏览器访问不了，但命令行 `-x` 能通 | ① 内核根本没在跑（TUN 也就没了）；② 系统代理被别的工具（Proxyman 之类）占着，浏览器流量先到它，根本进不了 Clash | 先 `pgrep -fl mihomo` 与 `ifconfig utun*` 确认内核在跑；再 `scutil --proxy` 看系统代理指向谁——TUN 模式下**不需要**系统代理，把抢它的工具关掉即可 |
| 延迟测试报 error | 测速 URL 是公网地址，而 overlay 只承载家侧发布的网段（家侧不是互联网出口） | 用内网地址测，或忽略（`select` 分组不需要自动测速） |
| **内核被反复重启**（GUI 日志 `service restarted the core (N restarts so far); last exit: … SIGKILL`，PID 一直变、TUN 时有时无） | **不是看门狗杀卡死的内核，而是内核自己崩了**（macOS 的代码签名校验把进程杀掉）。排查证据链：<br>① 崩溃报告：`ls -t /Library/Logs/DiagnosticReports/verge-mihomo-alpha-*.ips`（实测 27 份），内容为 `EXC_BAD_ACCESS` + `signal: SIGKILL (Code Signature Invalid)` + `termination: {namespace: CODESIGNING, indicator: Invalid Page}`；<br>② 报告里 `usedImages[].size` 等于该二进制的 **`__TEXT` 段 vmsize**（`otool -l <core>` 核对；实测 39780352 = `0x25f0000`），可据此确认"服务模式跑的内核与 sidecar 是同一构建"——**所以不是内核版本旧**；<br>③ 对照实验定位触发面：测机场节点 `{"delay":79}` 安然无恙；给 easytier 出站一个**不可达目标**（`http://10.99.99.99/`）也照样崩 → **与目标无关，是 easytier 的 WASI（wazero JIT）路径一启动就崩**；<br>④ 同一二进制在**用户态**跑 easytier 完全正常 → 差别只在"服务模式（root，由特权服务 approval/重签名后启动）"。<br>**机理**：服务模式会把内核复制进 `…/clash-verge-service/` 并重签名，但**没有 `com.apple.security.cs.allow-jit`**；easytier 是 mihomo 里唯一使用 JIT（wazero 编译器）的路径，JIT 生成的可执行页被判非法页 → SIGKILL。**排除 TUN 路由、关系统代理、腾端口都不会有效**（它们不是病根）。 | **先搞清 macOS 上"不装服务能不能开 TUN"**（源码结论，别凭 UI 猜）：<br>• `core/runstate/health.rs`：`tun_capable() = self.is_admin \|\| self.service_usable()`；<br>• `crates/tauri-plugin-clash-verge-sysinfo/src/lib.rs:113`：`is_binary_admin()` 在非 Windows 上就是 **`libc::geteuid() == 0`**；<br>• UI 里**没有**提权入口：`proxy-control-switches.tsx` 的 `handleTunToggle` 在 `!isTunModeAvailable` 时只弹 `tunNeedsService`，remedy 只有"安装服务/重装/切服务模式"。<br>→ 所以"管理员模式"= **整个 App 以 root 运行**（`sudo "/Applications/Clash Verge.app/Contents/MacOS/clash-verge"`，先退出已运行实例；之后首页出现「管理员模式」徽章，`home.json` 的 `adminMode`）。<br>**两条路**：<br>① **应急（立刻可用）**：以 root 启动 CVR → TUN 可用 → 内核由 App 直接 spawn（**不走服务、不重签名**）→ 不再触发那次 SIGKILL。代价：GUI 以 root 运行，配置/日志/缓存随之以 root 写入；<br>② **正解（不依赖 JIT）**：`easytier-go` 内部是 `wazero.NewRuntime(ctx)`（= 编译器/JIT，`internal/engine/host.go:68`）且**没有注入口子** → 在 mihomo fork 里 `replace` 一个补丁版（换成 `NewRuntimeConfigInterpreter()` 或加可配项），重建 darwin 内核替换 sidecar → **服务模式（重签名）也不会崩**，GUI 仍以普通用户运行。代价：WASI 走解释器（控制面变慢；TUN 模式下数据面走原生路径，影响有限）。<br>（另：实测把 GUI 生成的配置复制到临时目录、`tun.enable=false`、用户态跑 sidecar 内核，经混合端口访问家里 = **200 / 200（88ms）、内核存活、崩溃报告零新增**，可作为"非服务路径不崩"的基线证据） |

## 4. 日常运维与回退

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
> 家侧不便提权时改用 `examples/easytier/gateway/alt-native/`（零特权 native 容器，无 ICMP、吞吐低一档）。

## 6. macOS 定案：不依赖 JIT 的内核（路线 B）

上面排错表里的"内核被反复重启"在 macOS 上有**两条独立的处决路径**，实测数据如下：

| 运行方式 | TUN | 是否碰 easytier | 结果 |
| --- | --- | --- | --- |
| 服务模式 | 开 | 碰 | **崩**：`.ips` = `EXC_BAD_ACCESS` + `SIGKILL (Code Signature Invalid)` + `CODESIGNING / Invalid Page` |
| 服务模式 | 开 | 没碰 | 稳定数分钟 |
| GUI 侧车（app 以 root 跑） | 开 | 没碰 | **~11 秒被杀**（无 `.ips`，`ReportCrash` 处理了尸体但不落盘） |
| 手动 `sudo` 起内核 | 开 | 没碰 | **~5–10 秒被杀**（`zsh: killed`，无 `.ips`） |
| 用户态实例 | 关 | 碰 | 稳定，家里服务 200 |

→ **① 非服务路径创建 utun 会被系统杀**（所以"sudo 跑 app"这条路走不通）；**② 服务路径下 easytier 的 JIT 会被签名处决**（`App` 本身是 `adhoc,runtime` 签名，服务 stage/重签名内核时同样带 hardened runtime 而没有 `allow-jit`）。两者交集只有：**服务模式 + 不依赖 JIT 的内核**。

**本 fork 的解法**：把 `easytier-go` 里创建 wazero runtime 的那行改为解释器运行时
（`wazero.NewRuntimeConfigInterpreter()`，见 `third_party/README.md` 与 `third_party/setup-easytier-interpreter.sh`），
这样 WASI 不再生成机器码，签名策略无从判非法。

构建与安装：

```bash
cd mihomo
third_party/setup-easytier-interpreter.sh            # 生成补丁版依赖（首次/升级依赖后）
go build -tags with_gvisor -trimpath -ldflags '-w -s \
  -X "github.com/metacubex/mihomo/constant.Version=Prerelease-Alpha"' \
  -o verge-mihomo-alpha-nojit .

# 装回服务（GUI：设置 → 代理控制 → 安装服务），让 CVR 正常 stage 一次内核，然后替换 staged 副本：
sudo find "/Library/Application Support/clash-verge-service" -name "verge-mihomo-alpha" -type f
sudo cp verge-mihomo-alpha-nojit "<上一步找到的路径>"
# GUI 里「重启 Clash 内核」
```

验收：`route -n get 10.0.1.181 | grep interface` → `utun1024`；
`curl -s -o /dev/null -w '%{http_code}\n' http://10.0.1.181:8080/login` → `200`；
`ls /Library/Logs/DiagnosticReports/verge-mihomo-alpha-*.ips | wc -l` → **不再增长**。

> 实测（解释器版、用户态、TUN 关）：`10.0.1.181:8080/login` → **200（0.58s，含实例懒启动）**、`10.0.0.1/` → **200（88ms）**、内核存活。
> 长期做法：把该内核作为 fork 的 sidecar（`pnpm prebuild --force` 或替换 `src-tauri/sidecar/verge-mihomo-alpha-aarch64-apple-darwin` 后重新打包），
> 这样服务将来重新 stage 时拿到的也是它。

### 6.1 解耦架构：用户态 overlay 网关 + SOCKS5（macOS 客户端实测定案）

> ⚠️ **历史方案（已被 §6.9 取代）**：自 2026-10-02 起生产形态是**单内核**（服务模式内核内置 `et-core`），用户态网关已退役。以下 §6.1 内容保留为排查记录与回滚参考。

第 6 节那张表说明：服务模式（root + TUN）下 easytier 的 WASI 实例启动**必然挂住**（随后被服务看门狗 SIGKILL），
而同一二进制、同一配置在**用户态**完全正常。于是把 overlay 拆出去：

```
CVR 内核（服务模式 + TUN）  --socks5-->  用户态网关（launchd 常驻）  --easytier/WASI-->  会合点 --> 家侧
   rules: 10.x → 🏠 内网                     mixed-port 127.0.0.1:17899
```

* CVR 侧：把 `easytier` 出站换成 `type: socks5, server: 127.0.0.1, port: 17899, udp: true`，规则与分组不变
* 特权只在 CVR 的服务模式下用（TUN 合法通道），WASI 只在用户态跑 → 两边各自都在"自然模式"里

实测（2026-10-02）：网关 `socks5 → 10.0.1.181:8080/login` → 200（首次 2.9s 含实例懒启动，之后 0.12s）；
`10.0.0.1/` → 200。服务模式内核在换成 socks5 出站后不再启动 WASI，也就不会再被秒杀。

> 为什么不用 SSH/`ssh_system` 出站替代：OpenSSH 的 `-D` 不支持 UDP ASSOCIATE，UDP/ICMP 会丢；
> 本方案里 SOCKS5 两端都是 mihomo，UDP 原样透传。

### 6.2 踩坑记录：`auto_check_update` 会把你换上的内核悄悄换掉

排查「服务模式一碰 easytier 就死」时，连续多轮验证都复现失败——原因不是修复无效，而是**服务实际运行的内核被 CVR 的自动更新换成了官方 alpha**：

* `verge.yaml` 的 `auto_check_update: true`（默认开启）→ CVR 会重新下载官方 alpha 到自己的缓存，**staging 时用的是它自己的那份**，不是 App 里你放的那颗；
* 判据（实测）：崩溃报告为 `SIGKILL (Code Signature Invalid)` + `termination: CODESIGNING / Invalid Page`，
  `path` 指向 `…/clash-verge-service/*/verge-mihomo-alpha`，且报告里的 `size` 等于**官方构建的 `__TEXT` 段大小**
  （官方 alpha = `0x25f0000` = 39780352；fork 自建的 fix1 = `0x25f4000`）。`size` 与 `__TEXT` 对不上，就说明跑的不是你那份。

**结论**：在 macOS 服务模式下做内核级验证前，先关掉 `auto_check_update`（改 `verge.yaml` 时先退出 CVR），
否则一直在测官方内核。判断「当前跑的是哪一份」的最快方法：用 easytier 激发出站 ——
出现新的 `CODESIGNING` 崩溃报告 = 官方 JIT 内核；没有任何崩溃报告 = 我们的解释器内核。

### 6.3 坑：overlay IP 冲突会造成「能连会合点但家里全黑洞」

实测：同一 `network-name`+`network-secret` 下，如果先后启动过多个客户端实例、而它们**用了同一个 `ipv4`**
（例：网关用 `10.144.0.3/24`，临时实验实例也用 `10.144.0.3/24`，各自还有独立 state-dir 与 node id），
家侧/中继会保留指向**已消失 peer** 的条目，表现为：会合点 TCP 可达 ✓、实例日志显示 running ✓，
但所有到家里的连接 **i/o timeout / 502**，TUN 路径 `000`。

修复：把客户端的 `ipv4` 换成一个没用过的地址（本例 `10.144.0.4/24`）、并清掉实例状态
（`rm -rf "<网关目录>/easytier"`）后重启网关 —— 立刻恢复（`TUN → 家里 200`）。

推论：**同一个 overlay 网络里，每个客户端节点都必须有唯一的 `ipv4`**；做实验时给临时实例留出独立地址段，
用完顺手清 state，别让残留身份污染家侧的路由表。

### 6.4 定案（macOS 服务模式）：第一次真正穿过 overlay 的连接会带走内核

在**确认服务确实运行我们自己的内核**之后（§6.2 的方法：激发出站、看有没有新的 `CODESIGNING` 崩溃报告 —— 没有就是我们的解释器内核），
用 fix1（构造即预热 + 可重试启动 + panic 兜底，提交 `d917dced`）做了完整 A/B：

| 观察 | 结论 |
| --- | --- |
| 带**已预热、running** 的内置 easytier 实例，内核稳定存活 **7 分 15 秒**，零重启 | 「实例 running」本身不致命 ✗ |
| 流量路径（TUN → 规则 → 内置 easytier）一发出**真实连接** → **+3.0s 内核 pid 变化** | 触发器是**第一次真正穿过 overlay 的连接** ✓ |
| API 测速路径（`/proxies/{name}/delay`）同样 +3.0s 被杀 | 与"delay 路径特有"无关 ✗（v3 时流量路径活着，只是因为那次连接**没能穿透**——实例还在启动中 ✓） |
| 崩溃报告**零新增**（解释器内核） | 不是签名/JIT ✗ |
| 无 panic、无 OOM（RSS 峰值 ~174MB 就被杀，而实验里 259MB 都活着）、服务 plist 无 `ResourceLimits`、五个 API 端点 0.2s 采样全绿 | 不是崩溃/OOM/资源上限/API 阻塞 ✗ |
| 同一内核在**用户态**（含 root、含 TUN 设备但无 auto-route）跑同样的连接 → 200 ✓ | 只在**服务模式（root + TUN + auto-route）**下发生 ✓ |
| 服务自身日志**编译期关闭**（`ENABLE_LOGGING = false`） | 它的决策无法从外部观测 ✗ |

**因此**：要在一个内核里同时拥有 TUN 与 easytier，需要在 CVR/服务侧定位那条"下令 SIGKILL"的路径（可能需要给 App/服务加日志重编，或 macOS 级追踪），
或者向 mihomo/CVR 上游提 issue（本节的表格就是最小复现集）。**在此之前，推荐 §6.1 的解耦架构**（用户态网关 + socks5 出站）——该方案已跑通并被 §6.9 的单内核形态取代：
代价只是多一个 launchd 常驻进程。

### 6.5 定案（第二轮）：App 是观察者，服务是执行者

打开 App 的调试日志后（**`app_log_level: debug`**：GUI「设置 → 高级 → 应用日志等级」，
或退出 CVR 后改 `verge.yaml` —— **注意运行中的 CVR 会在退出时重写该文件**，所以必须先退出再改 ✓），
复现一次真实连接，被杀瞬间的完整日志是：

```
DEBUG client connection error: hyper::Error(Shutdown, Os { code: 57, kind: NotConnected, "Socket is not connected" })
DEBUG Connecting to IPC at /var/run/clash-verge-service/service.sock
DEBUG GET /status -> 200 OK 372
WARN  [Service] service restarted the core (40 restarts so far); last exit: Killed by OOM killer or admin (SIGKILL) (code: None)
```

**App 侧没有任何"决定重启内核"的日志**（没有 `service owner status was unreadable`，没有 `service core stopped`）
—— 它只是发现**与服务的 IPC 连接断了**，然后从服务读到内核的死亡信息。而服务进程本身 **3 小时未重启**（不是它崩了）：

* **执行者 = 特权服务**（root、长寿命，唯一同时持有子进程与上报 `SIGKILL` 的组件）；
* 它的自身日志**编译期关闭**（`ENABLE_LOGGING = false`），IPC 又要求**签名请求**
  （普通用户连得上 socket，但会得到 `service protocol version does not match`）→ 其决策路径**不可外部观测**；
* **触发器 = 第一条真正穿过 overlay 的连接**（流量路径与 API 测速路径等价）；
* **范围 = 服务模式（root + TUN + auto-route）**；同一内核在用户态（含 root、含 TUN 设备但 `auto-route: false`）完全正常。

结论与完整证据表已整理成可提交的上游 issue 草稿：`docs/easytier_service_mode_kill_issue.md`。
在产品上，**§6.1 的解耦架构**曾是推荐形态（已连续稳定运行、TCP/UDP 均通、无需改动服务或系统）；**该结论已被 §6.9 取代** —— 现网为单内核 + 内置 easytier。

### 6.6 终局证据：是**直接 SIGKILL**，没有礼貌停

用内核自己的 `/logs` 流（`curl -N --unix-socket /var/run/clash-verge-service/users/501/verge-mihomo.sock 'http://localhost/logs?level=info'`，
无需 sudo）在激发前就开始采集，然后发一条穿过 overlay 的真实连接：

* 流本身是通的 ✓（采到大量正常 `[TCP] …` 行）；
* **完全没有** `received interrupt, shutting down`（fix2 新加的信号点）；
* **完全没有** `shutdown: cleaning up listeners` / `listeners closed` / `Mihomo shutting down`；
* 内核在 +3.0 秒被换成新 pid，崩溃报告仍冻结（解释器内核 → 无 `CODESIGNING` 报告）。

**结论**：执行者**直接用 `kill -9`**，没有先发 SIGINT —— 之前"SIGINT → 关停卡在 TUN 清理 → 超时升级"的假设**不成立** ✗。
而 SIGKILL **不可捕获、不可防御** ✗ → **内核侧已无修复空间** ✓：无论内核怎么写，都无法阻止这次死亡。

剩下的未知只有一个：**服务为什么决定直接 SIGKILL 一个正在正常服务的核心**。它的自身日志在编译期关闭
（`ENABLE_LOGGING = false`）✗，IPC 又要求签名请求 ✗ → 想拿到答案就必须**自行构建并安装一份带日志的服务**
（公开仓库可构建 ✓，但要以 root 替换系统守护进程 ✗，且服务与 App 之间的协议版本/签名必须匹配 ✗）。

因此：**§6.1 的解耦架构**是当前唯一稳妥的生产形态 ✓；「一个 mihomo」需要先在上游层面解决服务的这次判定 ✓。

### 6.7 服务侧的终止机制（来自它的源码）

`clash-verge-rev/clash-verge-service-ipc`（安装版 2.7.4 的符号与之一致，`strings` 可验证 ✓）里，
`src/core/process.rs` 的终止流程是**固定套路**：

```
SIGTERM → 10 × 100ms → 若仍存活 → SIGKILL
```

即**宽限期正好 1 秒**、且**无条件升级**。`stop_core()` 在 `src/core/server.rs` 有五个调用点
（owner 启动切换 `stop_previous_core`、owner 回滚、显式停止命令、服务关停、启动失败），
而 `manager.rs` 的 watchdog **不在其中** —— 它只 `child.wait()` 等内核退出然后重启（这就是
`service restarted the core (N)` 的来源）。

这也解释了「内核日志里什么都没有」：内核的日志是**同步写进宿主持有的管道**，
管道一满，信号处理里的第一行日志就阻塞，关停永远走不到。fork 现在的做法是**先启动兜底再写日志**，
并把兜底预算（700ms）压进宿主那 1 秒窗口 —— 于是内核会以「被正常停止」而不是「被杀」结束。

剩下的唯一盲区：这五个调用点里**是哪一个在跑**。服务的自身 `info!/warn!` 走 log4rs（编译期关闭 ✗），
只有内核 stdout 会落盘 → 要回答它必须自建一份开启日志的服务；而 App 会校验服务的 `service_sha256`，
所以那是一条**两个仓库都要动**的路。

### 6.8 融合上游 #3215：内置 easytier 成为家里访问的主路径

上游 v1.19.32 重写了 `adapter/outbound/easytier.go`（#3215：*restart EasyTier outbound after silent
overlay failure*），自带监督循环：`StateRunning` 健康检查、`readyCh` 就绪通道、1s→30s 退避重启，
并且**失败时会打日志**（`[EasyTier]… start failed: <原因>; retry in <退避>`）。

融合分支 **`fa/easytier-v1.19.32`**（合并提交 `81d10d76`）：以上游实现为基，
只重新挂上 fork-only 的部分 —— TUN 模式的五处位（选项 / 构造期校验 / 结构体 / `init` 里接包面 /
`shutdown` 里先关设备）与 `init()` 的 panic 兜底。

**丢弃**的两样东西值得记下来：

* **自研重试状态机** —— 它每次失败都重跑 `init()` + `shutdown()`，而 **WASI 运行时没有随 `shutdown()`
  释放**：内核 RSS 从 86MB 涨到 **1136MB**（≈2MB/s）。上游的 `loop()` 语义相同（同样是失败重启），
  但至少**有日志**，所以同类问题下次能直接看到原因。
* **构造期预 warm** —— 上游改为「调用方等 `readyCh` + 监督循环自动重启」，预 warm 不再需要。

**当前生产形态**（一个 mihomo，主+备）：

| 组件 | 角色 | overlay IP |
| --- | --- | --- |
| 内核内置 `et-core`（组 `🏠 内网` 首选） | **主路径**：家里内网 TCP/UDP 直通，无额外进程 | `10.144.0.9` |
| 用户态网关（`home-overlay` socks5，组内可切换） | **兜底**：内置路径出问题时可一键切回 | `10.144.0.4` |

注意：服务模式内核的终止机制仍是「SIGTERM → 1 秒 → SIGKILL」（§6.7），
而历史那批「一用 easytier 就被杀」的现场，**重装服务（重置 owner 世代）后不再复现** —— 见 §6.5 的
`owner recovery (TransportFailure)` 记录：那些 SIGKILL 更可能是被反复替换内核/重启搅乱的 **owner 会话状态**所致。

### 6.9 终态（2026-10-02）：一个 mihomo

生产形态最终收敛为**一个内核进程**：

* CVR 服务模式内核（**解释器版** easytier-go，见 §6.8）内置 `et-core`，
  组 `🏠 内网: [et-core, DIRECT]`，家里四条网段规则不变；
* **用户态网关已退役**（`launchctl bootout` + `disable gui/$UID/com.fa.home-overlay`，
  文件保留可回滚：`enable` + `bootstrap`）；
* 实测：家里 `10.0.1.181:8080` 连续 5 次 **200（0.12–0.14s）**、`10.0.0.1` 200、
  google 200；内核 **零重启**、崩溃报告**零新增**、稳态 RSS **~460MB**。

**JIT 优化实测后放弃**（记录以免重复投入）：

| 场景 | JIT | 解释器 |
| --- | --- | --- |
| 用户态（同配置同负载）| **202MB** | 265MB |
| 服务模式（真实）| 497MB | **464MB** |

即 JIT 只在用户态省 ~60MB，**服务模式无收益** ✗ —— 服务模式多出的 ~300MB 来自内核自身
（TUN + gVisor + 48 个代理），而非 WASI 实例；而 JIT 需要**开发者证书签名**（证书有效期、
App 更新/恢复路径、以及 §6.4 那类 `CODESIGNING` 崩溃风险）→ 故保留解释器版为生产形态。

### 6.10 家侧网关的镜像发布通道（不必再拉源码）

CI：`.github/workflows/mihomo-image.yml`（`workflow_dispatch`，多架构 `linux/amd64` + `linux/arm64`）
产物：`ghcr.io/iuin8/mihomo:latest` / `:sha-<short>` / `:<tag>`

> 镜像**就是纯 mihomo** ✓：没有入口脚本、没有环境变量、没有内置配置 ✓，行为全部来自挂载的 config ✓
> （家侧的 `prewarm: true` 写在 config 里 ✓；`NET_ADMIN`/TUN 设备/`ip_forward` 由 compose 提供 ✓）。
> NAT / 看门狗 / 存活看护 / 触发懒启动这四样自研逻辑均已删除，原因与实测见 §6.12–§6.13 ✓。

```bash
# 发布（把内核版本一并写进镜像里的 mihomo -v）
gh workflow run mihomo-image.yml -R iuin8/mihomo --ref fa/trunk \
  -f tag=v1.19.32-fa.1001 -f mihomo_version=v1.19.32-fa.1001
```

**用镜像起家侧**（不需要仓库源码、不需要本地 Go）：

```bash
mkdir -p ~/easytier-gateway && cd ~/easytier-gateway
# 只需从仓库取两个文件：docker-compose.yml + gateway.yaml
# 改 gateway.yaml 中三处「改这里」：network-secret / peers / proxy-networks
docker compose up -d
GW_TAG=v1.19.32-fa.1001 docker compose up -d      # 生产建议钉版本
```

**自编译**（改了内核代码、或拉不到 GHCR）：

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

**本轮容器侧修正**（都是生产级问题，不是风格偏好）：

| 项 | 之前 | 现在 |
| --- | --- | --- |
| 构建镜像 | `golang:1.25` ✗（落后于仓库/CI 的 1.26）| **`golang:1.26-alpine`** ✓ |
| 运行镜像 | `alpine:3.20` ✗（已 EOL）| **`alpine:3.24`** ✓ |
| 版本注入 | 无 ✗ → `mihomo -v` 显示源码占位值 `1.10.0` | 注入 `constant.Version` ✓ |
| easytier 镜像 | `easytier/easytier:latest` ✗（不可复现）| **钉 `v2.6.4`** ✓（升级流程写进注释 ✓）|
| 容器加固 | 无 | `no-new-privileges` ✓ + `mem_limit` ✓（家侧 mihomo 1g / native 512m）|
| 健康检查 | 无 | 家侧 mihomo 加 API 探活 ✓；native 方案主进程即服务，`restart: unless-stopped` 已覆盖 ✓ |
| API 绑定 | `external-controller: 0.0.0.0:9090` ✗ | **`127.0.0.1:9090`** ✓（容器内探活/入口脚本都用回环 ✓）|
| compose 默认路径 | 必须本地编译 ✗ | **默认拉 GHCR 镜像** ✓；自编译走 `docker-compose.build.yml` 覆盖 ✓ |
| 发布产物源 | — | 官方 Alpine/Go 源 ✓（**不把国内镜像源烘进发布镜像** ✓；国内本地构建仍可用 `APK_MIRROR=`) |

### 6.11 镜像通道验收结论 + 家侧切到 GHCR

> ℹ️ **关于包可见性（2026-10-02 实测更正）**：官方文档说得很清楚 —— 包**默认继承的是"权限"，不是"可见性"**
> （[Configuring a package's access control and visibility](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)）。
> 但**本仓库实测**：由工作流推上去的新包**就是 public** ✓（`ghcr.io/iuin8/easytier-gateway` 在删掉后
> 被 CI 重新创建，匿名 token 端点仍返回 **200** ✓），所以**通常不需要手动设置** ✓；若确实拿不到，
> 再到包设置页把可见性改成 Public ✓。
>
> **判断公开与否的正确方法**（别再用错判据 ✗）：看 **token 端点** ——
> `curl -o /dev/null -w '%{http_code}' 'https://ghcr.io/token?scope=repository:iuin8/<包名>:pull&service=ghcr.io'`
> → **200 = public** ✓ / **403 = private 或不存在** ✗。
> `GET /v2/<包名>/tags/list` 返回 **401 是 OCI 的正常挑战** ✓，**不代表包是私有的** ✗（这里曾经误判过一次 ✓）。
> 另一个常见误判：`gh api /user/packages/...` 返回 403/404 是因为 `gh` 的 OAuth token 默认没有
> `read:packages` 权限 ✓，同样不代表包的状态 ✗。
>
> 附注：**改包名 = 新建包** ✓ —— 旧包不会自动消失，需要在 Packages 页面手动删除 ✓。

**验收（2026-10-02，全部为匿名操作，不带任何凭据）**：

| 项 | 结果 |
| --- | --- |
| 多架构 manifest | `linux/amd64` ✓ + `linux/arm64` ✓（另有 docker 的 attestation manifest ✓）|
| 匿名 `docker pull` | ⚠️ **本节初稿写错了**：当时匿名拉取成功的是**旧包名** `ghcr.io/iuin8/easytier-gateway` ✓，而 `mihomo` / `mihomo-home-gateway` 这两个名字**从未被创建过** ✗ —— 原因是 CI 里 `images:` 用的是 `${{ github.repository_owner }}` 表达式，改名时没匹配上（详见 §6.14）|
| 包可见性 | **公开** ✓（匿名可拉 ✓）——注意 `gh` 的 OAuth token 默认**没有** `read:packages`，用 `gh api /user/packages/...` 查会 403 ✗；那只说明 token 范围不够，**不代表包是私有的** ✓ |
| 镜像内版本 | `Mihomo Meta v1.19.32-fa.1001 linux arm64`（版本注入生效 ✓）|
| 镜像内指纹 | 解释器补丁 **1** ✓ / 上游 #3215 监督 **1** ✓ / 旧泄漏重试 **0** ✓ / TUN 位 **3** ✓ |
| 镜像体积 | 106MB ✓（alpine 3.24 ✓，含 iptables/ip ✓）|

**家侧切到 GHCR（从"本地编译"改成"只用镜像"）**：

```bash
cd <家侧目录>                     # 里面应有 docker-compose.yml、gateway.yaml、state/
# 1) 用仓库里新版的两个文件覆盖（新版 docker-compose.yml 默认拉 GHCR 镜像，不再 build）
#    docker-compose.yml、gateway.yaml（后者把 API 收到 127.0.0.1）
# 2) 切到钉版本并重启（state/ 不要动：overlay 节点身份在里面）
GW_TAG=v1.19.32-fa.1001 docker compose pull mihomo
GW_TAG=v1.19.32-fa.1001 docker compose up -d
docker compose ps                 # 期望：healthy ✓
```

> 切换后如果出现"能连会合点但家里不通"，先查 §6.3：**同一 overlay 网络里每个客户端必须用唯一 ipv4** ✗。

### 6.12 家侧看门狗被内核监督取代，懒启动补丁被 `prewarm` 取代（2026-10-02）

**`WATCHDOG*` 已删除** ✓ —— 它当年要解决的问题（"easytier 网卡不见了"）本质就是"实例已经停了"，
而这正是 upstream #3215 的内核监督循环（`loop()` / `serve()`）现在负责的事：**秒级**检测 + 按退避重建
+ 带日志，而且**只重建实例、不断其它连接** ✓。旧 shell 看门狗相比它更差 ✗：最长要等 60s×3 才动作 ✗、
动作代价是 kill 整个内核 ✗、一小时 3 次之后**永久放弃**（网关坏到需人工介入 ✗）。它那份持久化计数文件
（`state/.watchdog-restarts`）也随之作废 ✓。

**"触发懒启动"已删除** ✓ —— 它靠"打一次注定失败的 delay 探测 + 轮询网卡"把实例拉起来 ✗
（脚本注释自己都写着"不能用返回码判断成败" ✓）。现在改为内核侧的显式选项 **`prewarm: true`**（fork-only ✓）：
构造完只调用**一次** `ensureStarted()`，之后的重建仍归上游 `loop()` 管 —— 不会重复 init/shutdown，
因此**没有**旧自研重试那种泄漏 ✗。实测（零流量）：`prewarm: false` 15 秒内 instance running 行数 **0** ✓；
`prewarm: true` 同条件**实例已 running 并发现对端** ✓✓。

**家侧现在的三层守护（各管一段，互不重叠）** ✓：

| 层 | 负责的故障 | 机制 |
| --- | --- | --- |
| 实例层 | 实例停止 / 静默失败 | 内核监督循环（#3215 ✓）退避重建 ✓ 带日志 ✓ |
| 进程层 | 内核退出 / 崩溃 | Docker `restart: unless-stopped` ✓（退避限流由 Docker 负责 ✓）|
| 挂死层 | 内核还在但 API 不通 | 入口脚本的存活看护（判据 = healthcheck **同一个**探针 ✓）→ TERM 内核 → Docker 重建 ✓ |

镜像入口因此只剩两件事（网关模式）：**NAT** ✓ + **存活看护** ✓；不传任何网关 env 时退化为
**纯 mihomo**（`exec` 内核，无 NAT、无看护 ✓）。

### 6.13 家侧守护的最终取舍（含被否决的选项，避免重复讨论）

**已执行：方案 A —— 删除存活看护** ✓。网关模式现在只剩 **SNAT 一件事** ✓，其余自愈全部来自"谁拥有状态谁负责恢复"：

| 层 | 故障 | 机制 |
| --- | --- | --- |
| 实例层 | 实例停止 / 静默失败 | 内核监督循环（#3215 ✓）秒级重建 + 退避 + 日志 ✓ |
| 进程层 | 内核退出 / 崩溃 | Docker `restart: unless-stopped` ✓（退避限流也在 Docker）|
| 挂死层 | 进程挂死但 API 不通（**罕见**）| **不自动处理** ✓：healthcheck 标 unhealthy ✓ + 一条命令 `docker compose restart mihomo` ✓ |

**被否决但记录在案的选项**（将来若要"无人值守自愈"直接照做，不必重新论证）：

* **B. 用标准件而非自研脚本**：加一个 `willfarrell/autoheal` 边车（监听 Docker 事件，按 healthcheck 重启 unhealthy 的容器 ✓），或改用 `docker stack`（Swarm 原生按 healthcheck 重建任务 ✓）。**不要**再往入口脚本里塞定时逻辑 ✗ —— 自研存活探针的典型事故是"负载高 → 探测超时 → 把正在工作的网关重启掉" ✗。
* **C. 消灭根因（真正的创意是"不发明东西"）**：把会导致挂死的原因逐个消掉 —— 日志已封顶（`max-size 10m / max-file 3` ✓，这正是我们排查 macOS 那晚遇到的形态：写日志阻塞 ✗）；状态目录只写极小文件 ✓；剩余可能的死锁属上游 bug ✗，应走 issue 而不是加壳 ✗。

**关于 NAT：它不是妥协，而是所选拓扑的后果；对 TUN 拓扑是必须的** ✓

* **为什么必须**：TUN 模式下家侧是**真 L3 路由器** ✓，转发出去的包带的是**客户端的 overlay 源地址**（`10.144.0.x`）✗，家里内网机器无法回包 ✗；`MASQUERADE` 把源改写成本机内网地址后回包才通 ✓。
* **怎么才能不用它**（三种，按代价排序）：
  1. **改成"被路由"**：`SKIP_NAT=1` + 在**需要访问的机器**上加一条静态路由
     `ip route add 10.144.0.0/24 via <本机内网IP>` ✓ —— 不用 NAT ✓，而且家里内网能看到**真实客户端 overlay IP** ✓（日志/ACL 更清楚 ✓）。代价：要碰那几台机器 ✗（**不需要改路由器** ✓，符合原始约束 ✓）。
  2. **待验证的免费午餐** ✗：EasyTier 在 native（`no_tun`）模式下**自己会做 SNAT** ✓（见 `home/alt-native/docker-compose.yml` 的说明 ✓），TUN 模式下是否同样如此**尚未实测** ✗ —— 值得在家侧做一次实验：设 `SKIP_NAT=1` ✓，从客户端 `curl`/`ping` 内网机器 ✓；若通 ✓ 则这一步 NAT 也可删掉 ✓✓。
  3. **改用 native 方案（方案 B）** ✓：没有 TUN、没有 iptables ✓，EasyTier 内部 SNAT ✓ —— 代价是**没有 ICMP**、吞吐 ~8.4 MB/s ✓。
* ~~**它不算妥协的原因**~~ → **已被本节下面的实验推翻** ✗：当时以为 SNAT 是拓扑必然 ✓，实测证明 EasyTier 自己就会 SNAT ✓，因此这条 NAT 连同入口脚本**整个删掉了** ✗；本段保留为决策过程记录 ✓。
* 现在真正剩下的只有两件事 ✓：`cap_add NET_ADMIN` + `/dev/net/tun`（建 TUN）与 `sysctls ip_forward=1`（转发），都在 compose 里 ✓。

**实测（2026-10-02，本机 Docker 全本地复现）：结论是"连这一行 SNAT 也是多余的"** ✓✓

实验设计（私有 overlay + 直连 peer，**不碰线上 overlay** ✓）：三个本地容器 ——
`expt-lan`（nginx，扮演家里内网主机 `10.99.0.5`）、`expt-gw`（本镜像，家侧网关角色，权限与 compose
**完全一致**：仅 `cap_add NET_ADMIN` + `/dev/net/tun` + `ip_forward=1`，无 privileged）、
`expt-client`（本镜像，客户端角色，经代理访问内网主机）。判据两条：客户端能否通过代理取到内网页面 ✓、
内网主机看到的来源地址是什么 ✓。

| 实验 | 家侧 iptables | 结果 | 内网主机看到的来源 |
| --- | --- | --- | --- |
| 1（对照）| MASQUERADE **1** 条 | 成功 ✓ | `10.99.0.2`（网关内网地址）|
| 2 | MASQUERADE **0** 条（`SKIP_NAT=1`）| **同样成功** ✓ | **`10.99.0.2`** ✓ |

→ **guest（EasyTier）在 TUN 模式下自己会 SNAT** ✓（与 native 模式"EasyTier 自行转发并做 SNAT"一致 ✓）。因此：

* 入口脚本里的 SNAT 是多余的 ✗ → **整个入口脚本已删除** ✓✓，镜像回归"纯 mihomo"（与镜像名一致 ✓，
  也能当通用代理/路由容器用 ✓）；
* **仍然必须**保留：`cap_add NET_ADMIN` + `/dev/net/tun`（建 TUN ✓）、`sysctls net.ipv4.ip_forward=1`（转发 ✓）；
* **一键部署不受影响** ✓✓：不需要任何手工路由 ✓ —— 上一版 §6.13 里"改成被路由 + 手工加静态路由"那条
  按"必须一键"的要求**不再需要** ✗，仅作为理论选项留档。

**逃生口**（仅当你的内网拓扑特殊、实测回包不通时才用；镜像里保留 `iptables` 就是为了这一条 ✓）：

```yaml
# 加在 docker-compose.yml 的 mihomo 服务里
    entrypoint: ["/bin/sh", "-c",
      "iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE; exec mihomo -d /root/.config/mihomo -f /root/.config/mihomo/config.yaml"]
```

### 6.14 镜像改名为什么前三次都没生效（教训）

**症状**：本地改了三遍镜像名（`easytier-gateway` → `mihomo-home-gateway` → `mihomo`），
但每次 CI 构建完推上去的**仍是旧名字** ✗；被删掉的旧包名还会"自己回来" ✗；而
`https://github.com/users/iuin8/packages/container/mihomo/settings` 始终 404 ✗。

**根因**：工作流里那行是

```yaml
images: ghcr.io/${{ github.repository_owner }}/easytier-gateway
```

用的是 **`${{ github.repository_owner }}` 表达式**，而不是字面量 `ghcr.io/iuin8/…` ✓ ——
而我的改名脚本与"核对脚本"都只匹配 `ghcr.io/iuin8/…` 与 `ghcr.io/<owner>/…` 两种字面写法 ✗，
**表达式形式两种都没匹配上** ✗。于是：改名没生效 ✗、核对也看不见 ✗（**核对与改名共用同一套盲区** ✗），
而每次 CI 一推就把刚被删掉的旧包**重新创建**出来 ✓（本仓库实测：新包是 **public** ✓，见 §6.11）。

**教训（已固化为习惯）**：
1. **核对要打印原文行，不要只看计数** ✓ —— 计数为 0 只说明"没有匹配到我的模式"，不等于"没有旧名字" ✗。
2. **改名脚本与核对脚本不要共用同一套模式** ✗ —— 否则盲区完全重合 ✓。
3. CI 里的镜像名**用字面量**，不要用表达式 ✓（少一层"看不出来"的间接 ✓）。
4. `workflow_dispatch` 与默认分支的关系（**2026-10-03 两次实测；第一条曾写错，此处为准** ✗→✓）：
   * 工作流文件**必须存在于默认分支** ✓，否则 `gh workflow run` 直接 404 ✗（`mihomo-image.yml` 最初只在分支上时就是这样 ✓）；
   * **执行的是被触发 `--ref` 上的定义** ✓，`--ref` 同时决定 checkout 哪份代码 ✓；
   * 默认分支那份**只影响"能否触发"与"显示名字"** ✗ —— 它**不决定执行内容** ✗。

   **判定方法（关键，别再判错 ✗）**：不要看**运行标题** ✗（那是默认分支的名字 ✗），要看**执行指纹** ✓ ——
   矩阵生成的 job 名 ✓、构建参数 ✓、推送的镜像 tag ✓。本例：job 名
   `build (linux, amd64, v3, amd64-v3, amd64, x86_64, x86_64)` 的 **7 段字段**只可能由分支版矩阵产生 ✓，
   `main` 上那份 2024 年的旧 `build.yml` 连矩阵条目都没有 ✗ → 所以当时跑的确实是**分支版** ✓。
   我上午的错是：**先看显示的名字、后看执行出来的东西** ✗ —— 顺序反了 ✓。

   **结论**：分支与 `main` 两份**不需要**逐字节一致 ✗（`main` 那份不参与执行 ✓）；但默认分支上留着
   2024 年的旧 CI 文件会**误导人** ✗ —— 更干净的做法是给 fork 一个**自己的默认分支（trunk）** ✓，
   让默认分支上的文件就是真正执行的那份 ✓（见 §6.15）。

### 6.15 建议：给 fork 一个自己的默认分支（trunk），tag 统一从它打

**现状（2026-10-03 实测）**：

| 分支 | 相对上游 main | 性质 |
| --- | --- | --- |
| `main`（当前默认）| 落后 **0** / 领先 **6** | 上游 main 的镜像 + 6 个 fork **CI 提交**（`build.yml` 是 **2024-08-31** 的旧版 ✗）|
| `Alpha` | 落后 26 / 领先 3607 | fork 的稳定线 |
| `fa/v1.19.32-fa.0` | 落后 26 / 领先 4443 | 上游 v1.19.32 的干净合并（基线）|
| `fa/easytier-v1.19.32` | 落后 26 / 领先 4500 | 基线 + easytier 全部工作（**当前最新** ✓）|
| `fa/easytier-tun` | 落后 26 / 领先 4463 | 早先迭代，**已被取代** ✗ |

**为什么值得做**（注意：理由不是"怕上游覆盖代码" ✗ —— 同步流程只产出 `fa/<tag>-fa.0`，不碰 `main` ✓；
而且执行用的是被触发分支那份 ✓）：
1. **没有唯一发布主线** ✗：现在有 4 条以上 `fa/*` 线，tag 打在哪条上不明确 ✓ → trunk 是 tag 的唯一落点 ✓；
2. `main` 上留着 **2024 年的旧 CI 文件** ✗，而默认分支的名字又会被显示 ✓ → 极易误导（今天就误判过一次 ✗）；
3. `main` 应当回到**纯上游镜像**：不带任何 fork 提交 ✓ → 见第 5 条；
4. 默认分支上的文件＝真正执行的那份 ✓（因为触发时用的是被触发 ref ✓，而 trunk 就是发布线 ✓）；
5. 惯例问题：`fa/main` 这个命名与"上游同步分支 `fa/<上游tag>-fa.0`"**语义撞车** ✗，
   建议叫 **`trunk`** ✓（或 `fa/trunk` ✓）。

**执行清单**（待确认后照做）：
1. `trunk` 从 `fa/easytier-v1.19.32` 建 ✓（当前最新且已验证的线 ✓；它的 CI 文件就是实际在跑的那份 ✓）；
2. 仓库设置里把默认分支切成 `trunk` ✓（`gh api -X PATCH /repos/iuin8/mihomo -f default_branch=trunk` ✓）；
3. 之后 **tag 统一从 `trunk` 打** ✓：`release`（`v*`）与 `prerelease`（`Prerelease-Alpha`）技能里的
   分支约定同步更新 ✓；
4. 合并习惯：feature 线（如 `fa/easytier-*`）→ 合进 `trunk` ✓ 再打 tag ✓；
5. `main` 保持"上游镜像、只读" ✓：不再往 `main` 提交 fork 内容 ✗（本次那 6 个 CI 提交随第 1 步一并

**已执行（2026-10-03）** ✓：`fa/trunk` 从 `fa/easytier-v1.19.32` 建立 ✓ 并已设为**仓库默认分支** ✓
（`gh repo view iuin8/mihomo --json defaultBranchRef` → `fa/trunk` ✓）。切完后默认可触发的工作流为
`Build` / `mihomo image` / `SSH System Release` / `Test` ✓ —— 与 `fa/trunk` 上的文件一一对应 ✓。

**因此新的约定**（tag 与发布都从 trunk 走）：
* 发布相关触发一律 `--ref fa/trunk` ✓（例如 `gh workflow run mihomo-image.yml -R iuin8/mihomo --ref fa/trunk …` ✓）；
* feature 线先合进 `fa/trunk` ✓ 再打 tag ✓；
* `main` 暂不动 ✓（保留现状；将来可选重置为纯上游镜像 ✓）。

**切换时发现并记录**：`main` 上有个上游的 `Delete.yml`（每周日清理 30 天前的工作流运行 ✓，带 `schedule:` ✗）
不在 `fa/trunk` 上 ✗ —— 定时工作流只从默认分支运行 ✓，所以它暂时不会跑 ✓；它是上游在 v1.19.32 之后
新增的文件 ✓，**会在下次上游同步时自然带进来** ✓，无需手工处理 ✓。
   落入 `trunk` 后，`main` 可重置为上游 ✓）。

### 6.16 收尾清单与两条新教训（2026-10-03）

**仓库侧现状（已核实）**：

| 项 | 状态 |
| --- | --- |
| 默认分支 / 发布主线 | `fa/trunk` ✓（本地=远端，工作区干净 ✓）|
| 已发布产物 vs `fa/trunk` | **只有 docs/技能差异** ✓ —— 镜像（构建自 `e6a479bf`）与 alpha（构建自 `e5287ff2`）的**代码与 trunk 一致** ✓ |
| 其它 fork 分支 | 15 条（`Alpha` + `fa/*`，含历史 v1.19.21~v1.19.32 各线）**全部 0 个独有提交** ✓ —— 技术上冗余 ✓，保留与否纯属历史记录偏好 ✓（发布 tag 独立存在，删分支不影响 tag ✓）|
| `main` | 仍有上游 `main` 的 26 个提交 + 6 个已过时的 fork CI 提交 ✓ —— **按设计如此** ✓（trunk 跟 tag 走，上游新提交由下次同步并入 ✓）|
| GHCR 包 | 只保留 `ghcr.io/iuin8/mihomo`（public ✓）；两个过渡名已不存在 ✓ |

**教训一：删文件之后必须扫"谁还在引用它"** ✗→✓
`entrypoint.sh` 删除后，`Dockerfile.prebuilt` 仍在 `COPY` 它 ✗ —— 那条构建路径**直接坏掉** ✓，靠的是事后全量 grep 才发现 ✓。
**做法**：删任何文件后立刻 `grep -rn <文件名> .` ✓（含 docs/ 与 .github/ ✓），把命中逐条判定"历史语境还是活引用" ✓。

**教训二：脚本纪律（这一轮栽了 4 次）** ✗→✓
1. **每个脚本开头 `set -e`** ✓ —— 有一次 python 语法错误后，后面的 `git commit` 照跑 ✓，产生了**名不副实的提交** ✓（内容只有一个文件、message 却描述了一堆 ✓）。
2. **中文内容一律用单引号 Python 字符串** ✓ —— 同一处嵌套引号错误在一天内犯了 **4 次** ✗；中文引号是 `"`，与 `'...'` 不冲突 ✓。
3. **不要用 `2>/dev/null` 吞掉核对命令的错误** ✗ —— `git checkout <tag> -- <file>` 静默失败过一次 ✓，当时还被当成了"已恢复上游" ✓。
4. **核对要打印原文行** ✓、**改名脚本与核对脚本不得共用同一套模式** ✗（否则盲区重合 ✓，详见 §6.14）。

### 6.17 命名通用化（2026-10-03）与家侧迁移

命名统一成业界通用词（原来的 "home" 只适用"回家"这一种用法 ✗ —— 别人可能只是要打通**某个**内网 ✓）：

| 旧 | 新 |
| --- | --- |
| `docs/examples/easytier/home/` | `docs/examples/easytier/gateway/` |
| `home-mihomo-tun.yaml` | `gateway.yaml` |
| `home-easytier-core.toml` | `alt-native/easytier-core.toml` |
| compose 项目名 `easytier-home-gateway` | **`easytier-gateway`** |
| `container_name: mihomo-home` | `mihomo-gateway` |
| 出站名 `et-home` | `et-gateway` |
| `hostname`/`instance-name: home-gw` | 占位符（**每台网关都要唯一** ✗）|
| 客户端分组 `🏠 家里` | **`🏠 内网`** |
| 网络名 `home` | **`private-overlay`** ✓（2026-10-03 二次通用化：**网络名不是密钥** ✓ 可直接写进模板；密钥仍留占位符 ✓）|
| `docs/easytier_home_gateway{,_sop}.md` | `docs/easytier_gateway{,_sop}.md` |

⚠️ **家侧迁移：必须先停旧栈** ✗ —— compose 的 `name:` 变了，新老项目名不同 ✓；若直接 `up -d`，
会变成**新旧两个实例同时在跑** ✗，而它们**共用同一个 `./state` 目录** → 两个 easytier 实例抢同一个节点身份 ✗
（就是 §6.3 那种互相破坏 ✓）。正确顺序（第一步要在**旧**目录、用**旧**文件执行 ✓）：

```bash
cd <家侧目录>
docker compose down                      # 用旧文件停掉旧项目名的栈 ✓
# 再把新的 docker-compose.yml 与 gateway.yaml 覆盖进来
docker compose up -d && docker compose ps # state/ 保留 → overlay 身份不变 ✓
```

### 6.18 内网域名怎么走隧道 · 以及怎么确认走的是 P2P（2026-10-03 实测）

**内网域名**：`rules` 里的 `IP-CIDR` 只认 IP ✗，按名字访问要补两件事之一：

* **少量名字 → 静态映射** ✓（零依赖，先用这个）：
  ```yaml
  hosts:
    nas.home.lan: 10.0.1.5
  ```
* **整个后缀 → 交给内网 DNS，并让查询也走隧道** ✓：
  ```yaml
  dns:
    enable: true
    respect-rules: true                     # 关键：DNS 查询按 rules 走 → 才会进 et-core ✓
    nameserver-policy:
      "+.home.lan": ["10.0.0.1#et-core"]    # 内网 DNS + 经 et-core 出站 ✓
  rules:
    - DOMAIN-SUFFIX,home.lan,🏠 内网        # 连接也要有规则，否则不走隧道 ✗
  ```
  依据：本 fork 的内核把这个出站注册成了 DNS 传输（`dns.RegisterEasyTierDnsClient` ✓），
  且出站自身也能解析名字（`resolveIPv4` → "overlay hostname … was not found" ✓）。
  overlay 内**节点名**另有专用开关：`accept-dns: true` + `tld-dns-zone: <你的域>.`（末尾带点 ✓），
  它只负责 overlay 自己的名字 ✓，不管家里路由器的域名 ✗。

#### 6.21.1 一次真实事故（2026-10-03）：两个值会把**整网**搞坏 ✗✓

把 K8s 网关部署上去后出现"**无限重启 + 连别的网关节点都不通**" ✓。复盘出**两个叠加原因** ✓ + 一条**流程教训** ✓：

| 原因 | 机制 | 正确做法 |
| --- | --- | --- |
| **静态地址取了低位** ✗（填了 `10.144.0.3`）| overlay 的 DHCP **从低位往后发** ✓（家侧固定 `.2` ✓）→ `.3` 很可能**正是客户端已持有的地址** ✓ → 两节点同址 → **互相驱逐** ✗ → 对端日志每秒刷 `peer_added/peer_removed` ✓，**整网路由抖动** ✗ | 静态地址取**高位** ✓（如 `.30` / `.100` ✓），远离 DHCP 池 ✓ |
| **`proxy-networks: ["0.0.0.0/0"]`** ✗✓ | 等于向整个 overlay **公告一条默认路由** ✓ → 把**别的节点的转发**也拉向本网关 ✗ → 即使没有地址冲突，也会"其它网关不通" ✓ | 只填**本网关真能到的网段** ✓；不确定就写 `[]` ✓（`tun: true` 或 `enable-exit-node: true` 时照样会起 ✓）|
| （清单缺口 ✗）缺 `enable-exit-node: true` | 客户端 `exit-nodes` 指向本节点时，落地流量**不被转发** ✗（家侧当初同样因此不通 ✓）| K8s 版必须与家侧**逐条对齐** ✓ |

**流程教训** ✗✓（比上面两条更通用 ✓）：**先取证，再删** ✗ —— 本次先 `delete deploy` ✓ 才去抓 `describe`/`logs` ✓，
结果证据全没了 ✗。正确顺序 ✓：
```bash
kubectl get pod -w                                   # 看它在重启 ✓
kubectl describe pod -l app=easytier-gateway | tail -30   # 重启原因/退出码 ✓  ← 删除前抓 ✗
kubectl logs -l app=easytier-gateway --previous --tail=50 # 上一次的日志 ✓   ← 删除前抓 ✗
kubectl -n default delete deploy easytier-gateway          # 最后才止血 ✓
```
（若已删除 ✓：`kubectl get events` 仍可能保留约 1 小时 ✓；节点上的 `/var/log/pods/` 也可能还有 ✓。）

**另一条同样重要的教训** ✗✓：**清理范围要覆盖"全部"对象** ✓ —— 本次只删了 `Deployment` ✓ 与 `PVC` ✓，
**漏了 `ConfigMap`** ✗ → 它带着最早的旧内容**活过每一轮** ✗，于是"改对了文件却仍报同一个错" ✓
（`Parse config error: yaml: line 1: did not find expected key` ✓）。而且 **`subPath` 挂载在 Pod 创建时固化** ✗ ——
CM 更新后**必须重建 Pod** ✓ 才会重新挂载 ✓。**干净的重部署口令** ✓：

```bash
kubectl -n <ns> delete -f k8s.yaml          # 一次删干净：cm / pvc / deploy 全在内 ✓
kubectl -n <ns> apply  -f k8s.yaml
kubectl -n <ns> rollout restart deploy/easytier-gateway   # 或直接 apply（Recreate 策略会自己重建 ✓）
# 验收：把集群里的内容与本地文件对比 ✓（这才是"生效物" ✓）
kubectl -n <ns> get cm easytier-gateway -o jsonpath='{.data.config\.yaml}' | md5sum
```


### 6.21 把网关放进 Kubernetes（2026-10-03）

清单：[`examples/easytier/gateway/k8s.yaml`](examples/easytier/gateway/k8s.yaml) ✓ —— 目标是把**集群网段与集群域名**接到同一个 overlay ✓。

**与 compose 版的对应** ✓：`cap_add` → `capabilities.add` ✓、`devices` → `hostPath /dev/net/tun` ✓、
`sysctls` → `securityContext.sysctls` ✓、配置文件 → ConfigMap（`subPath` ✓）、命名卷 → PVC ✓。

**五个 K8s 特有的坑** ✗（清单里逐条注释了 ✓）：

| 坑 | 后果 | 处置 |
| --- | --- | --- |
| `replicas > 1` 或 `RollingUpdate` ✗ | 同一 `ipv4`/`hostname` 两副本互踢 ✓ —— 实测表现就是**对端每秒刷 `peer_added/peer_removed`** ✗ | `replicas: 1` + `strategy: Recreate` ✓（硬要求 ✓）|
| 用 `httpGet` 探针 ✗ | kubelet 从 Pod 外探 ✓，而 API 只绑 `127.0.0.1:9090` ✓ → **永远不 Ready** ✗ | 用 **exec 探针**在容器内探 loopback ✓ |
| `restricted` 命名空间 ✗ | hostPath 与 NET_ADMIN 双双被拒 ✓ | 命名空间用 **baseline 或更宽松** ✓ |
| **Pod 里 `ip_forward = 0`** ✗✓（实测两种集群都有 ✓）| 网关是**路由器** ✓；集群若不给 Pod 开转发 ✓ → 要么 Pod 被 **admission 拒**（`SysctlForbidden` ✓，表现为"无限重启"但**容器从未启动** ✓）要么起来了不转发 ✗ | 二选一 ✓：**A** 每台节点 kubelet 加 `--allowed-unsafe-sysctls=net.ipv4.ip_forward` ✓（再把 pod 级 `sysctls` 写回 ✓）；**B** `hostNetwork: true` ✓ + `dnsPolicy: ClusterFirstWithHostNet` ✓（**清单默认走 B** ✓，端口随之变为节点级 ✓：API 改 `9095` ✓、`mixed-port: 0` ✓）|
| overlay 网段与集群 CIDR 重叠 ✗ | 部分 Pod 时通时不通 ✓，极难排查 ✓ | 先核对 `cluster-cidr` / `service-cluster-ip-range` ✓ |
| 客户端只用一条规则 ✗ | 家里与集群混在一台网关上 ✓ | **两个出站 + 规则配对** ✓（见 §6.20 ✓）|

**客户端侧** ✓（集群域名必须**经隧道**问 CoreDNS ✓）：
```yaml
dns:
  enable: true
  nameserver-policy:
    "+.cluster.local": ["<kube-dns ClusterIP>#et-k8s"]
rules:
  - DOMAIN-SUFFIX,cluster.local,et-k8s
  - IP-CIDR,<Pod CIDR>,et-k8s
  - IP-CIDR,<Service CIDR>,et-k8s
```

**`hostname` 的唯一性边界（别把它当成"全网唯一" ✗✓）**

| 层次 | 谁保证 | 会不会撞 ✗ |
| --- | --- | --- |
| **节点真实身份** | **`state-dir` 里的 `machine_id`** ✓（每个「主机 × 出站」首次运行生成一个**随机 UUID** 并持久化 ✓） | 正常情况下**不会** ✓ —— **除非**你把 state 目录**复制/共用**出去 ✗（镜像里烤进 state ✓、多容器挂同一个卷 ✓）→ 那才是**真正的撞车** ✓✓ |
| `instance-name` | mihomo 兜底 = **出站名** ✓ | 同一台机器内不撞 ✓（不同出站名不同 ✓）；**跨机器会撞** ✗（同名出站 ✓） |
| `hostname` | mihomo 兜底 = **`<宿主主机名>-<出站名>-<8位随机后缀>`** ✓✓ | **两个维度都不撞** ✓✓：后缀持久化在 `<state-dir>/host-id` ✓ → **每机 × 每出站各一份** ✓。宿主机名重名也没关系 ✓（`MacBook-Pro` ✓/克隆的 VM ✓/`localhost` ✓ 都被后缀区分开 ✓） |

**结论** ✓：兜底已经做到**跨机唯一** ✓✓（`<宿主>-<出站>-<随机后缀>` ✓）—— 后缀写在 `<state-dir>/host-id` ✓，
**由本 fork 拥有** ✓，刻意不碰 guest 自己的 `machine_id` 文件 ✗（避免两边争写 ✓）；名字任何一步失败都退化为不带后缀 ✓
（**命名问题绝不能让出站起不来** ✗✓）。清洗规则 ✓：只保留 `[A-Za-z0-9-]` ✓，其余字符（空格 ✓/点 ✓/中文 ✓）换成 `-` ✓，
长度上限 63 ✓ —— 实测宿主机名可能形如 `mbp-fa-2.local` ✓ → 会被规范化成 `mbp-fa-2-local` ✓。

**仍然不要手写 `hostname`** ✗✓：写死会让**所有导入同一订阅的机器同名** ✓ —— 那是把"同机冲突"换成"跨机冲突" ✗。
真要让不同机器**共用同一份 state 目录**（镜像烤 state ✓/多容器同卷 ✓）才需要显式设置 ✓，且必须逐机不同 ✗。

回归测试 `TestNewEasyTierDefaultsHostnamePerOutbound` ✓（红→绿 ✓）：同机两个出站不同 ✓、换 state 目录（=换机器）后不同 ✓、
同一 state 重复构造**稳定** ✓、后缀已落盘 ✓、显式值优先 ✓。

**⚠️ 多出站必须各自设置 `hostname`** ✗✓（2026-10-03 实测定位 + 已修 fork ✓）：
EasyTier 用 **(hostname, instance-name)** 标识节点 ✓；`instance-name` mihomo 一直有兜底（= 出站名 ✓），
而 **`hostname` 此前没有兜底** ✗ → 未设置时两个出站共用宿主默认值 ✓ → overlay 视作**同一个节点** ✓ →
症状：**同一时刻只有一个出站能用** ✗，且**哪个能用取决于注册顺序**（实测：重新激活 profile 后会反过来 ✓）。
处置 ✓：**升级到含 `FORK(easytier-identity)` 修复的内核** ✓ —— 该修复让 `hostname` 默认为 **`<宿主主机名>-<出站名>`** ✓✓，
**两个维度都唯一** ✓：跨机器唯一 ✓（同一份订阅发给多台机器不会撞 ✓）+ 同机每个出站唯一 ✓。
回归测试 `TestNewEasyTierDefaultsHostnamePerOutbound` ✓ 已红→绿验证 ✓（连"只用出站名"的中间版本都会失败 ✓）。

⚠️ **不要为了绕过它而在订阅里写死 `hostname`** ✗✓：写死的值会让**所有导入该订阅的机器**同名 ✓ ——
把一个"同机冲突"换成更糟的"**跨机冲突**" ✗（这正是本条的教训 ✓）。真要显式填写时，**每台机器必须不同** ✗。

**两个出站能共存吗** ✓（2026-10-03 实测 ✓）：**能** ✓ —— 同一个 mihomo 进程里跑两个 easytier 出站
（各自 `state-dir` 默认 `easytier/<出站名>` ✓、各自 `machine_id` ✓、各自 `exit-nodes` ✓），
实测两个实例**都** `instance … running` ✓、各自 `peer_added` ✓、零错误 ✓（复现命令：两个出站都加 `prewarm: true` ✓
—— **客户端出站是懒启动的** ✗，不加 prewarm 会看不到任何实例日志 ✓，容易误判成"没生效"✗）。

⚠️ 但**"只有一个生效"是另一个原因** ✗✓：`select` 型**分组是单选** ✓ —— **指向分组的规则同一时刻只走选中的那条** ✗。
所以"两条隧道都要能用"的正确写法是：**规则直接指向出站** ✓（`IP-CIDR,…,et-home` / `DOMAIN-SUFFIX,…,et-k8s` ✓），
**分组只用于手动切换** ✓。

**TUN 回环：客户端节点"看起来起了、其实根本没进网"** ✗✓（2026-10-07 实测根因 ✓，最隐蔽的一个 ✓）

**症状** ✓：客户端（CVR 服务模式 ✓）里 easytier 实例 `running` ✓、TUN 设备也建了 ✓、`ping <家侧 overlay 地址>` **还通** ✓ ——
但**家侧永远看不到这个节点** ✗，任何从家侧发起的访问都超时 ✓。

**为什么 ping 会"通"** ✗✓：easytier 的 TUN 拥有整个 overlay 网段（`10.144.0.6/24` ✓）→
本机 ping `10.144.0.2` 时包**直接进了 easytier 的 TUN** ✓，由 guest 自己应答 ✓ —— **这是假阳性，不能当作"在网里"的证据** ✗✓。

**真正的根因** ✓✓：**mihomo 自己的 TUN** 是 `auto-route: true` 且 **`route-exclude-address` 为空** ✗ →
它把**一切**流量吸进 mihomo ✓，**包括 overlay 自己的 `10.144.0.0/24` 与 easytier 去会合点的 underlay** ✗ →
easytier 的握手流量被自己的 TUN 抓回去 ✓ → **隧道永远建不起来** ✓。

**为什么以前能用** ✓：老配置里有 `tun.route-exclude-address: [10.0.0.0/8, 192.168.0.0/16, …]` ✓ → 顺带把 overlay 网段排除了 ✓。

**通用修法（推荐 ✓，且能进订阅 ✓）** ✓✓：**用规则代替"排除路由"** ✓ —— TUN 抓走的流量**会先经过规则** ✓，
所以只要把这两条放在**最前面** ✓：

```yaml
rules:
  - IP-CIDR,<会合点 IP>/32,DIRECT,no-resolve        # underlay 必须直连 ✗ 否则被自己的 TUN 抓走 ✓
  - IP-CIDR,<overlay 网段>/24,DIRECT,no-resolve     # overlay 自己的网段交给 easytier 的 TUN ✓
  # …其余规则（家侧网段 → 出口出站 等）
```

**为什么不用 `route-exclude-address`** ✗✓：CVR 的 `constants.rs` 把它列为 **`tun::GUI_KEYS`** ✓
（App 对话框保存的字段 ✓）→ **写进 profile 也会被 App 自身设置覆盖** ✗ → **不适合做"分享给别人的订阅"** ✗✓；
而**规则是订阅的一部分** ✓✓，所以通用方案必须走规则 ✓。

**判据（唯一可信的 ✓）** ✓✓：**旁观者节点表里必须出现客户端的 overlay 地址** ✓ ——
`ping` ✗、`实例 running` ✗、`TUN 已建` ✗ 都不算证据 ✓✓。

**诊断法：架一个"旁观者节点"读整张网** ✓✓（2026-10-03 实战有效 ✓）

排查"某个出站/节点不通"时 ✓，最快的一步不是翻日志 ✗，而是**直接从 overlay 里看 peer 列表** ✓ —— 一条命令列出
**每个节点的 overlay 地址、hostname、链路方式与代价** ✓✓，重复地址、缺 hostname、节点缺失都会立刻现形 ✓：

```bash
# 用官方镜像起一个一次性观察点（不占 overlay 地址池的常用段，命名独立 ✓）
docker run -d --name et-observer --entrypoint sh easytier/easytier -c \
  "easytier-core --network-name <网络名> --network-secret <密钥> \
     --peers tcp://<会合点>:11010 --hostname observer-check --no-tun true > /tmp/o.log 2>&1 & sleep 900"
sleep 30
docker exec et-observer easytier-cli peer        # ← 整张网的节点表 ✓
docker exec et-observer sh -c 'tail -5 /tmp/o.log'   # 起不来时看这里（✗ 别把输出重定向进容器却去看 docker logs ✓）
docker rm -f et-observer                          # ⚠️ 看完就删 ✓，别在别人的网络里留节点 ✗
```

**实战收获** ✓（2026-10-03 一次就定位 ✓）：看到客户端两个实例分别拿到 `10.144.0.4` / `10.144.0.5` ✓（**地址不冲突** ✓）、
两台网关 `ssy-fa-gw`/`k8s-gw` 都在线 ✓ —— 于是"网络侧健康 ✓、问题在客户端侧 ✗"这个判断是**看出来的** ✓，不是猜的 ✓。

**怎么取集群的网段** ✓（2026-10-03 补；下面的命令都是标准 `kubectl` ✓，我这边没有集群 ✗ 未逐条跑过 ✓）：

只需要**两个** ✓：**Pod CIDR**（Pod 地址，headless/直连 Pod 用 ✓）与 **Service CIDR**（ClusterIP，所有 Service 用 ✓）。

```bash
# ① Pod CIDR：权威来源是 controller-manager 的 --cluster-cidr（kubeadm 类 ✓）
kubectl -n kube-system get pod -l component=kube-controller-manager -o yaml | grep -oE '\-\-cluster-cidr=[^ "]+'
# 也可以从节点分配上看（每个节点一块 ✓，合起来就是集群范围 ✓）
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.podCIDR}{"\n"}{end}'

# ② Service CIDR：看 apiserver 的 --service-cluster-ip-range，或用两个必然存在的 Service 反推 ✓
kubectl -n kube-system get pod -l component=kube-apiserver -o yaml | grep -oE '\-\-service-cluster-ip-range=[^ "]+'
kubectl get svc kubernetes -o jsonpath='{.spec.clusterIP}'; echo
kubectl -n kube-system get svc kube-dns -o jsonpath='{.spec.clusterIP}'; echo

# ③ 实测交叉验证（最可靠的一步 ✓：列真实地址，确认它们都落在你写的网段里 ✓）
kubectl get pods -A -o wide | awk '{print $7}' | grep -E '^[0-9]' | sort -u | head
kubectl get svc -A -o jsonpath='{range .items[*]}{.spec.clusterIP}{"\n"}{end}' | sort -u | head

# ④ 集群域名（默认 cluster.local，可被改 ✗ ✓）
kubectl -n kube-system get cm coredns -o yaml | grep -m1 -oE 'kubernetes [a-z0-9.-]+'
```

**发行版差异** ✗✓（别照抄默认值 ✓）：kubeadm 常见 `10.244.0.0/16` + `10.96.0.0/12` ✓；k3s 默认 `10.42.0.0/16` + `10.43.0.0/16` ✓；
**EKS 默认 CNI 的 Pod 地址直接来自 VPC 网段** ✗（没有独立 Pod CIDR ✓）→ 那时"Pod 网段"就是 VPC CIDR ✓，**务必做下面的重叠检查** ✗。

**重叠检查** ✗✓（overlay 与家侧网段都**不能**与集群网段重叠 ✓，否则规则无法区分 ✓）：
```bash
python3 - <<'EOF'
import ipaddress
overlay = ipaddress.ip_network('10.144.0.0/24')   # ← overlay
home    = ipaddress.ip_network('10.0.0.0/24')     # ← 家侧内网
for c in ['10.244.0.0/16', '10.96.0.0/12']:       # ← 集群的两个
    n = ipaddress.ip_network(c)
    print(c, 'overlay 重叠=', overlay.overlaps(n), '家侧 重叠=', home.overlaps(n))
EOF
```

**最后一句实用建议** ✓✓：客户端**配了 `exit-nodes`** 时 ✓，非 overlay 目标**全部**交给该网关 ✓（含节点 IP ✓、LB IP ✓、公网 ✓）→
所以 `proxy-networks` 只填这两个网段就够 ✓（它主要服务**转发侧** ✓）；**真正决定"去哪台网关"的仍是客户端的 `exit-nodes`** ✓。

**`proxy-networks` 只能填 CIDR，不能填域名** ✗（2026-10-03 源码 + 实测定案 ✓）：

* **类型就是 CIDR** ✓：`easytier-core/src/config/toml.rs` 里是 `ProxyNetworkConfig { cidr: cidr::Ipv4Cidr, mapped_cidr: Option<Ipv4Cidr>, allow: Option<Vec<String>> }` ✓
  —— 域名无法反序列化进 `Ipv4Cidr` ✓。（mihomo 侧只暴露了最常用的 `proxy-networks` 字符串列表 ✓，`mapped-cidr` / `allow` 未暴露 ✗。）
* **填域名会"响亮地失败"** ✓（不是静默 ✗）：实测实例**起不来** ✓，guest 报
  `start failed: create EasyTier instance: Error: failed to parse config TOML from WASI` ✓（内核按监督策略重试 ✓，日志里能直接看到 ✓）。
* **为什么** ✓：`proxy-networks` 是**路由广告**（"这些 **IP 前缀**在我这边" ✓）—— overlay 是 L3 ✓，没有"某个域名归我"这种路由 ✗；**DNS 是另一层** ✓。
* **通配符 `*` 也不行** ✗（2026-10-03 实测 ✓）：实例**起不来** ✓，报同一个 `failed to parse config TOML from WASI` ✓。
  **等价写法是 `0.0.0.0/0`** ✓（能过 ✓，实测 ✓ —— 它就是 CIDR 意义上的"全都要" ✓）。
* **多网关时不要用 `0.0.0.0/0`** ✓，但理由要**说准** ✓（2026-10-03 源码复核，修正了此前一句过强的说法 ✗✓）：
  **客户端的落地节点由 `exit-nodes` 决定** ✓✓，**不受** `proxy-networks` 影响 ✓ —— `peer_manager.rs::get_msg_dst_peer_ipv4` 的顺序是
  ① 目标是**某个 peer 自己的 overlay 地址**（`ipv4_peer_id_map` **精确查表** ✓，不含 CIDR ✗）→ 直投 ✓；
  ② 否则若目标**不属于本网络** → **按 `exit-nodes` 列表顺序**取第一个存在的 peer ✓（命中即 `break` ✓）；
  ③ 再否则本机兜底 ✓。**整条路径里没有 CIDR 路由表** ✗ → 所以"`0.0.0.0/0` 会盖过出口节点"**不成立** ✗。
  那 `0.0.0.0/0` 的问题在哪 ✓：它**污染 overlay 的路由表** ✓ —— 影响的是**转发侧**（relay / 未配 exit 的节点 / 网关之间 ✓）：
  两台都公告全网段时，这些"不是自己拨号"的路径只能按路由代价二选一 ✗ → 表现为**时对时错** ✓，比"完全不通"更难查 ✓。
  **所以仍建议** ✓：每台只公告**自己真正能到的网段** ✓✓ —— 精确、可解释、与 `exit-nodes` 各管一段 ✓。
  **规则分清两层** ✓：mihomo 的 rule 决定**拨哪个出站** ✓；进了隧道之后由 **overlay 的路由表**决定落地 ✓ →
  每台网关只公告**自己真正能到的网段** ✓，才能保证"拨 et-k8s 就落在集群网关" ✓✓。
  （`0.0.0.0/0` 真正对应的场景是"这台就是大家的公网出口" ✓ —— 那个用 `enable-exit-node` + 客户端的 `exit-nodes` 表达更准确 ✓，不必写进 `proxy-networks` ✓。）

* **所以域名这样走** ✓✓：客户端 `dns.nameserver-policy` 把该域名交给**集群内的 DNS** ✓（`"+.cluster.local": ["<CoreDNS ClusterIP>#et-k8s"]` ✓）+ 一条 `DOMAIN-SUFFIX,cluster.local,et-k8s` 规则 ✓；
  **CIDR（Pod CIDR + Service CIDR）放在 `proxy-networks`** ✓ —— 域名解析出来的就是这两个网段里的地址 ✓，两层配合才通 ✓（`cluster.local` 的普通 Service 落在 Service CIDR ✓、headless/Pod 落在 Pod CIDR ✓，所以两个都要写 ✓）。

**⚠️ `-t` 通过 ≠ guest 接受配置** ✗✓（2026-10-03 实测 ✓）：`mihomo -t` 只校验 **mihomo 自己**的配置 ✓；
guest 侧的 TOML 由**另一层**校验 ✓ —— 实测 `-t` 全绿 ✓ 而 guest 报
`failed to parse config TOML from WASI` ✓（本次原因是 `peers` 里残留了 `<改这里…>` 占位符 ✓ → `invalid domain character` ✓）。
**所以新配置必须再跑一次真实容器** ✓（用与 K8s 相同的挂载 ✓），并断言日志里出现这两行 ✓：
`instance … running` ✓ 与（网关角色）`tun mode enabled on <你的 ipv4>` ✓。

**本次已做的校验** ✓（2026-10-03 实测 ✓）：清单解析 ✓、不变量断言（replicas/strategy/hostNetwork/dnsPolicy/探针/挂载 ✓）、
**用同一镜像在容器内跑 `-t` = successful** ✓（连同 `state-dir` 的路径安全检查一起验掉 ✓）、`kubectl apply --dry-run=client` ✓。
**未做** ✗（本机没有集群 ✓）：真实调度、TUN 设备、CNI 对源地址的处理 ✓ —— 上线后按"客户端能否访问 `kubernetes.default.svc` 与某个 Pod"验收 ✓。

### 6.20 多个出口怎么配才不割裂（2026-10-03 源码定案）

**源码**（`open-source/easytier` → `easytier-core/src/peers/peer_manager.rs:2724 get_msg_dst_peer_ipv4` ✓）：
目标不在 overlay 网段时，按 **`exit_nodes` 的书写顺序**取**第一个"存在"的节点** ✓（找到即 `break` ✓）——
**不是**按延迟 ✗、**也不是**自动匹配"承载该目标的节点" ✗。所以多出口的语义 = **一条故障转移列表** ✓。

**由此产生的结构性风险** ✓：`exit_nodes` 是**实例级全局**设置 ✗，而 **rule 是逐目标**的 ✓ —— 一旦网络里有多个出口 ✓，
"规则指向 A 节点、出口却是 B 节点"的组合**完全可能** ✗，那正是"DNS 走一条路、请求走另一条"那类割裂的翻版 ✓。

**唯一自然的解法** ✓✓：**一个出口一个出站** ✓

```yaml
proxies:
  - {name: et-home, type: easytier, …, exit-nodes: ["10.144.0.2"]}   # 家侧出口 ✓
  - {name: et-vps,  type: easytier, …, exit-nodes: ["<另一出口 IP>"]} # 另一出口 ✓
rules:
  - DOMAIN-SUFFIX,shushangyun.com,et-home   # 端口映射型服务必须家侧出口 ✓
  - DOMAIN-SUFFIX,example.com,et-vps        # 其余公网走另一出口 ✓
```

→ **规则同时决定"谁承载"和"谁出口"** ✓ —— 割裂在**配置层**被排除 ✓，而不是靠运行时巧合 ✓。

**顺带两条已验证的事实** ✓（避免以后重复推理 ✓）：
* 家侧 LAN 网段**不依赖出口节点** ✓：靠 `proxy-networks` 公告 + foreign network 转发走通 ✓（实测：**还没配 exit-nodes 时** `10.0.0.1` / `10.0.16.1` 就已 200 ✓✓）。
* `exit_nodes` 只能填 **IP** ✓（`Vec<IpAddr>` ✓）—— **没有 `auto`/`any`** ✗。

### 6.19 端口映射型内网服务：域名解析出公网 IP，但仍需走隧道（2026-10-03 实测）

**现场**：内网服务经**端口映射**发布到公网 → 域名解析结果是**公网 IP** ✓（`dig` 看不到内网地址 ✓），
但内容**只有到家侧才访问得到** ✓（公网映射不直接对外服务 ✓）。

**实测（经真实 overlay ✓）**：
```
[DNS] siluhuilian-admin-test.shushangyun.com --> [183.62.24.58] A     ← 公网地址 ✓
[TCP] dial home … error: dial tcp4 183.62.24.58:12880 …               ← 隧道里没有到公网的路 ✗
curl -x 127.0.0.1:7899 → HTTP 502
```

**此时要的是"落地在家侧"，而不是"解析出内网地址"** ✓：

* 家侧 `gateway.yaml`：`enable-exit-node: true` ✓（允许它替客户端把流量落地出去 ✓）
* 客户端：`exit-nodes: ["<家侧 overlay 地址，如 10.144.0.2>"]` ✓ + 照常一条 `DOMAIN-SUFFIX` 规则 ✓

→ 整条链：**rule 选路 ✓ → 家侧落地 ✓ → 经家侧本地网络（含路由器回环）到内网机器 ✓✓**，
**DNS 与请求仍同一条隧道** ✓ —— 这就是"只配 rule + 家侧一个开关"的形态 ✓。

⚠️ 与 §6.18 的分工：`nameserver-policy` 解决"**只有内网 DNS 能解析**"✓；
本节解决"**解析没问题、但只在本地网络内通**"✓。按现场选一个 ✓，不必都上 ✗。

**同一场景下我修掉的内核缺陷** ✓（提交 `6405aed0` ✓）：easytier 出站解析**拨号目标**时原用
`ProxyServerHostResolver`（代理服务器专用 ✗）→ 绕过 `dns.nameserver-policy` ✗，于是"请求进隧道 ✓、
解析在隧道外 ✗"。已改为**内核 DNS** ✓，实测日志确认解析改由 policy 路径完成 ✓。

**合并的两个键策略**（`multi_merge.rs`，改这块前先读它 ✓）：

| 类别 | 键 | 行为 |
| --- | --- | --- |
| 五段合并 | `proxies` / `proxy-providers` / `proxy-groups` / `rules` / `rule-providers` | 逐项合并、冲突进 ConflictViewer ✓ |
| **深合并** | **`dns` / `tun` / `hosts` / `profile`** | 逐键合并 ✓ —— 所以 `dns:`/`hosts:` 写在**任一**被合并的 profile 里都生效 ✓✓ |
| 其余顶层键 | `mixed-port` / `mode` / `log-level` … | **只取合并列表第一位**（`configs[0]`）✗；主 profile 没有该键时，**该键的值不会被应用** ✗ |

⚠️ 2026-10-03 修正：早先这里写成"`dns:`/`hosts:` 会被丢弃 ✗"是**错的** ✗ —— 原因是只读了 `MERGE_STEPS`
没去解析 `DEEP_MERGE_FIELDS` 的内容 ✓。同一次修正还给第三种情况补了**如实的提示与 warn 日志**
（原来基里没有该键时也报 "already exists in primary; kept primary value" ✗，与实际行为不符 ✗）。

**确认走 P2P**（实测：把两端跑起来，看 guest 的 debug 日志 ✓）：

* **方案 A（内核内置 easytier）** ✓：把日志级别设成 **`log-level: debug`** ✓ —— 只有 debug 才打印
  连接详情（info 级的 `peer_added` 只有一个数字 ID ✗）。然后找 `peer_connection_added`：
  ```
  PeerConnAdded(PeerConnInfo { … tunnel: Some(TunnelInfo {
      tunnel_type: "tcp",
      remote_addr: Some(Url { url: "tcp://10.99.0.9:53856" }),      ← 对端自己的地址 = 直连 ✓
      resolved_remote_addr: … }), stats: Some(PeerConnStats { … latency_us, … }), loss_rate: 0.0 })
  ```
  判读：`remote_addr` 是**对端自己的地址** → 直连/P2P ✓；若是**中继节点**的地址 → 走中继 ✗。
  另外日志里大量 `stun.*` / `stun-heyuan-v6.easytier.cn` 记录 = 正在**打洞尝试** ✓（实测两端各 36–40 条 ✓）。
  ⚠️ **方案 A 用不了 `easytier-cli`** ✗：本 fork 的出站选项里**没有** `rpc-portal`（只有 `accept-dns` /
  `tld-dns-zone` 两个 DNS 相关项 ✓），CLI 连不上 guest 的 RPC ✗。
* **方案 B（native 容器）** ✓：官方镜像**自带** `easytier-cli` ✓（`peer` / `route` / `peer-center` / **`stun`** ✓），
  在容器里直接跑即可 ✓：`docker exec easytier easytier-cli peer` ✓。
