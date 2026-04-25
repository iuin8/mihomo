# 系统级 SSH SOCKS 代理指南

Mihomo 的系统级 SSH 模式只保留一条清晰路径：由 Mihomo 启动并托管本机 OpenSSH，执行 `ssh -N -D 127.0.0.1:<port> <server>` 建立本地 SOCKS5 隧道，然后复用 Mihomo 现有的 SOCKS5 outbound 作为数据转发路径。

这意味着 OpenSSH 负责 SSH 配置、认证、密钥、Agent、Keychain、ProxyJump、ProxyCommand、硬件密钥和平台行为；Mihomo 负责规则匹配、代理选择、连接生命周期和 SOCKS5 数据面。

## 设计目标

- **单一 SSH 层**：不再用 Go SSH 客户端包一层 OpenSSH，也不再走旧的 `ssh -W` 管道模式。
- **原生 OpenSSH 行为**：`server` 直接传给 OpenSSH，可使用 `~/.ssh/config` 里的 Host 别名。
- **复用 Mihomo SOCKS5 数据面**：OpenSSH 只提供本地 `-D` SOCKS5 监听，真实流量仍由 Mihomo 的 SOCKS5 outbound 处理。
- **稳定托管进程**：禁用 OpenSSH ControlMaster/后台化等会让 `ssh -D` 主进程退出的配置，确保 Mihomo 能准确管理进程生命周期。
- **真实就绪检测**：启动后必须通过 SOCKS5 greeting 探测，而不是只判断 TCP 端口可连接。
- **干净配置面**：系统 SOCKS 模式只需要 `server`、`use-system-socks`，可选 `ssh-user`、`ssh-flags`、`system-socks-port`。

## 最小配置

```yaml
proxies:
  - name: SSH_SYSTEM
    type: ssh
    server: my-host-alias
    use-system-socks: true
```

`server` 会原样作为 OpenSSH destination 传入。推荐把 SSH 服务器细节写在 `~/.ssh/config`：

```sshconfig
Host my-host-alias
  HostName example.com
  User fa
  Port 22
  IdentityFile ~/.ssh/id_ed25519
  ProxyJump jump-host
```

然后 Mihomo 配置里只写：

```yaml
server: my-host-alias
use-system-socks: true
```

## 完整配置示例

```yaml
proxies:
  - name: SSH_SYSTEM
    type: ssh
    server: my-host-alias
    use-system-socks: true
    ssh-user: fa
    system-socks-port: 1080
    ssh-flags:
      - "-J"
      - "jump-host"
      - "-o"
      - "StrictHostKeyChecking=accept-new"
```

## 字段说明

### `use-system-socks`

启用系统级 OpenSSH SOCKS 模式。启用后，Mihomo 会按需启动本机 `ssh -N -D 127.0.0.1:<port>`。

### `server`

OpenSSH destination，通常是 `~/.ssh/config` 的 Host 别名，也可以是普通主机名。

Mihomo 不再用 `ssh -G` 解析配置，也不会在 Go 层复制 `hostname/user/identityfile`。所有别名、跳板、密钥和认证逻辑都交给 OpenSSH 自己处理。

### `ssh-user`

指定本机运行 OpenSSH 的操作系统用户。

- macOS/Linux：如果当前 Mihomo 进程用户和 `ssh-user` 不同，会使用非交互 `sudo -n -u <user> -H ssh ...` 启动。
- Windows：不支持切换用户，只允许 `ssh-user` 与当前进程用户一致。
- 不允许指定 `root`、`SYSTEM` 等服务账号。

通常 GUI/sidecar 场景可以不写，Mihomo 会自动识别当前登录用户；如果自动识别失败，再显式配置 `ssh-user`。

### `system-socks-port`

可选。指定本地 `127.0.0.1` SOCKS5 监听端口。

不配置时，Mihomo 会自动选择空闲端口。只有在调试、抓包或需要固定端口时才建议配置。

### `ssh-flags`

追加 OpenSSH 参数，用于补充 `~/.ssh/config` 中没有的选项，例如：

```yaml
ssh-flags:
  - "-J"
  - "jump-host"
  - "-o"
  - "StrictHostKeyChecking=accept-new"
```

普通参数会保留，例如 `-v`、`-J`、`-o ConnectTimeout=30`。

以下会破坏 Mihomo 托管生命周期的参数会被过滤或强制覆盖：

- `-f`
- `-M`
- `-S <path>`
- `-O <command>`
- 组合形式，例如 `-fN`、`-MN`、`-MS <path>`、`-MO <command>`
- `-o ControlMaster=...`
- `-o ControlPath=...`
- `-o ControlPersist=...`
- `-o ForkAfterAuthentication=...`

Mihomo 会强制添加：

- `ControlMaster=no`
- `ControlPath=none`
- `ControlPersist=no`
- `ForkAfterAuthentication=no`

这是为了避免 OpenSSH 复用已有 master 后让当前 `ssh -D` 进程成功退出，导致 Mihomo 无法准确管理 SOCKS 隧道。

### `port`、`username`、`password`、`private-key`

