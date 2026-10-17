# EasyTier 内网接入 —— 目标、架构与经验（生产版）

**一句话目标**：任意一台机器，**只装 Clash + 导入一份配置**，就能
① 访问我家的内网 ✓　②（可选）**借我的网络出网** ✓ —— 不需要额外容器、不需要 VPN 客户端。

父目录下三个角色，各自一个文件夹：

| 目录 | 角色 | 跑在哪 | 要什么权限 |
| --- | --- | --- | --- |
| [`home/`](home/) | **家侧网关**：公告家里网段 ✓、替外网机器落地出网 ✓ | 家里（可 Docker `network_mode: host`） | 建 TUN + 转发（`NET_ADMIN` / 容器 host 网络） |
| [`exit/`](exit/) | **出口节点**：把家侧的包用自己的网络送出去 ✓（=`本机`/`workstation`） | 一台有"好网络"的机器（本方案里是 Mac） | 同上 |
| [`rendezvous/`](rendezvous/) | **会合点 / 中继**：让两侧 NAT 后也能连上 ✓ | 任意有公网 IP 的机器（云服务器） | 无（普通容器） |

```text
家侧 home(10.144.0.2)  ⇄  会合点 rendezvous(公网:11010)  ⇄  出口 exit(10.144.0.6)
   ↑ 公告家里网段、当出口落地                                     ↑ 提供网络、也替家侧落地出网
        └── 家侧的机器（含宿主机终端）→ 需要出去的流量 → 出口 → 互联网
```

配置骨架：

```yaml
# 出口（本机）
- name: et-fa
  type: easytier
  tun: true                       # 建真 TUN（**必须是本 fork 的内核** ✗ 上游会静默忽略 ✗）
  ipv4: <overlay 里的唯一地址>/24
  enable-exit-node: true          # 允许家侧借我的网出网
  dialer-proxy: <你的代理组>       # ★ 替对端拨号走本机代理（不写 = 裸直连 ⇒ 国外全不通 ✗）
  peers: ["tcp://<会合点>:11010"]
  exit-nodes: ["<家侧网关的 overlay 地址>"]
```

## 四条目标级不变量（2026-10-17 起 ✓）

> 这四条是**验收标准**，不是建议；违反任一条都算"看着通、其实没通" ✗。

1. **生产级干净**:只用官方/协议层能力(内核 API、EasyTier 官方配置面、特权助手);
   **不写 OS 路由** ✗、不改客户端 ✗;每处改动可回滚且有判据 ✓。
2. **可持续**:出口/入口**职责分离**(出口节点带 tun;入口节点**不带 tun** ⇒ 两者共存 ✓);
   现场/离网**同一套配置**,只切换一个代理分组 ✓;扩展一个站点 = 加一个入口节点 ✓。
3. **终端与浏览器都自动走代理**:靠 **TUN** ✓(系统代理管不到终端 ✗);验收必须
   **终端 `curl`(不带 `-x`)+ 真浏览器**两条都过 ✓。
4. **三类内网目标都覆盖**(见下表)。

### DNS / 分流矩阵（按类型办理，别凭记忆 ✓）

| 类型 | 表现 | 必须 |
| --- | --- | --- |
| **A 公网域名 → 内网 IP**（split-horizon） | `<service>.<corp>` 在内网解析成内网地址 | 该域名进 `dns.fake-ip-filter`，否则被 fake-IP 换掉 ⇒ 必失败 |
| **B 纯内网域名** | 只有内网 DNS 能解 | `dns.nameserver-policy: <域名> → <内网DNS>#<入口节点>`，并按域名分流到入口分组 |
| **C 内网 CIDR** | 直接用 IP | `IP-CIDR,<网段> → 入口分组` |

> 三类都落到**同一个入口分组** ⇒ 终端与浏览器**自动**生效，无需给任何客户端单独配代理 ✓。


## 分享给别人（订阅化 ✓ 模板化 ✓）

```text
① 复制 docs/easytier/client/mihomo-profile.template.yaml → my-profile.yaml
② 填值（或一键渲染,推荐 ✓）:
   sh docs/easytier/client/render.sh        # 缺任一变量会**拒绝输出** ✗;输出前会校验 YAML ✓
③ 导入:Clash Verge Rev → 订阅 → 导入**本地文件**（或把这个文件放进静态托管,作为**订阅 URL** 分享 ✓）
④ 切换:分组 <内网分组名> —— 现场选 DIRECT ✓ / 离网选入口节点 ✓（终端与浏览器自动生效 ✓）
⑤ 交付前自检:sh docs/easytier/check-templates.sh   # 不得有明文 secret / 公网 IP ✗
```

> ⚠️ 模板含 overlay 入网资格 ⇒ 对外分享建议**单独一套网络 + 单独 secret** ✓。

