# SSH -W 单层通道代理方案研究

**日期**: 2026-03-17
**核心问题**: 能否仅用 `ssh -W` 实现单层网络代理（不需要双层加密）？

---

## 一、SSH -W 工作原理

### 命令格式
```bash
ssh -W host:port user@server
```

### 功能说明
- **作用**: 将本地 stdin/stdout 直接转发到远程 `host:port`
- **用途**: 通常用作 ProxyCommand，实现 SSH 跳板
- **特点**:
  - 单个连接只能转发到一个目标地址
  - 每次连接都需要指定目标地址
  - stdin/stdout 是原始 TCP 流，没有协议封装

### 当前实现（双层模式）
```
[Mihomo] → [ssh -W localhost:22] → [SSH Server:22] → [Go SSH Client] → [目标]
         ↑ 物理层（系统SSH）              ↑ 逻辑层（Go SSH）
```

**问题**: 双重加密（系统 SSH + Go SSH）

---

## 二、单层 ssh -W 代理的核心挑战

### 挑战 1: 动态目标地址问题

**问题**：
```bash
# ssh -W 需要在启动时指定目标
ssh -W target.com:443 user@server

# 但代理需要支持动态目标
curl https://google.com  # 目标 1
curl https://github.com  # 目标 2
```

**ssh -W 的限制**: 一个进程只能连接一个目标，无法动态切换

### 挑战 2: 缺少协议层

**ssh -D (SOCKS5)** 提供了协议层：
```
[应用] → [SOCKS5 握手: 告诉目标地址] → [ssh -D] → [目标]
```

**ssh -W** 没有协议层：
```
[应用] → [原始 TCP 流] → [ssh -W ???] → [目标]
                                ↑ 如何知道目标地址？
```

### 挑战 3: 连接复用问题

**ssh -D**: 单个进程处理多个连接
```
ssh -D 1080  # 一个进程
→ 连接 1: google.com:443
→ 连接 2: github.com:443
→ 连接 3: twitter.com:443
```

**ssh -W**: 每个目标需要独立进程
```
ssh -W google.com:443   # 进程 1
ssh -W github.com:443   # 进程 2
ssh -W twitter.com:443  # 进程 3
```

---

## 三、可能的解决方案

### 方案 1: 动态 ssh -W 进程池 ⭐⭐

**思路**: 为每个目标动态启动 `ssh -W` 进程

```go
type DynamicSSHW struct {
    processes map[string]*exec.Cmd // key: "host:port"
    mu        sync.RWMutex
}

func (d *DynamicSSHW) Dial(ctx context.Context, target string) (net.Conn, error) {
    d.mu.Lock()
    defer d.mu.Unlock()

    // 检查是否已有到该目标的进程
    if cmd, exists := d.processes[target]; exists {
        // 复用现有进程？不行，ssh -W 的 stdin/stdout 已被占用
    }

    // 启动新的 ssh -W 进程
    cmd := exec.Command("ssh", "-W", target, "user@server")
    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    return &sshWConn{stdin: stdin, stdout: stdout, cmd: cmd}, nil
}
```

**问题**：
- ❌ 每个连接一个进程（资源消耗大）
- ❌ 无法复用 SSH 连接（每次都要握手）
- ❌ 进程管理复杂

**结论**: 不可行，性能比 `ssh -D` 更差

---

### 方案 2: SSH -W + 自定义多路复用协议 ⭐⭐⭐⭐

**思路**: 在服务端实现一个多路复用代理，通过单个 `ssh -W` 连接传输多个逻辑流

#### 架构设计
```
[Mihomo] → [ssh -W proxy-server:9999] → [SSH Server] → [多路复用代理] → [目标]
           ↑ 单个 SSH 连接                              ↑ 自定义协议
```

#### 协议设计
```
数据包格式:
[4字节 StreamID][2字节 命令][2字节 长度][数据]

命令类型:
- 0x01: CONNECT (建立新流)
- 0x02: DATA (传输数据)
- 0x03: CLOSE (关闭流)

示例:
CONNECT: [00 00 00 01][00 01][00 0E]google.com:443
DATA:    [00 00 00 01][00 02][00 10]<16字节数据>
CLOSE:   [00 00 00 01][00 03][00 00]
```

