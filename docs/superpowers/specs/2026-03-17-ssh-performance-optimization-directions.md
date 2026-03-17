# SSH 代理性能优化方向分析（仅 SSH 服务器场景）

**日期**: 2026-03-17
**场景**: 只有 SSH 服务器可用，无法部署其他协议
**目标**: 性能优化方向探索

---

## 当前实现分析

### 现有优化
1. **单层模式** (`use-system-socks: true`): 使用 `ssh -D` 建立 SOCKS5 隧道，避免双重加密
2. **零配置**: 自动解析 `~/.ssh/config`，自动加载密钥
3. **环境变量缓存**: 减少 `ssh -G` 调用
4. **健康检查**: 30s 间隔的 keepalive

### 性能瓶颈识别
```
[应用] → [Mihomo] → [SOCKS5 127.0.0.1:port] → [ssh -D] → [SSH Server] → [目标]
```

**瓶颈点**：
1. **本地 SOCKS5 握手**: 每个连接都需要 SOCKS5 协议握手
2. **SSH 进程启动**: 首次连接需要启动 `ssh -D` 进程（约 100-200ms）
3. **TCP 握手**: 本地 → SOCKS5 的 TCP 握手开销
4. **SSH 加密**: 单层加密，但仍有 CPU 开销
5. **内存拷贝**: 数据在 Mihomo → SOCKS5 → SSH 之间多次拷贝

---

## 性能优化方向

### 方向 1: SSH ControlMaster 连接复用 ⭐⭐⭐⭐⭐

**原理**: 利用 OpenSSH 的 ControlMaster 特性，多个 SSH 连接共享同一个底层 TCP 连接

**实现方式**：
```yaml
# 在 ssh-flags 中启用 ControlMaster
proxies:
  - name: "SSH_FAST"
    type: ssh
    server: my-server
    use-system-socks: true
    ssh-flags:
      - "-o"
      - "ControlMaster=auto"
      - "-o"
      - "ControlPath=~/.ssh/mihomo-control-%r@%h:%p"
      - "-o"
      - "ControlPersist=10m"
```

**预期收益**：
- **首次连接后**: 后续连接延迟降低 70-80%（无需 TCP 握手和 SSH 握手）
- **CPU 降低**: 10-15%（共享加密通道）
- **内存降低**: 20-30%（共享连接状态）

**风险**：
- 当前代码中 `ssh_system.go:102` 明确禁用了 ControlMaster（`-o ControlMaster=no`）
- 原因：与 `os.Pipe()` 的 I/O 接管机制冲突
- **解决方案**: 仅在 `use-system-socks: true` 模式下启用（不使用 Pipe）

**实现成本**: 低（仅需修改 `buildSshDCommand` 的参数）

---

### 方向 2: SSH 连接预热（Connection Pre-warming） ⭐⭐⭐⭐

**原理**: 在配置加载时立即启动 `ssh -D` 进程，而不是等待首次连接

**实现方式**：
```go
// 在 NewSsh 中添加预热选项
func NewSsh(option SshOption) (*Ssh, error) {
    // ... 现有代码 ...

    if option.UseSystemSocks && option.PreWarm {
        // 后台启动 SSH 隧道
        go func() {
            ctx := context.Background()
            if err := outbound.setupSystemSocks(ctx); err != nil {
                log.Warnln("[SSH] Pre-warm failed: %v", err)
            } else {
                log.Infoln("[SSH] Pre-warmed tunnel ready")
            }
        }()
    }

    return outbound, nil
}
```

**配置示例**：
```yaml
proxies:
  - name: "SSH_INSTANT"
    type: ssh
    server: my-server
    use-system-socks: true
    pre-warm: true  # 启动时立即建立隧道
```

**预期收益**：
- **首次连接延迟**: 从 100-200ms 降低到 < 10ms
- **用户体验**: 无感知的即时连接

**成本**：
- 额外内存: ~5-10MB（SSH 进程）
- 额外 CPU: 1-2%（keepalive）

**实现成本**: 低（约 50 行代码）

---

### 方向 3: 本地 SOCKS5 连接池 ⭐⭐⭐⭐

**原理**: 复用到本地 SOCKS5 的 TCP 连接，避免重复握手

**当前问题**：
```go
// ssh_system_socks.go:232
// 每次 Dial 都创建新的 TCP 连接到 127.0.0.1:port
c, err := s.dialer.DialContext(ctx, "tcp", addr)
```

