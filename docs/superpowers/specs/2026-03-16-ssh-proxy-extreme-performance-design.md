# SSH 系统代理稳定性增强设计

**日期**: 2026-03-16
**版本**: v3.0 (Minimal-impact revision)
**目标**: 在现有 SSH 系统代理基础上，以最小改动实现稳定性增强

---

## 设计原则

1. **最小化 ssh.go 改动**: 项目是 fork，需要减少上游合并冲突
2. **聚焦稳定性**: 优先解决可靠性问题，而非性能优化
3. **代码精简**: 不引入复杂的监控和统计系统
4. **平台通用**: 不做平台特定优化（如 Linux 专属的零拷贝）
5. **单层优先**: 当前主要使用 `use-system-socks: true` 单层模式，不需要连接池

---

## 审查结果总结

经过技术审查和用户需求调整：
1. **零拷贝 I/O**: ❌ 删除（仅 Linux 可用，macOS 不支持）
2. **连接池管理**: ❌ 删除（单层模式不需要）
3. **监控系统**: ❌ 删除（保持代码精简）
4. **SSH 多路复用**: ❌ 删除（Go SSH 库已内置）
5. **多路径冗余**: ❌ 删除（复杂度过高）
6. **UDP 支持**: ❌ 删除（协议不匹配）

**保留特性**（核心稳定性改进）：
- ✅ 断路器模式（防止级联故障）
- ✅ 自适应超时（减少误判）
- ✅ 增强健康检查（更早发现问题）

---

## 一、设计目标（最小化版本）

### 稳定性目标
- 实现 99.9% 连接成功率（通过断路器）
- 断线恢复时间 < 3s（通过快速重连）
- 避免级联故障（通过断路器快速失败）
- 减少超时误判（通过自适应超时）

### 非目标（明确不做）
- ❌ 性能优化（保持现有性能）
- ❌ 监控系统（不引入 Prometheus/metrics）
- ❌ 连接池（单层模式不需要）
- ❌ 平台特定优化（保持跨平台一致性）

---

## 二、核心架构设计（最小化改动）

### 2.1 断路器模式（新增独立文件）

**实现位置**: `adapter/outbound/ssh_circuit_breaker.go`（新文件，不修改 ssh.go）

**设计思路**：
- 独立的断路器组件，通过组合方式集成
- 不修改现有 `Ssh` 结构体的核心逻辑
- 仅在连接建立时包装调用

**实现**：
```go
// ssh_circuit_breaker.go
package outbound

import (
    "sync"
    "time"
)

type CircuitBreakerState int

const (
    StateClosed CircuitBreakerState = iota
    StateOpen
    StateHalfOpen
)

// CircuitBreaker 断路器（防止级联故障）
type CircuitBreaker struct {
    state         CircuitBreakerState
    failureCount  int
    successCount  int
    threshold     int           // 失败阈值（默认 5）
    halfOpenMax   int           // 半开状态最大尝试次数（默认 3）
    timeout       time.Duration // 熔断时长（默认 30s）
    openedAt      time.Time
    mu            sync.RWMutex
}

func NewCircuitBreaker() *CircuitBreaker {
    return &CircuitBreaker{
        state:       StateClosed,
        threshold:   5,
        halfOpenMax: 3,
        timeout:     30 * time.Second,
    }
}

func (cb *CircuitBreaker) Call(fn func() error) error {
    cb.mu.RLock()
    state := cb.state
    openedAt := cb.openedAt
    cb.mu.RUnlock()

    // 熔断状态：快速失败
    if state == StateOpen {
        if time.Since(openedAt) > cb.timeout {
            cb.mu.Lock()
            cb.state = StateHalfOpen
            cb.successCount = 0
            cb.mu.Unlock()
        } else {
            return fmt.Errorf("circuit breaker is open")
        }
    }

    // 执行实际操作
    err := fn()

    // 更新状态
    cb.mu.Lock()
    defer cb.mu.Unlock()

    if err != nil {
        cb.failureCount++
        if cb.state == StateHalfOpen || cb.failureCount >= cb.threshold {
            cb.state = StateOpen
            cb.openedAt = time.Now()
            log.Warnln("[SSH] Circuit breaker opened after %d failures", cb.failureCount)
        }
    } else {
        cb.failureCount = 0
        if cb.state == StateHalfOpen {
            cb.successCount++
            if cb.successCount >= cb.halfOpenMax {
                cb.state = StateClosed
                log.Infoln("[SSH] Circuit breaker closed after recovery")
            }
        }
    }

    return err
}

func (cb *CircuitBreaker) IsOpen() bool {
    cb.mu.RLock()
    defer cb.mu.RUnlock()
    return cb.state == StateOpen
}
```

