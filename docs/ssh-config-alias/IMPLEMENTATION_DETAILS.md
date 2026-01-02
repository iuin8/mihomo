# SSH Config Alias Support - 功能原理与实现说明

本项目为 Mihomo 引入了对系统 SSH 配置文件（`~/.ssh/config`）的完美支持。通过调用系统 `ssh` 命令作为“传输层”，Mihomo 能够无缝利用 `ProxyJump`、`ProxyCommand`、密钥别名等高级功能。

---

## 核心设计原理

### 1. 双层 SSH 隧道 (Dual-Layer SSH)
这是本设计的核心。连接被分解为两个逻辑层：

*   **外层 (Outer Layer - 传输层)**：由系统 `ssh` 命令执行。
    *   **命令**：`sudo -u fa ssh -o BatchMode=yes -W localhost:22 <alias>`
    *   **职责**：负责穿透复杂的网络环境（如跳板机等）。
    *   **产出**：通过 `-W` 参数建立一个原始的 TCP 流管道。
*   **内层 (Inner Layer - 协议层)**：由 Mihomo 内置的 Go SSH 内核执行。
    *   **逻辑**：在“外层”提供的管道上发起 SSH 握手。
    *   **职责**：进行最终的身份认证并执行 `direct-tcpip` 指令，用于转发上网流量。

> [!NOTE]
> 这种设计实现了完美的解耦：系统 SSH 解决“怎么连上服务器”，Mihomo 解决“连上后怎么代理流量”。

---

### 2. 贪婪环境抓取 (Greedy Environment Capture)
为了解决 `ProxyCommand`（如 `cloudflared`）对环境变量（`PATH`、Token）的强依赖，我们引入了“贪婪抓取”机制。

*   **执行环境隔离**：Mihomo 通常运行在 root 下，而用户配置在普通用户（如 `fa`）下。
*   **静默抓取**：在第一次连接前，自动执行 `sudo -u fa -i env`。
*   **环境解析**：捕获登录 Shell 的**全量**环境变量，并将其缓存。
*   **安全注入**：实际执行 SSH 连接时，注入这些变量。这保证了 `cloudflared` 能正确找到其认证信息。

---

### 3. 自愈式环境缓存 (Self-Healing Cache)
为了保证长期运行的稳定性，缓存具备自愈能力：

*   **失效检测**：持续监控 SSH 子进程的退出状态。
*   **自动清理**：一旦子进程异常退出（如因 Token 过期导致连接失败），系统会立即**清除**该用户的环境缓存。
*   **自动恢复**：下一次连接尝试会自动重新触发“环境抓取”，从而捕获最新的变量或重新生成的 Token。

---

### 4. 智能 Sudo 探测 (Smart Sudo)
为了减少不必要的性能开销和环境剥离：

*   **用户比对**：判断当前执行 Mihomo 的进程用户与目标 SSH 用户。
*   **动态切换**：如果用户一致，直接执行 `ssh`，跳过 `sudo` 环节。这显著降低了环境污染的概率并提高了启动速度。

---

## 协议冲突保护 (Stdout Protection)

这是一个极为关键的技术细节。SSH 握手对 `stdout` 非常敏感，任何非协议数据的混入（如 `Last login`、`MOTD` 消息）都会导致 `Invalid SSH identification string` 报错。

*   **双步分离方案**：
    1.  **抓取环境时**：使用 `-i` (Login Shell) 以获取完整变量，此时我们不关心握手。
    2.  **建立连接时**：使用 **非交互模式** (不带 `-i`)，但注入第一步抓取的环境变量。
*   **结果**：既拥有了全量变量，又保证了连接管道的绝对纯净。

---

## 代码结构

*   [ssh.go](/adapter/outbound/ssh.go): 负责高层逻辑分发和订阅配置解析。
*   [ssh_system.go](/adapter/outbound/ssh_system.go): 核心实现，包含环境抓取、进程管理、管道适配和缓存逻辑。

---
*Last Updated: 2026-01-02*
