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

**先搞清用哪种并入方式——段名不一样：**

| 你的用法 | 用什么段名 | 说明 |
| --- | --- | --- |
| 当成一个**订阅 / 本地配置**，与机场订阅一起「**多订阅合并**」（本 fork 的 multi_merge） | **原生段名** `proxies:` / `proxy-groups:` / `rules:` | 合并流水线是 `MERGE_STEPS: proxies → proxy-providers → rule-providers → proxy-groups → rules`，只认这些；`rules` 会被**前置插入**（优先于订阅的 MATCH 兜底） |
| CVR 的**带类型扩展条目**（`type: proxies` / `groups` / `rules`） | 条目文件里写 `prepend:` / `append:` / `delete:` | 这是 CVR 真正实现的 prepend 机制：`enhance/seq.rs::use_seq` + `enhance/mod.rs:349-357` 分别作用于 rules / proxies / proxy-groups |
| 老式 Merge profile 里写 `prepend-proxies:` / `prepend-rules:` | **不要用** | 「键名自带类型前缀」的写法在上游 2.4.7 里只剩 `enhance/merge.rs` 的测试 fixture，没有实现；本 fork 的多订阅合并路径也不认 |

用原生段名的完整片段见 `examples/easytier-home-gateway/client-clash.yaml`（含代理页分组）：

```yaml
proxies:
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
    interface-name: en0           # 本机跑 TUN 时建议
    # 不要写 state-dir：CVR 的 home dir 是 ~/Library/Application Support/…clash-verge-rev

proxy-groups:                     # 代理页的卡片来自分组；不加分组你以为没生效
  - name: 🏠 家里
    type: select
    proxies: [home-overlay, DIRECT]

rules:
  - IP-CIDR,10.144.0.0/24,🏠 家里   # overlay 网段本身也要走隧道
  - IP-CIDR,<家里网段>,🏠 家里      # 精确写！与本机所在网段重叠的那条不要写
  - SRC-IP-CIDR,<本机网段>,DIRECT   # 在家/在同网段时直连，避免绕隧道
```

**应用后怎么确认真的生效**（比在 GUI 里翻页面快）：

```bash
CFG=~/Library/Application\ Support/io.github.clash-verge-rev.clash-verge-rev/clash-verge.yaml
grep -c "home-overlay" "$CFG"     # 期望 ≥3：代理 1 + 分组 1 + 规则 2 以上
grep -A3 "🏠 家里" "$CFG" | head  # 期望看到 type: select 与 home-overlay
```

> 客户端**不需要换内核**：`easytier` 出站是 upstream 就有的，你现在的 alpha 内核即可。

## 3. 验证（约 1 分钟）

