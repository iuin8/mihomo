# SSH 系统代理极致性能与稳定性增强设计

**日期**: 2026-03-16
**版本**: v1.0
**目标**: 在现有 SSH 系统代理基础上，实现极致性能优化和岩石般的稳定性

---

## 一、设计目标

### 性能目标
- 降低首包延迟（TTFB）至 < 50ms（当前约 100-200ms）
- 提升吞吐量 30-50%（通过零拷贝和多路复用）
- 减少 CPU 占用 20-30%（通过优化 I/O 路径）
- 支持 10,000+ 并发连接而不降级

### 稳定性目标
- 实现 99.99% 连接成功率（通过多路径冗余）
- 断线恢复时间 < 1s（通过预测性切换）
- 零数据丢失（通过缓冲区管理）
- 优雅处理所有边缘场景

### 功能目标
- UDP 支持（通过封装或实验性协议）
- 连接多路复用（单 SSH 会话承载多逻辑连接）
- 压缩算法优化（动态选择最优压缩）
- QUIC over SSH 实验性支持

### 用户体验目标
- 实时性能监控面板
- 连接质量可视化
- 自动性能报告生成
- 零配置达到最优性能

---

## 二、核心架构设计

### 2.1 零拷贝 I/O 引擎

**问题分析**：
当前实现使用 `os.Pipe()` 进行数据传输，每次数据传输都需要：
1. 用户空间 → 内核空间（write）
2. 内核空间 → 用户空间（read）
3. 用户空间 → 内核空间（再次 write）

这导致了多次内存拷贝和上下文切换。

**解决方案**：
```go
// 新增零拷贝传输层
type ZeroCopyTransport struct {
    // Linux: splice/sendfile
    // macOS: sendfile (limited support)
    // Windows: TransmitFile
    useSplice bool
    bufPool   *sync.Pool // 复用缓冲区
}

// 实现策略
func (z *ZeroCopyTransport) Transfer(dst, src net.Conn) error {
    if z.useSplice && supportsSplice() {
        return z.spliceTransfer(dst, src)
    }
    return z.bufferedTransfer(dst, src) // fallback
}
```

**预期收益**：
- 减少 40-60% 的内存拷贝
- 降低 20-30% 的 CPU 使用
- 提升 30-50% 的吞吐量

---

### 2.2 连接多路复用（SSH Multiplexing）

**问题分析**：
当前每个逻辑连接都需要：
- 建立独立的 SSH 会话（双层模式）或 SOCKS5 连接（单层模式）
- 独立的 TCP 握手和 SSH 握手开销
- 无法共享已建立的加密通道

**解决方案**：
```go
// SSH 多路复用管理器
type SSHMultiplexer struct {
    baseSession *ssh.Client
    channels    chan *multiplexedConn
    maxStreams  int // 单会话最大流数量（默认 1000）
}

// 虚拟连接（复用底层 SSH 会话）
type multiplexedConn struct {
    stream ssh.Channel
    id     uint32
}

// 实现逻辑
func (m *SSHMultiplexer) Dial(ctx context.Context, addr string) (net.Conn, error) {
    // 1. 从池中获取或创建新的 SSH 会话
    session := m.getOrCreateSession(ctx)

    // 2. 在会话上打开新的 channel（类似 HTTP/2 stream）
    stream, reqs, err := session.OpenChannel("direct-tcpip", marshal(addr))
    if err != nil {
        return nil, err
    }

    go ssh.DiscardRequests(reqs)
    return &multiplexedConn{stream: stream}, nil
}
```

**预期收益**：
- 首包延迟降低 70-80%（复用已建立的会话）
- 减少 TCP 握手开销
- 提升并发连接处理能力

---

### 2.3 多路径冗余与智能切换

**问题分析**：
当前单一连接路径，一旦出现问题：
- 需要等待超时才能检测到故障
- 重连需要完整的握手流程
- 用户感知明显的延迟

**解决方案**：
```go
// 多路径管理器
type MultiPathManager struct {
    paths []*SSHPath // 多条独立的 SSH 连接
    selector PathSelector // 路径选择策略
}

type SSHPath struct {
    conn      *ssh.Client
    latency   time.Duration // 实时延迟
    lossRate  float64       // 丢包率
    bandwidth int64         // 可用带宽
    score     float64       // 综合评分
}

// 智能选择策略
func (m *MultiPathManager) SelectPath() *SSHPath {
    // 1. 实时评分（延迟 40% + 丢包率 30% + 带宽 30%）
    // 2. 选择评分最高的路径
    // 3. 如果主路径评分下降 > 30%，立即切换
    return m.selector.BestPath(m.paths)
}

// 断线预测
func (p *SSHPath) PredictFailure() bool {
    // 基于历史数据预测即将断线
    // 1. 延迟突然增加 3x
    // 2. 连续 3 次 keepalive 超时
    // 3. 丢包率 > 10%
    return p.latency > p.baseline*3 ||
           p.consecutiveTimeouts > 3 ||
           p.lossRate > 0.1
}
```