#### 实现代码
```go
// 客户端（Mihomo）
type SSHWMultiplexer struct {
    sshConn   net.Conn        // ssh -W 连接
    streams   map[uint32]*Stream
    nextID    uint32
    mu        sync.Mutex
}

func (m *SSHWMultiplexer) OpenStream(target string) (*Stream, error) {
    m.mu.Lock()
    streamID := m.nextID
    m.nextID++
    m.mu.Unlock()

    // 发送 CONNECT 命令
    packet := encodePacket(streamID, CMD_CONNECT, []byte(target))
    if _, err := m.sshConn.Write(packet); err != nil {
        return nil, err
    }

    stream := &Stream{
        id:   streamID,
        conn: m.sshConn,
        mu:   m.mu,
    }

    m.streams[streamID] = stream
    return stream, nil
}

// 服务端（需要部署）
type MultiplexProxy struct {
    listener net.Listener
}

func (p *MultiplexProxy) handleConnection(conn net.Conn) {
    streams := make(map[uint32]net.Conn)

    for {
        // 读取数据包
        streamID, cmd, data, err := readPacket(conn)
        if err != nil {
            break
        }

        switch cmd {
        case CMD_CONNECT:
            target := string(data)
            targetConn, err := net.Dial("tcp", target)
            if err != nil {
                sendError(conn, streamID, err)
                continue
            }
            streams[streamID] = targetConn

            // 启动双向转发
            go copyData(conn, targetConn, streamID)

        case CMD_DATA:
            if targetConn, ok := streams[streamID]; ok {
                targetConn.Write(data)
            }

        case CMD_CLOSE:
            if targetConn, ok := streams[streamID]; ok {
                targetConn.Close()
                delete(streams, streamID)
            }
        }
    }
}
```

#### 优势
- ✅ 单个 SSH 连接支持多个目标
- ✅ 连接复用（类似 HTTP/2）
- ✅ 单层加密（仅 SSH）
- ✅ 性能优于 `ssh -D`（减少 SOCKS5 握手）

#### 劣势
- ❌ 需要服务端部署自定义代理
- ❌ 实现复杂度高（约 800-1000 行）
- ❌ 需要维护自定义协议

**结论**: 可行，但需要服务端配合

---

### 方案 3: SSH -W + HTTP CONNECT 隧道 ⭐⭐⭐⭐⭐

**思路**: 在服务端部署 HTTP CONNECT 代理，通过 `ssh -W` 连接到该代理

#### 架构设计
```
[Mihomo] → [ssh -W proxy:3128] → [SSH Server] → [Squid/Privoxy] → [目标]
           ↑ 单个 SSH 连接                        ↑ 标准 HTTP 代理
```

#### 实现方式
```go
// 客户端（Mihomo）
type SSHWHTTPProxy struct {
    sshConn net.Conn // ssh -W proxy:3128
}

func (p *SSHWHTTPProxy) Dial(ctx context.Context, target string) (net.Conn, error) {
    // 发送 HTTP CONNECT 请求
    req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
    if _, err := p.sshConn.Write([]byte(req)); err != nil {
        return nil, err
    }

    // 读取响应
    reader := bufio.NewReader(p.sshConn)
    resp, err := http.ReadResponse(reader, nil)
    if err != nil {
        return nil, err
    }

    if resp.StatusCode != 200 {
        return nil, fmt.Errorf("proxy error: %s", resp.Status)
    }

    // 返回隧道连接
    return &tunnelConn{conn: p.sshConn}, nil
}
```

#### 服务端配置（Squid）
```conf
# /etc/squid/squid.conf
http_port 3128

# 允许 CONNECT 到所有端口
acl SSL_ports port 443
acl CONNECT method CONNECT
http_access allow CONNECT SSL_ports
http_access allow localhost
```

#### 优势
- ✅ 使用标准协议（HTTP CONNECT）
- ✅ 服务端部署简单（Squid/Privoxy）
- ✅ 单层加密（仅 SSH）
- ✅ 支持连接复用（单个 SSH 连接）
- ✅ 实现简单（约 200 行）

#### 劣势
- ❌ 需要服务端部署 HTTP 代理
- ⚠️ HTTP CONNECT 有协议开销（但很小）

**结论**: 最推荐的方案

---

### 方案 4: SSH -W + SOCKS5 代理 ⭐⭐⭐⭐

**思路**: 在服务端部署 SOCKS5 代理，通过 `ssh -W` 连接

#### 架构设计
```
[Mihomo] → [ssh -W socks:1080] → [SSH Server] → [SOCKS5 Server] → [目标]
           ↑ 单个 SSH 连接                        ↑ 标准 SOCKS5
```

#### 实现方式
```go
// 客户端（Mihomo）
type SSHWSOCKSProxy struct {
    sshConn net.Conn // ssh -W socks-server:1080
}

func (p *SSHWSOCKSProxy) Dial(ctx context.Context, target string) (net.Conn, error) {
    // 执行 SOCKS5 握手
    if err := socks5Handshake(p.sshConn, target); err != nil {
        return nil, err
    }

    return &tunnelConn{conn: p.sshConn}, nil
}
```

#### 服务端部署
```bash
# 使用 dante 或 shadowsocks-libev
apt install dante-server

# /etc/danted.conf
internal: 0.0.0.0 port = 1080
external: eth0
method: none
user.privileged: root
user.unprivileged: nobody

client pass {
    from: 0.0.0.0/0 to: 0.0.0.0/0
}

socks pass {
    from: 0.0.0.0/0 to: 0.0.0.0/0
}
```