```bash
# ① TCP（换成一个你确定在跑的 LAN 服务，例如路由器 Web 界面 / NAS）
curl -sS -o /dev/null -w '%{http_code} %{size_download}B\n' \
    -x http://127.0.0.1:7897 http://<内网IP>:<端口>/

# ② UDP：对端没有回声服务时，用真实 DNS 查询验证（收到合法 DNS 响应即 PASS）
python3 docs/examples/easytier-home-gateway/tools/socks5-udp-probe.py \
    --socks 127.0.0.1:7897 --target <家里DNS服务器IP> --port 53 --dns-query example.com

# ②' UDP：对端跑了回声服务时（tools/udp-echo-server.py）
python3 docs/examples/easytier-home-gateway/tools/socks5-udp-probe.py \
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
| `path is not subpath of home directory or SAFE_PATHS: /tmp/...` | EasyTier 的 `state-dir` 必须在**内核自己的 home dir** 内。终端跑隔离实例时是 `-d /tmp/et-client`（所以 `/tmp/et-client/state` 合法），但 **CVR 的 home dir 是 `~/Library/Application Support/io.github.clash-verge-rev.clash-verge-rev`** → 放进 CVR 的配置里写 `/tmp/...` 必然被拒 | 放进 CVR 的配置**直接删掉 `state-dir` 那一行**（默认 `easytier/<代理名>`，就在 home dir 内，合法）。另外别把隔离用的整份配置当 CVR profile 导入——会顶掉你的订阅，正确做法是 merge profile 里的 `prepend-proxies` / `prepend-rules` |
| 测 `10.144.0.2:9090` 连上但不回包 | 家侧 API 的 DNS-rebinding 保护（非本机来源直接关闭连接） | 别拿 API 当测试目标；用 LAN 上的普通服务，或 `--dns-query` 验 UDP |
| 用 `-x` 代理测某个 IP，结果被本机别的服务接走 | HTTP 代理场景下 `-H 'Host: …'` 会让 mihomo **按 Host 头拨号** | 用 SOCKS（`nc -X 5 -x`）并把 Host 头放进报文里；或干脆别改 Host |
| 规则里的某个家网段与本机当前网段重叠 | 会把你**本机局域网**的流量吸进隧道 | 在新网络里删掉重叠那条；或加 `SRC-IP-CIDR,<本机网段>,DIRECT` 兜底 |
| 浏览器访问不了，但命令行 `-x` 能通 | ① 内核根本没在跑（TUN 也就没了）；② 系统代理被别的工具（Proxyman 之类）占着，浏览器流量先到它，根本进不了 Clash | 先 `pgrep -fl mihomo` 与 `ifconfig utun*` 确认内核在跑；再 `scutil --proxy` 看系统代理指向谁——TUN 模式下**不需要**系统代理，把抢它的工具关掉即可 |
| 延迟测试报 error | 测速 URL 是公网地址，而 overlay 只承载家侧发布的网段（家侧不是互联网出口） | 用内网地址测，或忽略（`select` 分组不需要自动测速） |
| **内核被反复重启**（GUI 日志 `service restarted the core (N restarts so far); last exit: … SIGKILL`，PID 一直变、TUN 时有时无） | **不是看门狗杀卡死的内核，而是内核自己崩了**（macOS 的代码签名校验把进程杀掉）。排查证据链：<br>① 崩溃报告：`ls -t /Library/Logs/DiagnosticReports/verge-mihomo-alpha-*.ips`（实测 27 份），内容为 `EXC_BAD_ACCESS` + `signal: SIGKILL (Code Signature Invalid)` + `termination: {namespace: CODESIGNING, indicator: Invalid Page}`；<br>② 报告里 `usedImages[].size` 等于该二进制的 **`__TEXT` 段 vmsize**（`otool -l <core>` 核对；实测 39780352 = `0x25f0000`），可据此确认"服务模式跑的内核与 sidecar 是同一构建"——**所以不是内核版本旧**；<br>③ 对照实验定位触发面：测机场节点 `{"delay":79}` 安然无恙；给 easytier 出站一个**不可达目标**（`http://10.99.99.99/`）也照样崩 → **与目标无关，是 easytier 的 WASI（wazero JIT）路径一启动就崩**；<br>④ 同一二进制在**用户态**跑 easytier 完全正常 → 差别只在"服务模式（root，由特权服务 approval/重签名后启动）"。<br>**机理**：服务模式会把内核复制进 `…/clash-verge-service/` 并重签名，但**没有 `com.apple.security.cs.allow-jit`**；easytier 是 mihomo 里唯一使用 JIT（wazero 编译器）的路径，JIT 生成的可执行页被判非法页 → SIGKILL。**排除 TUN 路由、关系统代理、腾端口都不会有效**（它们不是病根）。 | **先搞清 macOS 上"不装服务能不能开 TUN"**（源码结论，别凭 UI 猜）：<br>• `core/runstate/health.rs`：`tun_capable() = self.is_admin \|\| self.service_usable()`；<br>• `crates/tauri-plugin-clash-verge-sysinfo/src/lib.rs:113`：`is_binary_admin()` 在非 Windows 上就是 **`libc::geteuid() == 0`**；<br>• UI 里**没有**提权入口：`proxy-control-switches.tsx` 的 `handleTunToggle` 在 `!isTunModeAvailable` 时只弹 `tunNeedsService`，remedy 只有"安装服务/重装/切服务模式"。<br>→ 所以"管理员模式"= **整个 App 以 root 运行**（`sudo "/Applications/Clash Verge.app/Contents/MacOS/clash-verge"`，先退出已运行实例；之后首页出现「管理员模式」徽章，`home.json` 的 `adminMode`）。<br>**两条路**：<br>① **应急（立刻可用）**：以 root 启动 CVR → TUN 可用 → 内核由 App 直接 spawn（**不走服务、不重签名**）→ 不再触发那次 SIGKILL。代价：GUI 以 root 运行，配置/日志/缓存随之以 root 写入；<br>② **正解（不依赖 JIT）**：`easytier-go` 内部是 `wazero.NewRuntime(ctx)`（= 编译器/JIT，`internal/engine/host.go:68`）且**没有注入口子** → 在 mihomo fork 里 `replace` 一个补丁版（换成 `NewRuntimeConfigInterpreter()` 或加可配项），重建 darwin 内核替换 sidecar → **服务模式（重签名）也不会崩**，GUI 仍以普通用户运行。代价：WASI 走解释器（控制面变慢；TUN 模式下数据面走原生路径，影响有限）。<br>（另：实测把 GUI 生成的配置复制到临时目录、`tun.enable=false`、用户态跑 sidecar 内核，经混合端口访问家里 = **200 / 200（88ms）、内核存活、崩溃报告零新增**，可作为"非服务路径不崩"的基线证据） |

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

第 6 节那张表说明：服务模式（root + TUN）下 easytier 的 WASI 实例启动**必然挂住**（随后被服务看门狗 SIGKILL），
而同一二进制、同一配置在**用户态**完全正常。于是把 overlay 拆出去：

```
CVR 内核（服务模式 + TUN）  --socks5-->  用户态网关（launchd 常驻）  --easytier/WASI-->  会合点 --> 家侧
   rules: 10.x → 🏠 家里                     mixed-port 127.0.0.1:17899
```

* 网关：`mac-gateway/`（`config.yaml` + `com.fa.home-overlay.plist`）
  * 内核**用官方版即可**（实测：用户态下官方 JIT 内核跑 easytier 完全正常，内存 ~209MB；
    解释器版同场景 ~435MB）。解释器版仅在"服务模式里跑 WASI"时才需要，而本架构已不需要那种组合
  * 首次启动实例约 20 秒（懒启动），第 2 次起 ~0.1s；换内核/重启网关后第一请求请耐心等实例就绪
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
或者向 mihomo/CVR 上游提 issue（本节的表格就是最小复现集）。**在此之前，推荐 §6.1 的解耦架构**（用户态网关 + socks5 出站）——它已经在生产状态跑通，
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
在产品上，**§6.1 的解耦架构**仍是推荐形态（已连续稳定运行、TCP/UDP 均通、无需改动服务或系统）。