## 三条不可省的判据（少一条就会出现"看着通、其实没通"✗）

1. **出口侧 `dialer-proxy` 必须写** ✓ —— 出口替家侧拨号时走的是 mihomo 的 **dialer**（不是规则引擎 ✗），
   不写就是裸直连 ⇒ **国内目标能通 ✓、国外目标一律 503/504 ✗**（实测过的典型症状 ✓）。
2. **出口侧 DNS 必须经代理 DoH** ✓ —— 否则替家侧解析国外域名会被国内 DNS 投毒
   （实测：`www.google.com` 被解析成 `69.171.235.22`，Facebook 段 ✗）⇒ 后续拨号必然失败 ✗。
   写在 profile 的 `dns.nameserver-policy` 里（**不要**只改运行时配置 ✗ —— 重新生成会被覆盖 ✗）。
3. **家侧宿主机终端要接管的话，`tun.route-exclude-address` 必须排除管理面** ✓ ——
   否则 SSH 来源网段、内网、Docker 网桥会被一起送进隧道 ✗ ⇒ 会把自己的管理连接掐断 ✗。

## 经验教训（都是实测踩出来的 ✓）

| 现象 | 真因 | 处置 |
| --- | --- | --- |
| 家侧终端 `curl google` 不通 ✗ | 宿主机根本没被接管（流量直连出网 ✗） | 家侧容器改 `network_mode: host` + 开 `tun` ✓（见 `home/`） |
| 借出口"国内能通、国外不通" ✗ | 出口侧没配 `dialer-proxy` ⇒ 裸直连 ✗ | 见判据 1 ✓ |
| 国外目标解析成奇怪 IP ✗ | 出口用了裸国内 DNS ✗ | 见判据 2 ✓ |
| 配置改对了却"没生效" ✗ | 改的是**生成物**（`clash-verge.yaml`）而不是内核真正读的运行时配置 ✗ | 改 profile ✓ 或改运行时 ✓（两者要一致 ✓） |
| macOS 上自建 TUN 完全不通 ✗ | macOS `utun` 每包带 **4 字节地址族头** ✗，裸读写会让内核丢弃/误判 ✗ | 本 fork 已修（`tun_packet_darwin.go` ✓）；**必须用本 fork 的内核** ✓ |
| 一开始"借出口成功"的读数 ✗ | 那是 guest 在宿主上的**裸直连**（不出现在 `/connections`、不经过 TUN ✗） | 判据要看**替对端拨号那条连接的字节数**（`wrote/read` ✓），不要看表象 ✓ |
| 家侧网关带 `sysctls` + host 网络起不来 ✗ | host 模式下 Docker 禁止设 `ip_forward` ✗ | 删掉 `sysctls` ✓（见 `home/docker-compose.yml` ✓） |

## 安全（对外分享前必看 ✓）

* 配置里含 **`network-secret`** ⇒ **分享配置 = 给出入网资格** ✓；
* 对外分享建议：**单独一套网络名 + 单独的密钥** ✓（`rendezvous/` 里也是同一份密钥 ✓）；
* 会合点用**私有模式** ✓（`--private-mode true` ✓）：只接受同名同密钥的网络 ✓，不必配 fail2ban ✓；
* 会合点放公网只需开 **11010 tcp+udp** ✓，其余端口按需 ✓。

## 验收（可执行 ✓，全部是 TCP；不要用 ICMP 判断 ✗）

```bash
# ① 家侧 → 出口：内网出口连通（在国内目标上先跑一次，排除链路问题 ✓）
curl -s -o /dev/null -w '%{http_code}\n' http://<家侧内网某台机器的 IP>:<端口>/

# ② 家侧借出口出网（国外目标 ✓，这一步才依赖 dialer-proxy + DoH ✓）
#    在 Clash 里对该出口节点做延迟测试：url 用 https://www.google.com/ ⇒ 期望有毫秒数 ✓
# ③ 家侧终端：curl -s -o /dev/null -w '%{http_code}\n' https://www.google.com/   ⇒ 期望 200 ✓
# ④ 出口侧自检（本机自己的网必须照旧通 ✓）：
curl -s -o /dev/null -w '%{http_code}\n' https://www.baidu.com/   ⇒ 期望 200 ✓
```

详细到每个角色的步骤，见各自目录里的 `README.md` ✓。设计与验收规格见 [`tun-mode-spec.md`](tun-mode-spec.md) ✓；
macOS 服务模式下内核被 SIGKILL 的那个坑见 [`service-mode-sigkill.md`](service-mode-sigkill.md) ✓。
- 想**全自动改配置/重建内核**、或排查"借出口超时"?看 [`ops-sop.md`](ops-sop.md)(特权助手 + 测试纪律 + 故障模式表 ✓)
