## 1. 基础配置 (极简模式 - Zero Config)

由于我们支持了 `ssh -G` 自动探测技术，你的配置现在可以极其精简。**如果你不配置 `username`, `port` 或 `private-key`，Mihomo 会尝试从你的系统 SSH 配置中自动抓取。**

```yaml
proxies:
  - name: "SSH-Zero-Config"
    type: ssh
    server: "my-host-alias"    # 填写 ~/.ssh/config 中的别名
    use-ssh-config-alias: true # 启用开关
    ssh-user: "fa"             # (推荐) 本地用户名
    ssh-flags:                 # (可选) 额外的 SSH 参数
      - "-o"
      - "ControlMaster=no"     # 推荐: 禁用复用，提高稳定性
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
- **原理**：Mihomo 会自动以 `ssh-user` 身份执行“贪婪环境抓取”，加载用户的全量登录变量，并进行**持久化缓存**。
- **自愈与防抖**：得益于底层的 `atomic.Bool` 状态锁与独立的进程监控，即使你在外网遭遇高延迟闪断，系统也会在后台执行**静默的指数退避重连**，绝不会因为单次握手超时而发疯般清空你的环境缓存。只有当真正的身份验证失败或底层命令彻底损坏时，缓存才会触发安全自毁并重新抓取。

### 🧊 极速且纯净的管道
- **进程隔离与复用**：外层长连接隧道完全独立于内层请求的 `Context` 生命期。网络抖动不会引发整个隧道的雪崩式关闭。
- **稳如泰山**：完美支持超过 60 秒的耗时请求，不再会出现空闲断连的情况。内置强制隔离的 `ControlMaster=no` 防多路复用干扰机制。
- **静默登陆**：自动屏蔽 `Last login` 等冗余输出，防止污染 SSH 握手协议。

---

## 3. 常见场景配置

### 场景 A：使用跳板机 (ProxyJump)
只要你在 `~/.ssh/config` 中配置好了 `ProxyJump`，Mihomo 端只需填写目标节点的别名，就像在终端操作一样简单。

### 场景 B：云端隧道 (ProxyCommand)
支持 Cloudflare Tunnel (`cloudflared`) 和 阿里云 ESA 等边缘加速节点。这些工具依赖的身份变量会被 Mihomo 自动捕获。

---

5.  **故障排查**

如果连接不稳定或报错，请查看 Mihomo 日誌，注意以下标识：

1.  **`[SSH-STDERR]`**：这是 SSH 子进程直接抛出的原始错误（如 `Permission denied`, `Could not resolve hostname`，或 `Connection refused`）。这是最直接的诊断信息。
2.  **`[SSH] Attempt X/Y ... failed: context deadline exceeded`** 或 **`i/o timeout`**：说明当前单次网页代理请求等不及了（默认5秒），但这**不代表**整个节点挂了。隧道的重连工作仍在后台不受干扰地进行。一旦连接成功，后续请求将瞬间恢复。
3.  **`[SSH] Process exited with error` / `Cleared all caches`**：发生这种极其罕见的情况，说明底层的 SSH 代理进程遭到致命破坏（例如密钥文件被删、Token 彻底吊销验证失败）。此时需要重新检查你的密钥配置。
4.  **终端先行原则**：
    - 在终端执行 `ssh <alias>`。
    - 确保**无需任何交互/输入密码**就能直接进入远程 Shell。
    - 如果终端都连不上，Mihomo 也无法连接。

---
*Last Updated: 2026-03-05*