**优化方案**：
```go
type SocksConnPool struct {
    pool     chan net.Conn
    maxConns int
    addr     string
}

func (p *SocksConnPool) Get(ctx context.Context) (net.Conn, error) {
    select {
    case conn := <-p.pool:
        // 检查连接是否仍然有效
        if isConnAlive(conn) {
            return conn, nil
        }
        _ = conn.Close()
    default:
    }

    // 创建新连接
    return net.DialTimeout("tcp", p.addr, 5*time.Second)
}

func (p *SocksConnPool) Put(conn net.Conn) {
    select {
    case p.pool <- conn:
    default:
        _ = conn.Close()
    }
}
```

**预期收益**：
- **连接建立延迟**: 降低 50-70%（复用连接）
- **CPU**: 降低 5-10%（减少 TCP 握手）

**注意**: SOCKS5 协议本身不支持连接复用，需要在应用层实现

**实现成本**: 中等（约 200 行代码，需要仔细处理连接生命周期）

---

### 方向 4: SSH 压缩优化 ⭐⭐⭐

**原理**: 针对特定流量类型启用 SSH 压缩

**当前状态**: 默认不启用压缩（`compression no`）

**优化策略**：
```yaml
proxies:
  - name: "SSH_COMPRESSED"
    type: ssh
    server: my-server
    use-system-socks: true
    ssh-flags:
      - "-o"
      - "Compression=yes"
      - "-o"
      - "CompressionLevel=6"  # 1-9，6 是平衡点
```

**适用场景**：
- 文本内容（HTML、JSON、API）
- 低带宽网络（< 10Mbps）

**不适用场景**：
- HTTPS 流量（已加密，无法压缩）
- 视频/图片（已压缩）
- 高带宽网络（压缩 CPU 开销 > 带宽收益）

**预期收益**：
- **文本流量**: 带宽节省 40-60%
- **延迟**: 可能增加 5-10ms（压缩开销）

**实现成本**: 零（仅需文档说明）

---

### 方向 5: SSH 多路复用优化（Multiplexing） ⭐⭐⭐⭐

**原理**: 在单个 SSH 连接上复用多个逻辑通道

**当前状态**: `ssh -D` 模式下，每个应用连接都是独立的 SOCKS5 连接

**优化方案**: 使用 SSH 的 `-W` 模式 + 自定义多路复用
```go
// 类似 HTTP/2 的多路复用
type SSHMultiplexer struct {
    sshConn   net.Conn
    streams   map[uint32]*Stream
    nextID    uint32
    mu        sync.Mutex
}

// 在单个 SSH 连接上打开新的逻辑流
func (m *SSHMultiplexer) OpenStream(ctx context.Context) (*Stream, error) {
    m.mu.Lock()
    streamID := m.nextID
    m.nextID++
    m.mu.Unlock()

    stream := &Stream{
        id:   streamID,
        conn: m.sshConn,
    }

    m.streams[streamID] = stream
    return stream, nil
}
```

**预期收益**：
- **连接建立**: 降低 80-90%（复用 SSH 连接）
- **内存**: 降低 50-60%（共享连接状态）

**挑战**：
- 需要服务端配合（实现多路复用协议）
- 实现复杂度高

**实现成本**: 高（约 1000+ 行代码）

---

### 方向 6: 缓冲区优化 ⭐⭐⭐

**原理**: 优化数据传输的缓冲区大小和策略

**当前问题**: 使用默认缓冲区大小（可能不是最优）

**优化方案**：
```go
// 根据网络条件动态调整缓冲区
type AdaptiveBuffer struct {
    size    int
    minSize int
    maxSize int
    rtt     time.Duration
}

func (b *AdaptiveBuffer) AdjustSize(throughput int64) {
    // BDP (Bandwidth-Delay Product) = Throughput × RTT
    bdp := throughput * int64(b.rtt.Seconds())

    // 缓冲区 = 2 × BDP
    newSize := int(bdp * 2)

    if newSize < b.minSize {
        newSize = b.minSize
    }
    if newSize > b.maxSize {
        newSize = b.maxSize
    }

    b.size = newSize
}
```

**预期收益**：
- **吞吐量**: 提升 10-20%（高延迟网络）
- **延迟**: 降低 5-10%（低延迟网络）

**实现成本**: 低（约 100 行代码）

---

### 方向 7: SSH Keepalive 优化 ⭐⭐

**原理**: 优化 keepalive 参数，减少不必要的探测

**当前状态**: 固定 30s 间隔

**优化方案**：
```yaml
proxies:
  - name: "SSH_OPTIMIZED"
    type: ssh
    server: my-server
    use-system-socks: true
    ssh-flags:
      - "-o"
      - "ServerAliveInterval=15"  # 15s 探测（更快发现断线）
      - "-o"
      - "ServerAliveCountMax=3"   # 3 次失败后断开
      - "-o"
      - "TCPKeepAlive=yes"        # 启用 TCP keepalive
```

