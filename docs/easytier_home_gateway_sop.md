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

> ⚠️ **历史方案（已被 §6.9 取代）**：自 2026-10-02 起生产形态是**单内核**（服务模式内核内置 `et-core`），用户态网关已退役。以下 §6.1 内容保留为排查记录与回滚参考。

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
| 内核内置 `et-core`（组 `🏠 家里` 首选） | **主路径**：家里内网 TCP/UDP 直通，无额外进程 | `10.144.0.9` |
| 用户态网关（`home-overlay` socks5，组内可切换） | **兜底**：内置路径出问题时可一键切回 | `10.144.0.4` |

注意：服务模式内核的终止机制仍是「SIGTERM → 1 秒 → SIGKILL」（§6.7），
而历史那批「一用 easytier 就被杀」的现场，**重装服务（重置 owner 世代）后不再复现** —— 见 §6.5 的
`owner recovery (TransportFailure)` 记录：那些 SIGKILL 更可能是被反复替换内核/重启搅乱的 **owner 会话状态**所致。

### 6.9 终态（2026-10-02）：一个 mihomo

生产形态最终收敛为**一个内核进程**：

* CVR 服务模式内核（**解释器版** easytier-go，见 §6.8）内置 `et-core`，
  组 `🏠 家里: [et-core, DIRECT]`，家里四条网段规则不变；
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

> 镜像**默认是纯 mihomo**（无内置配置，挂自己的 YAML 即通用代理容器 ✓）；
> 传了 `NAT_INTERFACE` / `TRIGGER_PROXY` / `WATCHDOG` / `SKIP_NAT` / `API_BASE` 或 `GATEWAY=1`
> 才进入家侧网关模式（NAT + 触发懒启动 + 看门狗 ✓）。

```bash
# 发布（把内核版本一并写进镜像里的 mihomo -v）
gh workflow run mihomo-image.yml -R iuin8/mihomo --ref <branch> \
  -f tag=v1.19.32-fa.1001 -f mihomo_version=v1.19.32-fa.1001
```

**用镜像起家侧**（不需要仓库源码、不需要本地 Go）：

```bash
mkdir -p ~/easytier-home-gateway && cd ~/easytier-home-gateway
# 只需从仓库取两个文件：docker-compose.yml + home-mihomo-tun.yaml
# 改 home-mihomo-tun.yaml 中三处「改这里」：network-secret / peers / proxy-networks
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
> 但**本仓库实测**：由工作流推上去的新包**就是 public** ✓（`ghcr.io/iuin8/easytier-home-gateway` 在删掉后
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
| 匿名 `docker pull` | ⚠️ **本节初稿写错了**：当时匿名拉取成功的是**旧包名** `ghcr.io/iuin8/easytier-home-gateway` ✓，而 `mihomo` / `mihomo-home-gateway` 这两个名字**从未被创建过** ✗ —— 原因是 CI 里 `images:` 用的是 `${{ github.repository_owner }}` 表达式，改名时没匹配上（详见 §6.14）|
| 包可见性 | **公开** ✓（匿名可拉 ✓）——注意 `gh` 的 OAuth token 默认**没有** `read:packages`，用 `gh api /user/packages/...` 查会 403 ✗；那只说明 token 范围不够，**不代表包是私有的** ✓ |
| 镜像内版本 | `Mihomo Meta v1.19.32-fa.1001 linux arm64`（版本注入生效 ✓）|
| 镜像内指纹 | 解释器补丁 **1** ✓ / 上游 #3215 监督 **1** ✓ / 旧泄漏重试 **0** ✓ / TUN 位 **3** ✓ |
| 镜像体积 | 106MB ✓（alpine 3.24 ✓，含 iptables/ip ✓）|

**家侧切到 GHCR（从"本地编译"改成"只用镜像"）**：

```bash
cd <家侧目录>                     # 里面应有 docker-compose.yml、home-mihomo-tun.yaml、state/
# 1) 用仓库里新版的两个文件覆盖（新版 docker-compose.yml 默认拉 GHCR 镜像，不再 build）
#    docker-compose.yml、home-mihomo-tun.yaml（后者把 API 收到 127.0.0.1）
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
  2. **待验证的免费午餐** ✗：EasyTier 在 native（`no_tun`）模式下**自己会做 SNAT** ✓（见 `alt-native/docker-compose.yml` 的说明 ✓），TUN 模式下是否同样如此**尚未实测** ✗ —— 值得在家侧做一次实验：设 `SKIP_NAT=1` ✓，从客户端 `curl`/`ping` 内网机器 ✓；若通 ✓ 则这一步 NAT 也可删掉 ✓✓。
  3. **改用 native 方案（方案 B）** ✓：没有 TUN、没有 iptables ✓，EasyTier 内部 SNAT ✓ —— 代价是**没有 ICMP**、吞吐 ~8.4 MB/s ✓。