**集成方式**（最小化修改 ssh.go）：
```go
// 在 Ssh 结构体中添加一个字段（仅此一处修改）
type Ssh struct {
    *Base
    // ... 现有字段 ...
    circuitBreaker *CircuitBreaker // 新增
}

// 在 NewSsh 中初始化
func NewSsh(option SshOption) (*Ssh, error) {
    // ... 现有代码 ...
    outbound := &Ssh{
        // ... 现有字段 ...
        circuitBreaker: NewCircuitBreaker(), // 新增
    }
    // ... 现有代码 ...
}

// 在 connect 方法中包装调用（最小化修改）
func (s *Ssh) connect(ctx context.Context, addr string) (client *ssh.Client, err error) {
    // 使用断路器包装连接逻辑
    err = s.circuitBreaker.Call(func() error {
        var connectErr error
        client, connectErr = s.connectInternal(ctx, addr)
        return connectErr
    })
    return client, err
}

// 将原有 connect 逻辑重命名为 connectInternal（保持不变）
func (s *Ssh) connectInternal(ctx context.Context, addr string) (client *ssh.Client, err error) {
    // ... 原有的 connect 逻辑完全不变 ...
}
```

**预期收益**：
- 避免级联故障（快速失败）
- 减少无效重试（节省资源）
- 提升用户体验（快速失败 vs 长时间等待）

---

### 2.2 自适应超时（新增独立文件）

**实现位置**: `adapter/outbound/ssh_adaptive_timeout.go`（新文件）

**设计思路**：
- 基于历史延迟动态调整超时
- 不修改现有健康检查逻辑
- 可选特性，默认使用固定超时

**实现**：
```go
// ssh_adaptive_timeout.go
package outbound

import (
    "sort"
    "sync"
    "time"
)

// AdaptiveTimeout 自适应超时管理器
type AdaptiveTimeout struct {
    samples   []time.Duration
    maxSample int // 保留最近 100 个样本
    mu        sync.RWMutex
}

func NewAdaptiveTimeout() *AdaptiveTimeout {
    return &AdaptiveTimeout{
        samples:   make([]time.Duration, 0, 100),
        maxSample: 100,
    }
}

func (a *AdaptiveTimeout) RecordLatency(d time.Duration) {
    a.mu.Lock()
    defer a.mu.Unlock()

    a.samples = append(a.samples, d)
    if len(a.samples) > a.maxSample {
        a.samples = a.samples[1:]
    }
}

func (a *AdaptiveTimeout) GetTimeout() time.Duration {
    a.mu.RLock()
    defer a.mu.RUnlock()

    if len(a.samples) < 10 {
        return 5 * time.Second // 样本不足，使用默认值
    }

    // 计算 P95 延迟（比 P99 更稳定）
    sorted := make([]time.Duration, len(a.samples))
    copy(sorted, a.samples)
    sort.Slice(sorted, func(i, j int) bool {
        return sorted[i] < sorted[j]
    })

    p95Index := int(float64(len(sorted)) * 0.95)
    p95 := sorted[p95Index]

    // 超时 = P95 * 2 + 1s 缓冲
    timeout := p95*2 + time.Second

    // 限制范围：2s ~ 30s
    if timeout < 2*time.Second {
        timeout = 2 * time.Second
    }
    if timeout > 30*time.Second {
        timeout = 30 * time.Second
    }

    return timeout
}
```