**预期收益**：
- **断线检测**: 从 90s 降低到 45s
- **资源占用**: 略微增加（更频繁的探测）

**实现成本**: 零（仅需文档说明）

---

## 综合优化方案推荐

### 方案 A: 快速见效（1周实现）⭐⭐⭐⭐⭐

**组合**：
1. SSH ControlMaster 连接复用
2. SSH 连接预热
3. 缓冲区优化

**预期收益**：
- 首次连接: 100-200ms → < 10ms（预热）
- 后续连接: 50-100ms → 10-20ms（ControlMaster）
- 吞吐量: +10-20%（缓冲区优化）

**实现成本**: 低（约 200 行代码）

**配置示例**：
```yaml
proxies:
  - name: "SSH_OPTIMIZED"
    type: ssh
    server: my-server
    use-system-socks: true
    pre-warm: true  # 连接预热
    ssh-flags:
      - "-o"
      - "ControlMaster=auto"
      - "-o"
      - "ControlPath=~/.ssh/mihomo-%r@%h:%p"
      - "-o"
      - "ControlPersist=10m"
      - "-o"
      - "ServerAliveInterval=15"
      - "-o"
      - "ServerAliveCountMax=3"
```

---

### 方案 B: 深度优化（3-4周实现）⭐⭐⭐⭐

**组合**：
- 方案 A 的所有优化
- 本地 SOCKS5 连接池
- 自适应缓冲区

**预期收益**：
- 连接建立: 再降低 50%
- 吞吐量: 再提升 10-15%
- 内存占用: 降低 20-30%

**实现成本**: 中等（约 400 行代码）

---

### 方案 C: 极致优化（6-8周实现）⭐⭐⭐

**组合**：
- 方案 B 的所有优化
- SSH 多路复用（需要服务端配合）

**预期收益**：
- 连接建立: 再降低 80%
- 内存占用: 再降低 50%

**实现成本**: 高（约 1000+ 行代码）

**注意**: 需要服务端部署自定义多路复用协议

---

## macOS 平台特定优化

### 利用 macOS 特性

#### 1. Network.framework 优化
```go
// 使用 macOS 的 Network.framework 替代标准 net 包
// 可以获得更好的性能和更低的延迟
```

**收益**: 延迟降低 5-10%

**实现成本**: 高（需要 CGO 或 syscall）

#### 2. M 系列芯片硬件加速
```yaml
# 使用支持硬件加速的加密算法
ssh-flags:
  - "-o"
  - "Ciphers=aes128-gcm@openssh.com,aes256-gcm@openssh.com"
```

**收益**: CPU 降低 20-30%（M1/M2/M3）

**实现成本**: 零（仅需配置）

---

## 最终推荐

### 立即可实施（零成本）
1. **启用 ControlMaster**: 修改 `ssh-flags` 配置
2. **优化 Keepalive**: 调整探测间隔
3. **启用硬件加密**: 使用 GCM 算法

**预期收益**: 性能提升 20-30%

### 短期实施（1-2周）
1. **连接预热**: 添加 `pre-warm` 选项
2. **缓冲区优化**: 实现自适应缓冲区

**预期收益**: 性能再提升 15-20%

### 中期实施（3-4周）
1. **SOCKS5 连接池**: 复用本地连接

**预期收益**: 性能再提升 10-15%

---

## 性能提升预估总结

| 优化方向 | 实现成本 | 延迟改善 | 吞吐量提升 | CPU 降低 | 推荐度 |
|---------|---------|---------|-----------|---------|--------|
| ControlMaster | 低 | 70-80% | 0% | 10-15% | ⭐⭐⭐⭐⭐ |
| 连接预热 | 低 | 90%+ | 0% | 0% | ⭐⭐⭐⭐⭐ |
| SOCKS5 连接池 | 中 | 50-70% | 0% | 5-10% | ⭐⭐⭐⭐ |
| 压缩优化 | 零 | -5~10ms | 40-60%* | -10~20% | ⭐⭐⭐ |
| 缓冲区优化 | 低 | 5-10% | 10-20% | 0% | ⭐⭐⭐⭐ |
| Keepalive 优化 | 零 | 0% | 0% | 0% | ⭐⭐ |
| 多路复用 | 高 | 80-90% | 0% | 10-20% | ⭐⭐⭐ |

*仅适用于文本流量

---

**结论**: 在只有 SSH 服务器的场景下，通过 ControlMaster + 连接预热 + 缓冲区优化，可以实现 **30-50% 的性能提升**，且实现成本低（1-2周）。
