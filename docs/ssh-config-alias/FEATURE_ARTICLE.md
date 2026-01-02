# 深度集成 SSH Config：让 Mihomo 完美复用 ~/.ssh/config 魔法，告别繁琐代理链！

## 🔗 项目信息

- **Fork 仓库**: [https://github.com/iuin8/mihomo](https://github.com/iuin8/mihomo)
- **分支**: (请确保查看包含 `ssh-config-alias` 功能的分支, 例如: `ssh_system_v1.19.18`)
- **核心文件**: `adapter/outbound/ssh.go`, `adapter/outbound/ssh_system.go`

---

## 📖 需求背景

在复杂的网络环境中，用户往往已经维护了一套完善的 SSH 配置文件 (`~/.ssh/config`)。原生 Mihomo SSH 适配器无法直接利用这些配置（如 `ProxyJump`、`ProxyCommand`），导致用户必须手动配置繁琐的代理链。

**本项目的目标**：让 Mihomo 可以直接“借用”系统 SSH 客户端的能力，只需填一个主机别名，剩下的认证、跳转全交给系统 SSH 处理。

---

## ✨ 核心特性

### 1. 深度集成 SSH Config
支持系统 SSH 客户端的所有指令，包括但不限于：
- ✅ `ProxyJump` / `JumpHost` (跳板机级联)
- ✅ `ProxyCommand` (集成 `cloudflared`, `nc` 等工具)
- ✅ `IdentityFile`, `Include`, `Match` 逻辑

### 2. 贪婪环境抓取 (Greedy Environment Capture)
针对 `ProxyCommand` 依赖环境变量的问题，实现了智能抓取：
- **零配置**：自动捕获用户的 `PATH` 和 Token 变量。
- **自愈能力**：Token 过期导致连接失败时，系统会自动刷新环境缓存，实现无感恢复。

### 3. 原生级稳定性 (Native Deadlines)
通过手动接管子进程管道并透传文件描述符，实现了完整的 **Deadline** 支持：
- **稳如原生**：长达 60s+ 的业务请求不再会有意外断连。
- **资源回收**：与 Mihomo 连接池生命周期完美契合。

---

## 🚀 快速开始

### 配置示例

```yaml
proxies:
  - name: "My-Stable-SSH"
    type: ssh
    server: "alias-in-ssh-config"
    port: 22
    username: "dev"
    private-key: "..."          # 仍需提供，用于内层协议安全认证
    use-ssh-config-alias: true  # 开启本功能
    ssh-user: "your-local-user" # 推荐设置，用于权限切换
```

---

## 🛠 原理简述 (双层隧道设计)

本功能采用了创新的 **双层隧道 (Dual-Layer)** 架构：

1.  **外层 (Outer Layer)**：调用系统 `ssh -W` 指令。利用你系统中的密钥和配置穿透跳板机，建立一条原始的 TCP 通道。
2.  **内层 (Inner Layer)**：Mihomo 在建立好的 TCP 通道内再次发起 SSH 协议认证。

> [!TIP]
> **为什么要双层认证？**
> 这种设计实现了完美的解耦：系统 SSH 负责解决“怎么连上服务器”，而 Mihomo 负责解决“怎么通过服务器代理流量”，且安全性得到了最大化保障。

---

## 📚 参考文档

* [实现原理](./IMPLEMENTATION.md)
* [使用指南](./USER_GUIDE.md)
* [SSH Config 官方文档](https://man.openbsd.org/ssh_config)
* [fork from MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo)

*Last Updated: 2026-01-02*