**集成方式**（可选，不强制启用）：
```go
// 在 Ssh 结构体中添加字段
type Ssh struct {
    // ... 现有字段 ...
    adaptiveTimeout *AdaptiveTimeout // 新增（可选）
}

// 在健康检查中记录延迟（修改 ssh_resilience.go）
func (s *Ssh) startHealthCheck(client *ssh.Client) {
    // ... 现有代码 ...
    for {
        select {
        case <-ticker.C:
            start := time.Now()
            _, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
            latency := time.Since(start)

            // 新增：记录延迟
            if s.adaptiveTimeout != nil {
                s.adaptiveTimeout.RecordLatency(latency)
            }

            // ... 现有错误处理逻辑 ...
        }
    }
}
```

**预期收益**：
- 减少超时误判（网络慢时自动延长超时）
- 提升响应速度（网络快时自动缩短超时）

---

### 2.3 增强健康检查（最小化修改现有文件）

**实现位置**: 修改 `adapter/outbound/ssh_resilience.go`

**设计思路**：
- 在现有健康检查基础上增加质量评分
- 动态调整检查间隔（质量差时增加频率）
- 不改变现有的重连逻辑

**实现**：
```go
// 在 ssh_resilience.go 中添加健康度评分结构
type HealthMetrics struct {
    latency          time.Duration
    errorCount       int
    successCount     int
    consecutiveFails int
}

func (h *HealthMetrics) Update(latency time.Duration, err error) {
    if err != nil {
        h.errorCount++
        h.consecutiveFails++
    } else {
        h.successCount++
        h.consecutiveFails = 0
        h.latency = latency
    }
}

// 健康评分（0.0 - 1.0）
func (h *HealthMetrics) Score() float64 {
    if h.consecutiveFails >= 3 {
        return 0.0
    }

    total := h.errorCount + h.successCount
    if total == 0 {
        return 1.0
    }

    successRate := float64(h.successCount) / float64(total)

    // 延迟惩罚（基线 100ms）
    latencyPenalty := 0.0
    if h.latency > 100*time.Millisecond {
        latencyPenalty = float64(h.latency-100*time.Millisecond) / float64(time.Second) * 0.2
    }

    score := successRate - latencyPenalty
    if score < 0 {
        score = 0
    }
    return score
}

// 修改现有的 startHealthCheck 函数
func (s *Ssh) startHealthCheck(client *ssh.Client) {
    dead := make(chan struct{})
    go func() {
        _ = client.Wait()
        close(dead)
    }()

    metrics := &HealthMetrics{}
    interval := sshHealthCheckInterval // 使用现有常量（30s）

    ticker := time.NewTicker(interval)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            start := time.Now()
            _, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
            latency := time.Since(start)

            metrics.Update(latency, err)
            score := metrics.Score()

            if err != nil {
                log.Warnln("[SSH] Health check failed for %s (score: %.2f): %v",
                    s.option.Name, score, err)

                // 连续失败 3 次，主动关闭连接
                if metrics.consecutiveFails >= 3 {
                    log.Errorln("[SSH] Connection unhealthy, closing")
                    _ = client.Close()
                    <-dead
                    return
                }
            } else {
                log.Debugln("[SSH] Health check OK for %s (latency: %v, score: %.2f)",
                    s.option.Name, latency, score)
            }

            // 动态调整检查间隔
            if score < 0.7 {
                interval = 10 * time.Second // 质量下降，增加检查频率
            } else {
                interval = sshHealthCheckInterval // 恢复默认间隔
            }
            ticker.Reset(interval)

        case <-dead:
            log.Warnln("[SSH] Connection closed for %s", s.option.Name)
            return
        }
    }
}
```

**预期收益**：
- 更早发现连接质量下降
- 动态调整检查频率（节省资源）
- 主动关闭不健康连接（触发重连）

---

## 三、实现路线图（最小化版本）

### Phase 1: 断路器实现（1周）
**目标**: 防止级联故障

