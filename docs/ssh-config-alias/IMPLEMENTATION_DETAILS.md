# SSH Config Alias Support - 实现细节 (Implementation Details)

## 核心工作内容

本项目旨在解决 Mihomo SSH 出站适配器无法利用系统 SSH 配置文件（`~/.ssh/config`）的高级功能（如 `ProxyJump`、`ProxyCommand`）的问题。

### 1. 遇到的挑战

- **权限隔离**：Mihomo 通常以 root 权限运行（用于 TUN 模式等），但用户的 SSH 配置文件和密钥通常位于普通用户目录下，且权限严格限制（需 600/700）。
- **环境依赖**：某些 `ProxyCommand`（如 `cloudflared`）依赖用户的环境变量（`PATH`）才能正确执行。
- **协议冲突**：简单的 `ssh -W` 转发如果处理不当，会产生双层 SSH 握手冲突。

### 2. 解决方案：系统 SSH 代理模式

我们不尝试在 Go 语言中重新实现 SSH 配置解析，而是直接调用系统的 `ssh` 命令来建立隧道。

#### 核心命令

```bash
sudo -u <username> -i ssh -W localhost:22 <host-alias>
```

- **`sudo -u <username>`**:  切换到指定用户（或自动检测的实际用户），解决文件权限问题。
- **`-i` (Login Shell)**: 加载用户的完整登录环境（读取 `.zshrc`/`.bashrc`），确保 `PATH` 包含所有必要工具。
- **`-W localhost:22`**: 建立一条透传 TCP 隧道到目标主机的 SSH 端口，Mihomo 在此隧道上进行自己的加密握手。

### 3. 代码变更

#### 新增文件: `adapter/outbound/ssh_system.go`

- 封装了系统命令的调用逻辑。
- 实现了 `sshCmdConn` 结构体，将 SSH 子进程的标准输入/输出（stdin/stdout）适配为 `net.Conn` 接口，使其能无缝集成到 Mihomo 的连接池中。
- 增加了用户自动检测逻辑：
    1. 优先使用配置的 `ssh-user`。
    2. 尝试从 `ssh-user-home` 路径推断。
    3. 尝试读取 `SUDO_USER` 环境变量。
    4. 扫描 `/Users` 目录（macOS）作为降级方案。

#### 修改文件: `adapter/outbound/ssh.go`

- 扩展了 `SshOption` 配置结构，新增：
    - `UseSshConfigAlias` (bool): 启用开关。
    - `SshUser` (string): 显式指定执行用户。
    - `SshUserHome` (string): 用户主目录辅助字段。
- 在 `connect` 方法中增加分支判断，当启用该功能时调用 `dialViaSystemSsh`。

### 4. 优势

- **零配置拥有高级功能**：只要系统终端能 `ssh host` 通，Mihomo 就能用。
- **无需密钥迁移**：不需要把密钥复制到 Mihomo 的配置中，直接读取 `~/.ssh/id_rsa`。
- **兼容性强**：支持所有 OpenSSH 客户端支持的指令（`Match`, `Include`, `Certificate` 等）。