* **它不算妥协的原因** ✓：入口脚本里只剩 8 行、幂等、失败时只告警不中断 ✓，而且它编码的是**拓扑事实**✓，不是给某个 bug 打的补丁 ✗（旧看门狗与"触发懒启动"才是补丁 ✗，都已被内核能力取代 ✓）。

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

**症状**：本地改了三遍镜像名（`easytier-home-gateway` → `mihomo-home-gateway` → `mihomo`），
但每次 CI 构建完推上去的**仍是旧名字** ✗；被删掉的旧包名还会"自己回来" ✗；而
`https://github.com/users/iuin8/packages/container/mihomo/settings` 始终 404 ✗。

**根因**：工作流里那行是

```yaml
images: ghcr.io/${{ github.repository_owner }}/easytier-home-gateway
```

用的是 **`${{ github.repository_owner }}` 表达式**，而不是字面量 `ghcr.io/iuin8/…` ✓ ——
而我的改名脚本与"核对脚本"都只匹配 `ghcr.io/iuin8/…` 与 `ghcr.io/<owner>/…` 两种字面写法 ✗，
**表达式形式两种都没匹配上** ✗。于是：改名没生效 ✗、核对也看不见 ✗（**核对与改名共用同一套盲区** ✗），
而每次 CI 一推就把刚被删掉的旧包**重新创建**出来（新包默认为 private ✗）✓。

**教训（已固化为习惯）**：
1. **核对要打印原文行，不要只看计数** ✓ —— 计数为 0 只说明"没有匹配到我的模式"，不等于"没有旧名字" ✗。
2. **改名脚本与核对脚本不要共用同一套模式** ✗ —— 否则盲区完全重合 ✓。
3. CI 里的镜像名**用字面量**，不要用表达式 ✓（少一层"看不出来"的间接 ✓）。
4. `workflow_dispatch` 与默认分支的关系有**两层**（2026-10-03 实测确认 ✓，不是推断）：
   * 工作流文件**必须存在于默认分支** ✓，否则 `gh workflow run` 直接 404 ✗；
   * **执行的是默认分支上那份定义** ✗ —— `--ref` 只决定 checkout 用哪个提交的代码 ✓。
   验证方法（2 分钟，代价一次可取消的运行）：在 `main` 的副本里把 `name:` 改成带标记的名字 ✓，用分支
   ref 触发 ✓ → 运行标题显示的是 `main` 上的标记名 ✓（实验后立刻 `gh run cancel` ✓）。
   **因此本次改名失效有两个同时成立的原因** ✓：① `images:` 用了 `${{ github.repository_owner }}` 表达式，
   改名与核对都没匹配上 ✗；② 即使改对了分支副本，触发时跑的仍是 `main` 上那份 ✗。
   **固化做法**：两份**逐字节一致** ✓（改完用 `git show <ref>:<文件> | md5` 比对**内容**，不看名字 ✗），
   并且"必须同步到 main"的警示注释直接写在文件里 ✓。