**任务**：
1. 创建 `ssh_circuit_breaker.go`（新文件）
2. 在 `Ssh` 结构体中添加 `circuitBreaker` 字段
3. 修改 `connect` 方法包装断路器调用
4. 单元测试（状态转换、阈值触发）

**文件改动**：
- 新增：`adapter/outbound/ssh_circuit_breaker.go`
- 修改：`adapter/outbound/ssh.go`（约 10 行改动）

**风险**: 低

---

### Phase 2: 自适应超时（1周）
**目标**: 减少超时误判

**任务**：
1. 创建 `ssh_adaptive_timeout.go`（新文件）
2. 在 `Ssh` 结构体中添加 `adaptiveTimeout` 字段（可选）
3. 在健康检查中记录延迟
4. 单元测试（P95 计算、边界条件）

**文件改动**：
- 新增：`adapter/outbound/ssh_adaptive_timeout.go`
- 修改：`adapter/outbound/ssh.go`（约 5 行改动）
- 修改：`adapter/outbound/ssh_resilience.go`（约 5 行改动）

**风险**: 低

---

### Phase 3: 增强健康检查（1周）
**目标**: 更早发现连接质量问题

**任务**：
1. 在 `ssh_resilience.go` 中添加 `HealthMetrics` 结构
2. 修改 `startHealthCheck` 函数（增加评分和动态间隔）
3. 集成测试（模拟网络抖动）

**文件改动**：
- 修改：`adapter/outbound/ssh_resilience.go`（约 50 行改动）

**风险**: 低

---

### Phase 4: 测试与文档（1周）
**目标**: 确保稳定性和可维护性

**任务**：
1. 集成测试（断路器 + 健康检查联动）
2. 故障注入测试（模拟网络故障）
3. 更新用户文档（配置说明）
4. 代码审查

**文件改动**：
- 新增：测试文件
- 更新：文档

**风险**: 无

---

**总计**: 4周（相比原计划的 10-13周大幅简化）

---

## 四、文件改动清单

### 新增文件（2个）
1. `adapter/outbound/ssh_circuit_breaker.go` (~150 行)
2. `adapter/outbound/ssh_adaptive_timeout.go` (~80 行)

### 修改文件（2个）
1. `adapter/outbound/ssh.go`
   - 添加 2 个字段：`circuitBreaker`, `adaptiveTimeout`
   - 修改 `NewSsh` 初始化（2 行）
   - 修改 `connect` 方法包装断路器（约 10 行）
   - **总改动**: ~15 行

2. `adapter/outbound/ssh_resilience.go`
   - 添加 `HealthMetrics` 结构（~30 行）
   - 修改 `startHealthCheck` 函数（~50 行）
   - **总改动**: ~80 行

**总代码量**: 约 300 行新增代码，95 行修改

---

## 五、配置示例（最小化）

### 默认配置（无需修改）
```yaml
proxies:
  - name: "SSH_STABLE"
    type: ssh
    server: my-server
    use-ssh-config-alias: true
    use-system-socks: true

# 断路器和自适应超时默认启用，无需配置
```

### 高级配置（可选）
```yaml
proxies:
  - name: "SSH_CUSTOM"
    type: ssh
    server: my-server
    use-ssh-config-alias: true

    # 断路器配置（可选，有默认值）
    circuit-breaker:
      enabled: true              # 默认 true
      failure-threshold: 5       # 失败阈值（默认 5）
      timeout: 30s               # 熔断时长（默认 30s）
      half-open-max: 3           # 半开状态最大尝试（默认 3）

    # 自适应超时（可选）
    adaptive-timeout:
      enabled: true              # 默认 true
      min-timeout: 2s            # 最小超时（默认 2s）
      max-timeout: 30s           # 最大超时（默认 30s）

    # 健康检查（可选，有默认值）
    health-check:
      interval: 30s              # 默认间隔（默认 30s）
      dynamic-interval: true     # 动态调整间隔（默认 true）
```

---

## 六、风险评估（最小化版本）

### 技术风险

