## 1. 基础配置 (极简模式 - Zero Config)

由于我们支持了 `ssh -G` 自动探测技术，你的配置现在可以极其精简。**如果你不配置 `username`, `port` 或 `private-key`，Mihomo 会尝试从你的系统 SSH 配置中自动抓取。**

```yaml
proxies:
  - name: "SSH-Zero-Config"
    type: ssh
    server: "my-host-alias"    # 填写 ~/.ssh/config 中的别名
    use-ssh-config-alias: true # 启用开关
    ssh-user: "fa"             # (推荐) 本地用户名
```

> [!TIP]
> **自动填充逻辑**：
> 1. **用户名**：自动抓取并在内层握手时使用。
> 2. **端口**：如果 Mihomo 没配 `port`，自动从 config 读；如果配了（如 `port: 22`），则优先用 Mihomo 的配置（适用于 frp 映射场景）。
> 3. **私钥**：自动查找 `IdentityFile` 路径并尝试加载。

> [!IMPORTANT]
> **关于双层认证 (Dual-Layer Auth)**
> 虽然外层连接使用了系统 SSH 及其配置好的私钥，但 **Mihomo 配置中仍需提供 `private-key`**。
> - **外层 (系统 SSH)**：利用系统密钥穿透跳板机、完成通道建立。
> - **内层 (Mihomo 协议)**：在通道内发起最终认证。这是安全防范的要求，确保连接者有权代理流量。

---

## 2. 核心特性说明

### 🛡️ 智能环境抓取 (Zero Config)
你无需在 Mihomo 中配置 `PATH` 或手动指定 `cloudflared` 的位置。
- **原理**：Mihomo 会自动以 `ssh-user` 身份执行“贪婪环境抓取”，加载用户的全量登录变量。
- **自愈能力**：如果 `ProxyCommand` 因为 Token 失效报错，缓存会自动清除。你只需再次发起请求，系统会重新抓取最新的环境（如更新后的 Token）。

### 🧊 极速且纯净的管道
- **进程复用**：系统 SSH 子进程被适配为原生的 Socket，支持完整的 **SetDeadline**。
- **稳如泰山**：完美支持超过 60 秒的耗时请求，不再会出现空闲断连的情况。
- **静默登陆**：自动屏蔽 `Last login` 等冗余输出，防止污染 SSH 握手协议。

---

## 3. 常见场景配置

### 场景 A：使用跳板机 (ProxyJump)
只要你在 `~/.ssh/config` 中配置好了 `ProxyJump`，Mihomo 端只需填写目标节点的别名，就像在终端操作一样简单。

### 场景 B：云端隧道 (ProxyCommand)
支持 Cloudflare Tunnel (`cloudflared`) 和 阿里云 ESA 等边缘加速节点。这些工具依赖的身份变量会被 Mihomo 自动捕获。

---

## 4. 故障排查

如果连接不稳定或报错，请查看 Mihomo 日誌，注意以下标识：

1.  **`[SSH-STDERR]`**：这是 SSH 子进程直接抛出的原始错误（如 `Permission denied`, `Could not resolve hostname`）。这是最直接的诊断信息。
2.  **`[SSH] SSH process exited with error`**：说明环境抓取或子进程崩溃。常见于 `ssh-user` 配置错误或密钥文件权限不正确（通常需 600）。
3.  **终端先行原则**：
    - 在终端执行 `ssh <alias>`。
    - 确保**无需任何交互/输入密码**就能直接进入远程 Shell。
    - 如果终端都连不上，Mihomo 也无法连接。

---
*Last Updated: 2026-01-02*
