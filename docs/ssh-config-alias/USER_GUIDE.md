# SSH Config Alias Support - 使用指南 (User Guide)

本功能允许 Mihomo 直接使用你系统中的 SSH 配置文件（`~/.ssh/config`），从而支持 `ProxyJump`（跳板机）和 `ProxyCommand`（如 Cloudflare Access）等高级功能。

## 1. 基础配置

在你的 Mihomo 配置文件（`config.yaml`）中，按如下方式配置 SSH 代理节点：

```yaml
proxies:
  - name: "SSH-System-Proxy"
    type: ssh
    server: "my-host-alias"    # 这里填写你在 ~/.ssh/config 中定义的 Host 名称
    port: 22                   # 通常填 22，实际连接端口由 SSH config 决定
    username: "root"           # 这里的用户名为目标机器的登录用户（可选，部分场景需匹配）
    # private-key: ...         # 不需要填写私钥，会自动读取系统配置
    
    # === 关键配置 ===
    use-ssh-config-alias: true # 启用系统 SSH 配置支持
    ssh-user: "fa"             # (推荐) 指定你电脑上原本能成功 SSH 的用户名
```

## 2. 常见场景配置

### 场景 A：使用跳板机 (ProxyJump)

假设你的 `~/.ssh/config` 配置如下：
```ssh
Host jump-server
  HostName jump.example.com
  User admin

Host internal-server
  HostName 10.0.0.5
  User dev
  ProxyJump jump-server    # 通过跳板机连接
```

**Mihomo 配置：**
```yaml
proxies:
  - name: "Internal-Via-Jump"
    type: ssh
    server: "internal-server"   # 直接填最终目标的 Host 别名
    use-ssh-config-alias: true
    ssh-user: "your-local-username"
```

### 场景 B：使用 Cloudflare Access (ProxyCommand)

假设 SSH 配置如下：
```ssh
Host cf-protected-server
  HostName ssh.example.com
  ProxyCommand cloudflared access ssh --hostname %h
```

**Mihomo 配置：**
```yaml
proxies:
  - name: "CF-Access-SSH"
    type: ssh
    server: "cf-protected-server"
    use-ssh-config-alias: true
    ssh-user: "your-local-username"
```

## 3. 配置项说明

| 选项 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `use-ssh-config-alias` | bool | 是 | 设置为 `true` 以启用此功能。 |
| `port` | int | 否 | **重要**：请填写服务器**内部**监听的端口（通常是 **22**）。即使你在 `~/.ssh/config` 中配置了 `Port 10022` 这种外部映射端口，这里也只需填 22。因为外部连接是由系统 SSH 处理的，而这个 port 是用于隧道建立后的内部数据传输。 |
| `ssh-user` | string | 推荐 | 指定执行 `ssh` 命令的本地系统用户名。Mihomo 可能会以 root 运行，此选项确保它切换回你的用户身份去读取正确的 keys 和 config。 |
| `ssh-user-home` | string | 否 | 指定用户主目录（辅助自动检测），一般只需配置 `ssh-user` 即可。 |

### ⚠️ 关于端口配置的重要提示

**即使使用了非标准 SSH 端口，这里也应该填 22！**

- **场景**：目标服务器（容器）外部映射端口为 `12202`，内部是 `22`。
- **SSH Config**：必须配置 `Port 12202`，系统 SSH 获取以此建立连接。
- **Mihomo Config**：必须配置 `port: 22`。Mihomo 会请求 "请转发流量到 localhost:22"。如果填了 12202，会被拒绝（因为容器内部并没有监听 12202）。

## 4. 故障排查

如果连接失败，请检查：

1. **终端测试**：首先确保在终端中执行 `ssh <server>` 能成功连接且**不需要输入密码**（使用密钥认证）。
2. **sudo 权限**：Mihomo 需要有权限执行 `sudo -u <user> -i`。如果 Mihomo 是以普通用户运行的，确保该用户有 sudo 权限或直接即是目标用户。
3. **Cloudflared 路径**：如果使用 `ProxyCommand`，确保相关命令（如 `cloudflared`）在用户的 `PASS` 环境变量中。本功能已自动添加 `-i` 参数来加载用户环境，一般能正常工作。