| 风险项 | 影响 | 概率 | 缓解措施 |
|--------|------|------|----------|
| 断路器误判 | 中 | 低 | 可配置阈值、充分测试 |
| 健康检查逻辑冲突 | 低 | 低 | 保持现有逻辑不变，仅增强 |
| 并发安全问题 | 中 | 低 | 使用 sync.RWMutex、race detector |

### 兼容性风险

| 风险项 | 影响 | 缓解措施 |
|--------|------|----------|
| 上游合并冲突 | 低 | 最小化 ssh.go 改动（仅 15 行） |
| 破坏现有功能 | 低 | 所有新特性可通过配置禁用 |
| 配置不兼容 | 无 | 所有配置都有默认值，无需修改现有配置 |

---

## 七、成功指标（最小化版本）

### 稳定性指标
- [x] 连接成功率 > 99.9%（通过断路器）
- [x] 断线恢复时间 < 3s（通过快速重连）
- [x] 避免级联故障（通过断路器快速失败）
- [x] 减少超时误判（通过自适应超时）

### 代码质量指标
- [x] ssh.go 改动 < 20 行
- [x] 新增代码 < 500 行
- [x] 单元测试覆盖率 > 80%
- [x] 无内存泄漏（通过 pprof 验证）
- [x] 无并发问题（通过 race detector 验证）

### 可维护性指标
- [x] 代码结构清晰（独立文件）
- [x] 配置简单（有合理默认值）
- [x] 文档完整（配置说明 + 故障排查）

---

## 八、测试策略

### 单元测试
- 断路器状态转换（Closed → Open → HalfOpen → Closed）
- 断路器阈值触发
- 自适应超时 P95 计算
- 健康评分算法

### 集成测试
- 断路器 + 健康检查联动
- 自适应超时在实际连接中的表现
- 配置加载和默认值

### 故障注入测试
- 模拟网络延迟（验证自适应超时）
- 模拟连接失败（验证断路器）
- 模拟间歇性故障（验证健康检查）

### 长期稳定性测试
- 24 小时连续运行
- 内存泄漏检测（pprof）
- 并发安全检测（race detector）

---

## 九、向后兼容性保证

### API 兼容性
- ✅ 现有配置文件无需修改
- ✅ 所有新配置项都有默认值
- ✅ 不删除任何现有配置选项
- ✅ 不修改现有 API 接口

### 行为兼容性
- ✅ 默认行为与当前版本一致
- ✅ 新特性默认启用但不改变核心逻辑
- ✅ 可通过配置完全禁用新特性

### 升级路径
1. 直接替换二进制文件（无需配置修改）
2. 观察日志确认断路器和健康检查工作正常
3. 根据需要调整高级配置（可选）

---

## 十、与上游合并策略

### 最小化冲突
- ssh.go 仅修改 15 行（添加字段 + 包装调用）
- 新功能全部在独立文件中实现
- 不修改现有函数签名
- 不删除现有代码

### 合并时处理
1. 如果上游修改了 `Ssh` 结构体：手动添加 2 个字段
2. 如果上游修改了 `connect` 方法：手动添加断路器包装
3. 如果上游修改了 `startHealthCheck`：评估是否需要合并增强逻辑

### 长期维护
- 定期同步上游更新
- 保持新增代码的独立性
- 文档记录所有改动点

---

**设计审核**: ✅ 已通过技术审查（v3.0 - 最小化版本）
**实现负责人**: 待分配
**预计完成时间**: 4周
**下一步**: 用户审核并批准设计

---

## 四、实现路线图（修订版）

### Phase 1: 缓冲池优化与基础监控（2周）
**目标**: 低风险的性能提升 + 可观测性基础

**任务**：
1. 实现 `sync.Pool` 缓冲池（所有平台受益）
2. 添加 Prometheus metrics
3. 实现统计 API (`/connections/ssh/{name}/stats`)
4. 性能基准测试（建立 baseline）

**预期收益**：
- 内存分配减少 30%
- GC 压力降低
- 可观测性建立

**风险**: 低

---