#### 优势
- ✅ 使用标准协议（SOCKS5）
- ✅ 服务端部署简单
- ✅ 单层加密（仅 SSH）
- ✅ 支持 UDP（SOCKS5 UDP ASSOCIATE）
- ✅ 实现简单（约 150 行）

#### 劣势
- ❌ 需要服务端部署 SOCKS5 代理
- ⚠️ 与当前 `ssh -D` 类似（但是单层）

**结论**: 可行，但与 `ssh -D` 重复

---

## 四、方案对比

| 方案 | 单层加密 | 连接复用 | 服务端要求 | 实现复杂度 | 性能 | 推荐度 |
|------|---------|---------|-----------|-----------|------|--------|
| 动态 ssh -W | ✅ | ❌ | 仅 SSH | 中 | ⭐⭐ | ⭐⭐ |
| 自定义多路复用 | ✅ | ✅ | SSH + 自定义代理 | 高 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ |
| HTTP CONNECT | ✅ | ✅ | SSH + HTTP 代理 | 低 | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ |
| SOCKS5 | ✅ | ✅ | SSH + SOCKS5 | 低 | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ |
| 当前 ssh -D | ✅ | ✅ | 仅 SSH | 零 | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ |

---

## 五、深入研究方向

### 方向 1: SSH -W + HTTP CONNECT（最推荐）⭐⭐⭐⭐⭐

**为什么推荐**：
1. 标准协议，兼容性好
2. 服务端部署简单（Squid 一行命令）
3. 实现简单（200 行代码）
4. 性能优秀

**实现步骤**：
1. 在服务端部署 Squid/Privoxy
2. 在 Mihomo 中实现 HTTP CONNECT 客户端
3. 通过 `ssh -W proxy:3128` 建立隧道
4. 复用单个 SSH 连接处理多个目标

**预期收益**：
- 单层加密（vs 双层）
- 连接复用（vs 动态 ssh -W）
- 性能与 `ssh -D` 相当或更好

**实现时间**: 1-2 周

---

### 方向 2: 自定义多路复用协议（高性能）⭐⭐⭐⭐

**为什么值得研究**：
1. 性能最优（无协议开销）
2. 完全可控（自定义协议）
3. 可扩展（支持更多特性）

**实现步骤**：
1. 设计多路复用协议（参考 HTTP/2）
2. 实现客户端（Mihomo）
3. 实现服务端代理
4. 性能测试和优化

**预期收益**：
- 性能比 HTTP CONNECT 高 10-20%
- 更灵活的流控和优先级

**实现时间**: 3-4 周

---

### 方向 3: SSH ControlMaster + ssh -W（最简单）⭐⭐⭐⭐⭐

**核心思路**: 利用 ControlMaster 复用 SSH 连接，每个目标仍然是独立的 `ssh -W` 进程，但共享底层 TCP 连接

```bash
# 主连接
ssh -M -S /tmp/ssh-control -fN user@server

# 复用连接（几乎零延迟）
ssh -S /tmp/ssh-control -W google.com:443 user@server
ssh -S /tmp/ssh-control -W github.com:443 user@server
```

**优势**：
- ✅ 无需服务端修改
- ✅ 利用 SSH 原生特性
- ✅ 实现简单（100 行代码）
- ✅ 连接建立延迟极低（< 1ms）

**劣势**：
- ⚠️ 每个目标仍需独立进程（但共享 SSH 连接）
- ⚠️ 进程管理复杂度

**实现时间**: 1 周

---

## 六、最终推荐

### 短期方案（1-2周）: SSH -W + HTTP CONNECT

**理由**：
1. 标准协议，稳定可靠
2. 服务端部署简单
3. 实现成本低
4. 性能优秀

**实现路径**：
```
Week 1: 实现 HTTP CONNECT 客户端 + ssh -W 集成
Week 2: 测试、优化、文档
```

### 中期方案（3-4周）: 自定义多路复用

**理由**：
1. 性能最优
2. 完全可控
3. 可扩展性强

**实现路径**：
```
Week 1: 协议设计
Week 2: 客户端实现
Week 3: 服务端实现
Week 4: 测试、优化
```

### 长期方案: SSH ControlMaster 优化

**理由**：
1. 无需服务端修改
2. 利用 SSH 原生特性
3. 与现有方案兼容

---

## 七、关键发现

### ssh -W 的本质限制
- **单目标**: 一个进程只能连接一个目标
- **无协议层**: 需要外部协议提供目标地址信息

### 突破限制的方法
1. **服务端代理**: 在服务端实现协议层（HTTP/SOCKS5/自定义）
2. **ControlMaster**: 复用 SSH 连接，降低进程开销
3. **进程池**: 管理多个 ssh -W 进程

### 最优解
**SSH -W + HTTP CONNECT + ControlMaster**：
- 单层加密
- 连接复用
- 标准协议
- 低延迟
- 易部署

---

**结论**: `ssh -W` 可以实现单层代理，但需要服务端配合。推荐使用 **SSH -W + HTTP CONNECT** 方案，兼顾性能、稳定性和实现成本。