**预期收益**：
- 连接成功率提升至 99.99%
- 断线恢复时间 < 1s
- 用户无感知的路径切换

---

### 2.4 UDP 支持方案

**问题分析**：
SSH 原生不支持 UDP，当前完全无法处理 UDP 流量。

**解决方案（三选一）**：

#### 方案 A：UDP over TCP 封装（推荐）
```go
// UDP 封装协议
type UDPOverTCP struct {
    tcpConn net.Conn
    buffer  *packetBuffer
}

// 数据包格式：[2字节长度][UDP payload]
func (u *UDPOverTCP) WritePacket(data []byte) error {
    length := uint16(len(data))
    header := []byte{byte(length >> 8), byte(length)}
    _, err := u.tcpConn.Write(append(header, data...))
    return err
}
```

#### 方案 B：SOCKS5 UDP ASSOCIATE 扩展
```go
// 扩展 SOCKS5 协议支持 UDP
// 需要服务端配合（sshd 配置或中间代理）
func (s *Ssh) DialUDP(ctx context.Context, addr string) (net.PacketConn, error) {
    // 1. 建立 TCP 控制连接
    // 2. 发送 UDP ASSOCIATE 请求
    // 3. 获取 UDP relay 地址
    // 4. 返回 PacketConn 接口
}
```

#### 方案 C：QUIC over SSH（实验性）
```go
// 在 SSH 隧道内运行 QUIC 协议
type QUICOverSSH struct {
    sshConn net.Conn
    quicSession quic.Session
}
```

**推荐方案 A**，因为：
- 无需服务端修改
- 兼容性最好
- 实现复杂度适中

**预期收益**：
- 支持 QUIC、DNS、游戏等 UDP 应用
- 无需用户手动配置 fallback

---

### 2.5 压缩算法优化

**问题分析**：
SSH 支持多种压缩算法（zlib、zlib@openssh.com、none），但当前使用默认配置，未根据数据特征动态选择。

**解决方案**：
```go
// 智能压缩选择器
type CompressionSelector struct {
    detector *DataTypeDetector
}

func (c *CompressionSelector) SelectAlgorithm(sample []byte) string {
    dataType := c.detector.Detect(sample)

    switch dataType {
    case DataTypeText, DataTypeJSON:
        return "zlib@openssh.com" // 高压缩比
    case DataTypeVideo, DataTypeEncrypted:
        return "none" // 已压缩或加密，跳过
    case DataTypeBinary:
        return "zlib" // 平衡模式
    default:
        return "zlib@openssh.com"
    }
}

// 数据类型检测（基于前 1KB 样本）
func (d *DataTypeDetector) Detect(sample []byte) DataType {
    // 1. 熵分析（高熵 = 已压缩/加密）
    // 2. 特征匹配（HTTP headers, JSON, etc.）
    // 3. 文件头识别（magic bytes）
}
```

**预期收益**：
- 文本数据压缩率提升 40-60%
- 避免对已压缩数据的无效压缩
- 降低 CPU 占用

---

## 三、可靠性增强

### 3.1 断路器模式

```go
// 断路器（防止级联故障）
type CircuitBreaker struct {
    state        State // Closed, Open, HalfOpen
    failureCount int
    threshold    int // 失败阈值
    timeout      time.Duration
}

func (cb *CircuitBreaker) Call(fn func() error) error {
    if cb.state == Open {
        if time.Since(cb.openedAt) > cb.timeout {
            cb.state = HalfOpen
        } else {
            return ErrCircuitOpen
        }
    }

    err := fn()
    if err != nil {
        cb.failureCount++
        if cb.failureCount >= cb.threshold {
            cb.state = Open
            cb.openedAt = time.Now()
        }
    } else {
        cb.failureCount = 0
        cb.state = Closed
    }
    return err
}
```

### 3.2 自适应超时

```go
// 基于历史数据动态调整超时
type AdaptiveTimeout struct {
    history []time.Duration
    p99     time.Duration // 99分位延迟
}

func (a *AdaptiveTimeout) GetTimeout() time.Duration {
    // 超时 = P99 * 2 + 抖动缓冲
    return a.p99*2 + 500*time.Millisecond
}
```

### 3.3 连接健康检查增强