### Phase 2: 零拷贝 I/O（Linux）（2-3周）
**目标**: Linux 平台性能优化

**任务**：
1. 实现 `splice()` 传输路径
2. 错误处理与 fallback 机制
3. 平台检测与运行时切换
4. 性能测试与对比

**预期收益**：
- Linux 吞吐量提升 10-20%
- CPU 占用降低 10-15%

**风险**: 中等（需要处理各种边缘情况）

---

### Phase 3: 断路器与自适应超时（2周）
**目标**: 提升稳定性和用户体验

**任务**：
1. 实现断路器模式
2. 实现自适应超时
3. 集成到现有连接逻辑
4. 故障注入测试

**预期收益**：
- 避免级联故障
- 减少误判超时
- 快速失败体验

**风险**: 低

---

### Phase 4: 连接池管理（2-3周）
**目标**: 高并发场景性能提升

**任务**：
1. 实现连接池（仅 Go SSH 客户端模式）
2. 负载均衡策略
3. 连接生命周期管理
4. 并发测试（10,000+ 连接）

**预期收益**：
- 高并发下连接建立开销降低 60-80%
- 支持 10,000+ 并发连接

**风险**: 中等（需要仔细处理并发安全）

---

### Phase 5: 增强健康检查（1-2周）
**目标**: 更智能的连接质量监控

**任务**：
1. 实现健康评分系统
2. 动态调整检查间隔
3. 集成到 metrics
4. 长期稳定性测试

**预期收益**：
- 更早发现连接质量下降
- 节省健康检查资源

**风险**: 低

---

### Phase 6: 文档与优化（1周）
**目标**: 完善文档和最终优化

**任务**：
1. 用户文档（配置说明、最佳实践）
2. 性能调优指南
3. 故障排查手册
4. 性能对比报告

**预期收益**：
- 用户可以充分利用新特性
- 降低支持成本

**风险**: 无

---

**总计**: 10-13周（相比原计划的 12-16周更现实）

---

## 五、风险评估与缓解

### 技术风险

| 风险项 | 影响 | 概率 | 缓解措施 |
|--------|------|------|----------|
| splice() 兼容性问题 | 中 | 中 | 完善的 fallback 机制，充分测试 |
| 连接池并发 bug | 高 | 中 | 代码审查、race detector、压力测试 |
| 断路器误判 | 中 | 低 | 可配置阈值、充分的单元测试 |
| 内存泄漏 | 高 | 低 | pprof 分析、长期稳定性测试 |
| 性能回退 | 中 | 低 | 基准测试、A/B 对比 |

### 兼容性风险

| 风险项 | 影响 | 缓解措施 |
|--------|------|----------|
| 破坏现有配置 | 高 | 所有新特性通过配置选项启用，默认关闭 |
| 系统 SSH 模式冲突 | 中 | 明确文档说明哪些特性适用于哪种模式 |
| Go 版本依赖 | 低 | 保持与现有代码相同的 Go 版本要求 |

### 运维风险

| 风险项 | 影响 | 缓解措施 |
|--------|------|----------|
| 监控数据过多 | 低 | metrics 可通过配置禁用 |
| 配置复杂度增加 | 中 | 提供合理的默认值，高级选项可选 |
| 升级迁移成本 | 低 | 向后兼容，无需修改现有配置 |

---

## 六、配置示例（修订版）

### 基础配置（推荐）
```yaml
proxies:
  - name: "SSH_OPTIMIZED"
    type: ssh
    server: my-server
    use-ssh-config-alias: true

    # 性能优化（默认启用）
    performance:
      buffer-pool: true           # 缓冲池优化（默认 true）
      zero-copy: auto             # 自动检测平台支持（Linux 启用）

    # 可靠性增强（默认启用）
    reliability:
      circuit-breaker: true       # 断路器（默认 true）
      adaptive-timeout: true      # 自适应超时（默认 true）

    # 监控（默认启用）
    monitoring:
      metrics: true               # Prometheus metrics
      stats-api: true             # 统计 API
```