这些字段属于 Mihomo 内置 Go SSH 客户端路径；在 `use-system-socks: true` 下不需要使用。

系统 SOCKS 模式请把 SSH 服务器端口、用户名、私钥、证书、跳板等配置放到 `~/.ssh/config`，或通过 `ssh-flags` 传给 OpenSSH。

## 自动用户识别

未配置 `ssh-user` 时，Mihomo 会自动选择用于启动 OpenSSH 的本机用户：

1. 显式 `ssh-user`
2. 非服务账号的 `SUDO_USER`
3. 当前进程用户（如果不是 `root` / `SYSTEM`）
4. 当前登录桌面用户

macOS 当前登录用户检测优先使用：

```bash
/usr/bin/stat -f %Su /dev/console
```

如果不可用，再 fallback 到：

```bash
/usr/sbin/scutil show State:/Users/ConsoleUser
```

这样可以避免 GUI/sidecar/服务环境里 `PATH` 不包含 `/usr/sbin`，或 `scutil` 状态为空导致无法自动识别用户。

如果最终只能解析到 `root`、`SYSTEM`、`loginwindow` 或空用户，Mihomo 会拒绝启动系统 SSH，并提示配置 `ssh-user` 或以真实用户运行。

## 启动和转发流程

1. 首次流量命中该 SSH proxy。
2. Mihomo 解析实际本机用户。
3. 自动选择本地端口，或使用 `system-socks-port`。
4. 启动 OpenSSH：

   ```bash
   ssh -T -N -D 127.0.0.1:<port> ... -- <server>
   ```

5. 捕获目标用户登录环境，尽量拿到正确的 `SSH_AUTH_SOCK`。
6. 等待本地端口通过 SOCKS5 greeting 探测。
7. 创建内部 `Socks5` outbound 指向 `127.0.0.1:<port>`。
8. 后续连接复用同一个托管 SSH SOCKS 进程。
9. proxy 关闭时，Mihomo kill 对应 OpenSSH 进程并清理状态。

## 使用建议

### 推荐做法

- 优先把 SSH 细节写进 `~/.ssh/config`。
- `server` 使用 Host 别名。
- GUI/sidecar 正常运行时不写 `ssh-user`；如果自动用户识别失败，再写 `ssh-user: <你的登录用户名>`。
- 先在终端确认 OpenSSH 可非交互连接。

### 终端验证

```bash
ssh -N -D 127.0.0.1:1080 my-host-alias
```

如果终端里这条命令不能稳定运行，Mihomo 也无法让它可靠。

### 避免递归代理

如果规则把 `ssh` 进程自己的连接也代理进 Mihomo，可能形成递归。建议加直连规则：

```yaml
rules:
  - PROCESS-NAME,ssh,DIRECT
  - PROCESS-NAME,ssh.exe,DIRECT
```

## 常见问题

### 看不到 `Capturing fresh environment for user: ...`

通常说明还没解析出真实用户，或系统 SSH 在启动前就失败了。

macOS 可手动检查：

```bash
/usr/bin/stat -f %Su /dev/console
/usr/sbin/scutil show State:/Users/ConsoleUser
```

如果自动识别失败，可以显式配置：

```yaml
ssh-user: fa
```

### 日志里 `ssh -D` 很快退出，退出值是 `<nil>`

这通常不是认证失败，而是 OpenSSH 复用了已有 ControlMaster，当前进程成功退出。

当前实现会强制禁用 ControlMaster/ControlPersist/ForkAfterAuthentication，并过滤相关 `ssh-flags`。如果仍出现类似问题，检查是否运行的是最新构建产物。

### Host 别名解析失败

系统 SOCKS 模式不会在 Mihomo 内部解析 Host 别名，而是直接让 OpenSSH 读取实际用户的 `~/.ssh/config`。

请确认：

```bash
sudo -n -u <ssh-user> -H ssh -G <server>
```

或同用户终端下：

```bash
ssh -G <server>
```

能看到正确的 `hostname`、`user`、`identityfile`。

### `Could not resolve hostname <alias>`

说明 OpenSSH 没读到包含该 Host 的配置，常见原因：

- Mihomo 以 `root` 或服务账号运行。
- 自动用户识别失败。
- `ssh-user` 指错用户。
- 目标用户的 `~/.ssh/config` 没有对应 Host。

### UDP / QUIC 不通

OpenSSH dynamic forwarding 是 TCP SOCKS5。UDP、QUIC 等流量不能直接通过 OpenSSH `-D` 转发，需要依赖应用降级 TCP、规则分流或其他代理协议。

## 已移除的旧行为

本实现刻意移除了旧系统 SSH 路径：

- `use-ssh-config-alias`
- Mihomo 内部 `ssh -G` 解析和缓存
- `ssh -W` 管道转发模式
- 把 `port` 当作本地 SOCKS 端口的兼容行为
- Go 层自动读取 OpenSSH identity file 并注入内置 SSH client

现在系统级 SSH 只有一种模型：OpenSSH 托管本地 SOCKS5，Mihomo 复用 SOCKS5 outbound。