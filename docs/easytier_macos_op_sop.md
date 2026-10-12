# macOS 侧「必须人工处理」的操作 SOP

> 适用范围：Clash Verge（fork）+ 本 fork 编译的 mihomo 内核，**服务（Service）模式**。
> 目标：**您只需要做两件事**——装内核、点重启。其余全部可以交给命令行/自动化。

---

## 0. 先认清三个事实（这决定了一切）

| 事实 | 含义 |
| --- | --- |
| **服务跑的内核是** `Service 目录`**里的那份** | 路径：`/Library/Application Support/clash-verge-service/cores/verge-mihomo-alpha`（**root 所有**） |
| **App bundle 里那份**只是备份/通道副本** | 路径：`/Applications/Clash Verge.app/Contents/MacOS/verge-mihomo-alpha` |
| **换 `Service 目录`里的文件**不影响任何签名** | 它**不是** bundle ⇒ 随便换 ✓ |
| **换 App bundle 里的文件会**废掉整个 bundle 的签名** ✗ | ⇒ 服务启动时被 Gatekeeper **SIGKILL** ⇒ 必须**重签整个 bundle** |

**⇒ 结论**：**平时只装 Service 目录那一处**。App bundle 那份**别动**（除非您愿意跟着重签）。

---

## 1. 情况 A：换内核（最常见）

**什么时候需要**：我给了您一个新内核文件（例如 `/tmp/fa-fwd`）。

**做两件事**：

```bash
# ① 装（会自检、自动备份、并打印回滚命令）
sudo sh ~/dev/projects/other/me/fork/clash-group/mihomo/scripts/install-service-core.sh /tmp/fa-fwd
```

```text
② 在 Clash Verge 里点一次「重启内核」
```

**⇒ 没点那一下会怎样**：`launchctl kickstart` **不会**自动拉起它（实测），内核会**一直躺着** ⇒ 代理**不可用**。

**⇒ 装完的**判据**（三条全对才算成功）：

```bash
# 内核活着吗
pgrep -f 'clash-verge-service/cores'            # 应有 PID 输出
# 跑的是哪一份（版本号由构建时注入）
curl -s --unix-socket /var/run/clash-verge-service/users/501/verge-mihomo.sock http://localhost/version
```

| 判据 | 期望 |
| --- | --- |
| `pgrep` | **有 PID** ✓ |
| `/version` | **与我告诉您的版本号一致** ✓（形如 `1.10.0-fa.1012.fwd`） |
| 代理自检 | 见下 |

```bash
# 代理自检（本机自己的出口，必须通）
curl -s --unix-socket /var/run/clash-verge-service/users/501/verge-mihomo.sock \
  'http://localhost/proxies/et-fa/delay?url=http%3A%2F%2Fwww.baidu.com%2F&timeout=12000'
# 期望: {"delay":数字}
```

---

## 2. 情况 B：回滚（内核有问题时）

**做两件事**：

```bash
sudo sh ~/dev/projects/other/me/fork/clash-group/mihomo/scripts/rollback-service-core.sh
```

```text
在 Clash Verge 里点一次「重启内核」
```

**⇒ 它用的是 `/tmp/core-backup-<名字>-<时间戳>`**（⇒ `install-service-core.sh` 每次都会建一个 ✓）。

**⇒ 若 `/tmp` 被清空过**（⇒ 重启机器会清 ✓）：备份**没了** ⇒ 只能重新装一份**已知good**的内核 ✓（⇒ 问我要 ✓）。

---

## 3. 情况 C：App 报 `重启内核失败 … signal: 9 (SIGKILL)`

**⇒ 这是签名问题**，不是内核问题。**两种原因**：

| 原因 | 症状 | 处理 |
| --- | --- | --- |
| **flags 里有 `runtime`** ✗ | 崩溃报告写 `CODESIGNING` / `Invalid Page` | **重装一份不带 `runtime` 的** ⇒ 见情况 A（⇒ 装脚本会**拒绝**带 `runtime` 的文件 ✓） |
| **App bundle 签名被改坏** ✗ | `codesign --verify --deep --strict "/Applications/Clash Verge.app"` 非零 | 见下（**只在您动过 App bundle 时才会发生**） |

**⇒ 诊断**（一条）：

```bash
codesign --verify --deep --strict "/Applications/Clash Verge.app" && echo "✓ bundle 完好" || echo "✗ bundle 签名坏了"
# 崩溃报告（若有）：
ls -t /Library/Logs/DiagnosticReports/verge-mihomo-alpha-*.ips | head -1
```

**⇒ 若 bundle 坏了 ⇒ 重签（一条，注意**没有** `--options runtime`）**：

```bash
sudo codesign --force --deep --sign - --timestamp=none "/Applications/Clash Verge.app" \
  && codesign --verify --deep --strict "/Applications/Clash Verge.app" && echo "✓ 已重签"
```

> ⚠️ **`--deep` 会重签 bundle 里**所有**二进制**（含主程序）⇒ 只在必要时机用 ✓。

---

## 4. 绝对不要做的事

| ✗ 不要 | 为什么 |
| --- | --- |
| **给内核签名时加 `--options runtime`** | Hardened Runtime ⇒ **禁 JIT** ⇒ wazero 崩溃 ⇒ SIGKILL |
| **手动 `cp` 内核进 App bundle 却不重签** | 整个 bundle 判废 ⇒ 服务起不来 |
| **以为「终端能跑 `-v`」就等于「服务能跑」** | 服务走 Gatekeeper ＋ 会真的执行 JIT；终端都不走 |
| **`launchctl kickstart` 想拉起内核** | 实测无效 ⇒ 必须在 App 里点「重启内核」 |
| **在 `/tmp` 里长期保存唯一备份** | 重启机器会清 ⇒ 备份要留一份在别处 |

---

## 5. 一页速查

```text
换内核:   sudo sh .../scripts/install-service-core.sh <新内核>   →  点「重启内核」
回滚:     sudo sh .../scripts/rollback-service-core.sh            →  点「重启内核」
查版本:   curl -s --unix-socket /var/run/clash-verge-service/users/501/verge-mihomo.sock http://localhost/version
查活着:   pgrep -f 'clash-verge-service/cores'
查签名:   codesign -dv <内核> 2>&1 | grep flags      # **不能**出现 runtime
查 bundle: codesign --verify --deep --strict "/Applications/Clash Verge.app"
回滚点:   /tmp/core-backup-verge-mihomo-alpha-*      # install 脚本每次自动建
```