```go
// 增强的健康检查
func (s *Ssh) enhancedHealthCheck(client *ssh.Client) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    metrics := &HealthMetrics{}

    for {
        select {
        case <-ticker.C:
            start := time.Now()
            _, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
            latency := time.Since(start)

            metrics.Update(latency, err)

            // 预测性切换
            if metrics.Score() < 0.5 {
                log.Warnln("[SSH] Connection quality degraded, preparing failover")
                s.prepareFailover()
            }
        }
    }
}
```

---

## 四、性能监控与可视化

### 4.1 Metrics 暴露

```go
// Prometheus metrics
var (
    sshConnectionDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "ssh_connection_duration_seconds",
            Help: "SSH connection duration",
        },
        []string{"server", "status"},
    )

    sshBytesTransferred = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "ssh_bytes_transferred_total",
            Help: "Total bytes transferred",
        },
        []string{"server", "direction"},
    )

    sshActiveConnections = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "ssh_active_connections",
            Help: "Number of active SSH connections",
        },
        []string{"server"},
    )
)
```

### 4.2 实时性能面板

```go
// 性能统计 API
type PerformanceStats struct {
    Server           string        `json:"server"`
    ActiveConns      int           `json:"active_connections"`
    TotalConns       int64         `json:"total_connections"`
    AvgLatency       time.Duration `json:"avg_latency"`
    P99Latency       time.Duration `json:"p99_latency"`
    BytesSent        int64         `json:"bytes_sent"`
    BytesReceived    int64         `json:"bytes_received"`
    ErrorRate        float64       `json:"error_rate"`
    CompressionRatio float64       `json:"compression_ratio"`
}

// HTTP API endpoint
// GET /api/ssh/stats
func (s *Ssh) GetStats() *PerformanceStats {
    return s.statsCollector.Snapshot()
}
```

---

## 五、实现路线图

### Phase 1: 基础性能优化（2-3周）
1. 实现零拷贝 I/O 引擎
2. 优化缓冲区管理
3. 添加基础性能指标

### Phase 2: 连接多路复用（3-4周）
1. 实现 SSH multiplexing
2. 连接池管理
3. 流量调度优化

### Phase 3: 多路径冗余（3-4周）
1. 多路径管理器
2. 智能选择算法
3. 断线预测与切换

### Phase 4: UDP 支持（2-3周）
1. UDP over TCP 封装
2. 协议实现与测试
3. 性能调优

### Phase 5: 监控与可视化（2周）
1. Metrics 集成
2. 性能面板开发
3. 文档完善

**总计**: 12-16周

---

## 六、风险评估

### 高风险项
- **零拷贝 I/O**: 平台兼容性问题（Windows/macOS 支持有限）
- **连接多路复用**: 可能与某些 SSH 服务器不兼容
- **UDP 封装**: 性能开销和协议复杂度

### 缓解策略
- 实现 graceful fallback（检测失败自动降级）
- 充分的单元测试和集成测试
- 分阶段灰度发布
- 提供配置开关（允许用户禁用特定功能）

### 兼容性保证
- 保持现有 API 不变
- 新功能通过配置选项启用
- 完整的向后兼容性测试

---

## 七、配置示例

```yaml
proxies:
  - name: "SSH_EXTREME"
    type: ssh
    server: my-server
    use-ssh-config-alias: true

    # 性能优化选项
    performance:
      zero-copy: true              # 启用零拷贝（默认 true）
      multiplexing: true           # 启用多路复用（默认 true）
      max-streams: 1000            # 单会话最大流数
      compression: auto            # 自动选择压缩算法

    # 可靠性选项
    reliability:
      multi-path: true             # 启用多路径（默认 false）
      path-count: 2                # 并行路径数量
      failover-threshold: 0.5      # 切换阈值
      circuit-breaker: true        # 启用断路器

    # UDP 支持
    udp:
      enabled: true                # 启用 UDP 支持
      method: tcp-encap            # 封装方式: tcp-encap | socks5-udp | quic

    # 监控
    monitoring:
      metrics: true                # 暴露 Prometheus metrics
      stats-api: true              # 启用统计 API
```

---

## 八、成功指标

### 性能指标
- [ ] TTFB < 50ms（当前 100-200ms）
- [ ] 吞吐量提升 30-50%
- [ ] CPU 占用降低 20-30%
- [ ] 支持 10,000+ 并发连接

### 稳定性指标
- [ ] 连接成功率 > 99.99%
- [ ] 断线恢复时间 < 1s
- [ ] 零数据丢失
- [ ] 无内存泄漏

### 功能指标
- [ ] UDP 支持覆盖率 > 95%
- [ ] 多路复用效率 > 80%
- [ ] 压缩率提升 40-60%（文本数据）

### 用户体验指标
- [ ] 零配置达到最优性能
- [ ] 实时监控面板可用
- [ ] 文档完整度 > 90%

---

**设计审核**: 待审核
**实现负责人**: 待分配
**预计完成时间**: 2026-Q2
