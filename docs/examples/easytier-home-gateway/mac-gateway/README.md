# 家侧 overlay 网关（macOS 客户端，用户态）

服务模式（root + TUN）的内核**不能**跑 easytier：WASI 实例启动会挂住，被服务看门狗 SIGKILL
（用户态无此问题，实测同二进制同配置：用户态 `10.0.1.181:8080/login` → 200 / 0.57s，服务模式挂住）。
所以把 overlay 拆成**独立的用户态网关**，CVR 只用 `socks5` 出站指向它——服务模式的内核从此不碰 WASI。

## 结构

```
~/Library/Application Support/home-overlay/
├── verge-mihomo-alpha      # 内核（解释器版，见 mihomo/third_party/README.md）
├── config.yaml             # 本目录 config.yaml（填好密钥/会合点）
├── gateway.log             # 日志
└── easytier/               # 实例状态（自动生成）
~/Library/LaunchAgents/com.fa.home-overlay.plist   # launchd 常驻（RunAtLoad + KeepAlive）
```

## 安装

```bash
# 1) 复制内核（解释器版）与配置
GW="$HOME/Library/Application Support/home-overlay"
mkdir -p "$GW"
cp <解释器版内核> "$GW/verge-mihomo-alpha" && chmod 755 "$GW/verge-mihomo-alpha"
cp config.yaml "$GW/config.yaml"        # 先改里面的 network-secret 与 peers
# 2) 装 LaunchAgent（把 plist 里的 $GW 展开成实际路径）
cp com.fa.home-overlay.plist "$HOME/Library/LaunchAgents/"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.fa.home-overlay.plist"
# 3) 验证
curl -s -o /dev/null -w '%{http_code}\n' -x http://127.0.0.1:17899 http://10.0.1.181:8080/login   # 期望 200
```

## CVR 侧（本机 Clash 的配置项）

把 easytier 出站换成指向网关的 SOCKS5（`udp: true` 让 UDP 也走它）：

```yaml
proxies:
  - name: home-overlay
    type: socks5
    server: 127.0.0.1
    port: 17899
    udp: true
```

规则与分组不变（`IP-CIDR,10.x,🏠 家里` → `home-overlay`）。之后浏览器 / 终端都经 CVR 的 TUN → SOCKS5 → 网关 → overlay。

## 排错

| 现象 | 检查 |
| --- | --- |
| `17899` 连不上 | `launchctl print gui/$(id -u)/com.fa.home-overlay`、`tail -20 "$GW/gateway.log"` |
| 网关起来了但家里不通 | `curl -x http://127.0.0.1:17899 http://10.0.0.1/`；再看家侧容器 `docker compose logs`（会合点/家侧是否在线） |
| 经 CVR 的 TUN 不通但网关直连通 | 确认 CVR 已应用新配置（`grep -A5 'name: home-overlay' …/clash-verge.yaml` 应显示 `type: socks5`） |