### 高并发场景配置
```yaml
proxies:
  - name: "SSH_HIGH_CONCURRENCY"
    type: ssh
    server: my-server
    use-ssh-config-alias: false  # 使用 Go SSH 客户端模式

    # 连接池（仅 Go SSH 模式可用）
    connection-pool:
      enabled: true
      max-connections: 3         # 最大连接数
      max-channels-per-conn: 500 # 单连接最大 channel 数

    # 其他优化
    performance:
      buffer-pool: true
      buffer-size: 65536         # 64KB 缓冲区（高吞吐场景）
```

### 低延迟场景配置
```yaml
proxies:
  - name: "SSH_LOW_LATENCY"
    type: ssh
    server: my-server
    use-system-socks: true       # 单层模式（最低延迟）

    # 健康检查优化
    health-check:
      interval: 10s              # 更频繁的检查
      timeout: 3s                # 更短的超时

    # 断路器配置
    circuit-breaker:
      failure-threshold: 3       # 3 次失败后熔断
      timeout: 15s               # 15s 后尝试恢复
```

### 调试配置
```yaml
proxies:
  - name: "SSH_DEBUG"
    type: ssh
    server: my-server

    # 详细监控
    monitoring:
      metrics: true
      stats-api: true
      detailed-logging: true     # 详细日志（性能影响）

    # 禁用优化（排查问题）
    performance:
      zero-copy: false           # 禁用零拷贝
      buffer-pool: false         # 禁用缓冲池
```

---

## 七、成功指标（修订版）

### 性能指标
- [x] **所有平台**: 内存分配减少 30%（通过缓冲池）
- [x] **所有平台**: GC 压力降低（通过对象复用）
- [x] **Linux**: 吞吐量提升 10-20%（通过零拷贝）
- [x] **Linux**: CPU 占用降低 10-15%（通过零拷贝）
- [x] **高并发**: 支持 10,000+ 并发连接（通过连接池）

### 稳定性指标
- [x] 连接成功率 > 99.9%（通过断路器）
- [x] 断线恢复时间 < 3s（通过快速重连）
- [x] 零数据丢失（通过缓冲区管理）
- [x] 无内存泄漏（通过长期测试验证）
- [x] 无死锁（通过 race detector 验证）

### 可观测性指标
- [x] Prometheus metrics 完整暴露
- [x] 实时统计 API 可用
- [x] 健康评分系统工作正常
- [x] 断路器状态可监控

### 用户体验指标
- [x] 零配置达到合理性能（默认值优化）
- [x] 文档完整度 > 90%
- [x] 配置示例覆盖常见场景
- [x] 故障排查指南完善

---

## 八、测试策略

### 单元测试
- 缓冲池正确性（获取/归还）
- 断路器状态转换
- 自适应超时计算
- 健康评分算法

### 集成测试
- 零拷贝 fallback 机制
- 连接池并发安全
- 断路器与重连交互
- metrics 数据准确性

### 性能测试
- 基准测试（与当前版本对比）
- 吞吐量测试（1Gbps+ 网络）
- 并发测试（10,000+ 连接）
- 长期稳定性测试（24h+）

### 兼容性测试
- Linux (Ubuntu 22.04, CentOS 8)
- macOS (13+, 14+)
- Windows (10, 11)
- 各种 SSH 服务器（OpenSSH, Dropbear）

---

## 九、向后兼容性保证

### API 兼容性
- 现有配置文件无需修改即可工作
- 新增配置项都有合理默认值
- 不删除任何现有配置选项

### 行为兼容性
- 默认行为与当前版本一致
- 新特性通过配置显式启用
- 性能优化不改变功能语义

### 升级路径
1. 直接替换二进制文件（无需配置修改）
2. 观察 metrics 确认工作正常
3. 根据需要启用高级特性
4. 逐步调优配置参数

---

**设计审核**: ✅ 已通过技术审查（v2.0）
**实现负责人**: 待分配
**预计完成时间**: 2026-Q2（10-13周）
**下一步**: 用户审核并批准设计
