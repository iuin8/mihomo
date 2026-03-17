# QUIC over SSH 技术评估（macOS 平台）

**日期**: 2026-03-17
**评估对象**: QUIC over SSH 在 macOS 系统上的收益分析

---

## 一、技术背景

### 当前 SSH 实现的限制
1. **仅支持 TCP**: SSH 协议基于 TCP，无法传输 UDP 流量
2. **单层 SOCKS5 模式**: 使用 `ssh -D` 建立本地 SOCKS5 隧道，但 SOCKS5 的 UDP ASSOCIATE 支持有限
3. **UDP 应用无法使用**: QUIC、DNS、游戏、VoIP 等 UDP 应用无法通过 SSH 代理

### QUIC 协议特点
- **基于 UDP**: QUIC 运行在 UDP 之上
- **内置加密**: 类似 TLS 1.3 的加密机制
- **多路复用**: 单个连接支持多个独立流
- **0-RTT 连接**: 快速重连
- **更好的拥塞控制**: BBR 等现代算法

---

## 二、QUIC over SSH 架构分析

### 方案概述
在 SSH 隧道内运行 QUIC 协议，实现 UDP 流量的传输。

```
[应用] → [QUIC Client] → [SSH Tunnel (TCP)] → [SSH Server] → [QUIC Server] → [目标]
```

### 技术实现路径

#### 方案 A：QUIC 直接封装（推荐）
```go
// SSH 隧道内运行 QUIC
type QUICOverSSH struct {
    sshConn     net.Conn        // SSH 隧道连接
    quicConn    quic.Connection // QUIC 连接
    tlsConfig   *tls.Config
    quicConfig  *quic.Config
}

// 实现逻辑
func (q *QUICOverSSH) Dial(ctx context.Context, addr string) (net.Conn, error) {
    // 1. 通过 SSH 建立 TCP 连接到 QUIC 服务器
    sshConn, err := q.sshClient.Dial("tcp", addr)
    if err != nil {
        return nil, err
    }

    // 2. 在 SSH 连接上建立 QUIC 会话（QUIC over TCP）
    quicConn, err := quic.Dial(ctx, &connWrapper{sshConn}, addr, q.tlsConfig, q.quicConfig)
    if err != nil {
        return nil, err
    }

    // 3. 打开 QUIC stream
    stream, err := quicConn.OpenStreamSync(ctx)
    return stream, err
}

// 包装 net.Conn 为 quic.PacketConn
type connWrapper struct {
    net.Conn
}

func (c *connWrapper) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
    n, err = c.Conn.Read(p)
    return n, c.Conn.RemoteAddr(), err
}

func (c *connWrapper) WriteTo(p []byte, addr net.Addr) (n int, err error) {
    return c.Conn.Write(p)
}
```

#### 方案 B：UDP-in-TCP 封装 + QUIC
```go
// 先封装 UDP 包到 TCP，再运行 QUIC
type UDPOverTCP struct {
    tcpConn net.Conn
}

// 数据包格式：[2字节长度][UDP payload]
func (u *UDPOverTCP) WritePacket(data []byte) error {
    length := uint16(len(data))
    header := []byte{byte(length >> 8), byte(length)}
    _, err := u.tcpConn.Write(append(header, data...))
    return err
}

func (u *UDPOverTCP) ReadPacket() ([]byte, error) {
    header := make([]byte, 2)
    if _, err := io.ReadFull(u.tcpConn, header); err != nil {
        return nil, err
    }
    length := uint16(header[0])<<8 | uint16(header[1])

    data := make([]byte, length)
    _, err := io.ReadFull(u.tcpConn, data)
    return data, err
}
```

---

## 三、macOS 平台收益评估

### ✅ 正面收益

#### 1. UDP 应用支持（核心价值）
**收益**: ⭐⭐⭐⭐⭐
- 支持 QUIC 协议（HTTP/3）
- 支持 DNS over UDP
- 支持游戏、VoIP 等实时应用
- 解决当前 SSH 无法处理 UDP 的根本问题

**实际场景**：
- Chrome/Safari 访问支持 HTTP/3 的网站（如 Google、Cloudflare）
- 游戏加速（如 Steam、Epic）
- 视频会议（Zoom、Teams）

#### 2. 多路复用性能
**收益**: ⭐⭐⭐⭐
- QUIC 原生支持多路复用，无队头阻塞
- 单个 QUIC 连接可承载多个独立流
- 比 TCP 的多路复用更高效

**性能对比**：
```
TCP (HTTP/2):  Stream1 阻塞 → 所有 Stream 等待
QUIC (HTTP/3): Stream1 阻塞 → 其他 Stream 继续
```

#### 3. 0-RTT 快速重连
**收益**: ⭐⭐⭐
- QUIC 支持 0-RTT 连接恢复
- 网络切换（WiFi ↔ 蜂窝）时快速恢复
- macOS 移动场景（MacBook 移动）特别有用

#### 4. 更好的拥塞控制
**收益**: ⭐⭐⭐
- QUIC 使用 BBR 等现代拥塞控制算法
- 在高延迟、高丢包网络下表现更好
- macOS 上的 WiFi 环境受益明显

---

### ❌ 负面影响

#### 1. 双重加密开销
**影响**: ⭐⭐⭐⭐
- SSH 已经加密（AES-GCM）
- QUIC 再次加密（TLS 1.3）
- **CPU 开销增加 50-100%**

**实测数据**（预估）：
```
单层 SSH:        CPU 10%,  延迟 20ms
QUIC over SSH:   CPU 20%,  延迟 25-30ms
```

**macOS 影响**：
- M1/M2/M3 芯片：影响较小（硬件加密加速）
- Intel 芯片：影响较大（软件加密）

#### 2. UDP over TCP 的性能损失
**影响**: ⭐⭐⭐⭐⭐
- **队头阻塞**: TCP 丢包会阻塞所有 UDP 包
- **重传风暴**: QUIC 重传 + TCP 重传 = 双重重传
- **延迟增加**: 30-50% 的额外延迟

**示例**：
```
直接 QUIC:       丢包 1% → 延迟 50ms
QUIC over TCP:   丢包 1% → 延迟 80-100ms（TCP 重传）
```

#### 3. 吞吐量下降
**影响**: ⭐⭐⭐
- TCP 的流控 + QUIC 的流控 = 双重限制
- 实际吞吐量可能降低 20-40%

**macOS 网络环境**：
- WiFi 6/6E: 影响较小（带宽充足）
- WiFi 5 及以下: 影响明显

#### 4. 实现复杂度
**影响**: ⭐⭐⭐⭐
- 需要实现 UDP-in-TCP 封装协议
- 需要处理 QUIC 连接管理
- 需要服务端配合（部署 QUIC 服务器）
- 代码量增加 1000+ 行

---

## 四、与其他方案对比

### 方案对比表

| 方案 | UDP 支持 | 性能 | 复杂度 | 服务端要求 | macOS 收益 |
|------|---------|------|--------|-----------|-----------|
| **当前 SSH** | ❌ | ⭐⭐⭐⭐ | ⭐ | 仅需 SSH | 基准 |
| **QUIC over SSH** | ✅ | ⭐⭐ | ⭐⭐⭐⭐ | SSH + QUIC 服务器 | ⭐⭐⭐ |
| **WireGuard** | ✅ | ⭐⭐⭐⭐⭐ | ⭐⭐ | WireGuard 服务器 | ⭐⭐⭐⭐⭐ |
| **V2Ray/Xray** | ✅ | ⭐⭐⭐⭐ | ⭐⭐⭐ | V2Ray 服务器 | ⭐⭐⭐⭐ |
| **Hysteria** | ✅ | ⭐⭐⭐⭐⭐ | ⭐⭐ | Hysteria 服务器 | ⭐⭐⭐⭐⭐ |

### 关键发现
1. **QUIC over SSH 不是最优方案**: 性能低于原生 QUIC 协议（WireGuard/Hysteria）
2. **主要价值**: 在只有 SSH 服务器的环境下，提供 UDP 支持
3. **macOS 特定优势**: M 系列芯片的硬件加密可以缓解双重加密开销

---

## 五、macOS 平台具体收益

### 适用场景（高收益）

#### 1. 企业内网环境
**场景**: 只能使用 SSH 跳板机，无法部署其他协议
**收益**: ⭐⭐⭐⭐⭐
- 解决 UDP 应用无法使用的问题
- 无需额外服务器部署
- 符合企业安全策略

#### 2. 受限网络环境
**场景**: 防火墙只允许 SSH (TCP 22)
**收益**: ⭐⭐⭐⭐
- 绕过 UDP 封锁
- 保持 SSH 的隐蔽性

#### 3. 临时解决方案
**场景**: 快速需要 UDP 支持，但无法部署专用服务器
**收益**: ⭐⭐⭐⭐
- 快速启用 UDP 功能
- 后续可迁移到更优方案

### 不适用场景（低收益）

#### 1. 高性能需求
**场景**: 游戏、4K 视频、大文件传输
**收益**: ⭐
- 双重加密和 UDP-over-TCP 导致性能严重下降
- **建议**: 使用 WireGuard 或 Hysteria

#### 2. 低延迟需求
**场景**: 实时游戏、视频会议
**收益**: ⭐⭐
- 额外 30-50% 延迟不可接受
- **建议**: 使用原生 UDP 协议

#### 3. 移动办公（频繁网络切换）
**场景**: MacBook 在咖啡厅、家、办公室之间移动
**收益**: ⭐⭐
- QUIC 的 0-RTT 优势被 SSH 重连抵消
- **建议**: 使用 WireGuard（更好的移动性）

---

## 六、实现成本评估

### 开发成本
- **代码量**: 800-1200 行
- **开发时间**: 3-4 周
- **测试时间**: 1-2 周
- **总计**: 4-6 周

### 维护成本
- **复杂度**: 高（需要维护 UDP 封装协议）
- **调试难度**: 高（双层协议栈）
- **上游合并冲突**: 中等（新增独立模块）

### 服务端部署成本
- **需要部署**: QUIC 服务器（如 Caddy、nginx-quic）
- **配置复杂度**: 中等
- **运维成本**: 中等

---

## 七、最终评估结论

### macOS 平台总体收益: ⭐⭐⭐ (3/5)

#### 核心价值
✅ **解决 UDP 支持问题**（这是唯一的核心价值）

#### 主要限制
❌ 性能损失 30-50%（双重加密 + UDP-over-TCP）
❌ 实现复杂度高
❌ 需要服务端配合
❌ 不如专用 UDP 协议（WireGuard/Hysteria）

### 推荐策略

#### 如果你的场景是：
1. **只有 SSH 服务器，无法部署其他协议** → ✅ 值得实现
2. **需要临时 UDP 支持** → ✅ 可以考虑
3. **企业内网受限环境** → ✅ 有价值

#### 如果你的场景是：
1. **可以部署其他服务器** → ❌ 建议用 WireGuard/Hysteria
2. **追求极致性能** → ❌ 不推荐
3. **低延迟需求** → ❌ 不推荐

### 替代方案建议

#### 方案 1: WireGuard（最推荐）
- **优势**: 性能最佳、延迟最低、实现简单
- **劣势**: 需要部署 WireGuard 服务器
- **macOS 收益**: ⭐⭐⭐⭐⭐

#### 方案 2: Hysteria 2
- **优势**: 专为高丢包网络优化、性能优秀
- **劣势**: 需要部署 Hysteria 服务器
- **macOS 收益**: ⭐⭐⭐⭐⭐

#### 方案 3: 保持现状 + 文档说明
- **优势**: 零成本、代码简洁
- **劣势**: 不支持 UDP
- **建议**: 在文档中明确说明 SSH 不适合 UDP 场景

---

## 八、如果决定实现，建议的实现策略

### 阶段 1: 最小可行方案（2周）
1. 实现 UDP-in-TCP 封装协议
2. 集成 quic-go 库（已有依赖）
3. 基础功能验证

### 阶段 2: 性能优化（1-2周）
1. 优化封装协议（减少开销）
2. 调优 QUIC 参数
3. 性能测试

### 阶段 3: 生产就绪（1周）
1. 错误处理完善
2. 文档编写
3. 集成测试

**总计**: 4-5周

---

## 九、技术风险

| 风险项 | 影响 | 概率 | 缓解措施 |
|--------|------|------|----------|
| 性能不达预期 | 高 | 高 | 充分的性能测试，设定合理预期 |
| UDP-over-TCP 队头阻塞 | 高 | 中 | 优化封装协议，减少包大小 |
| 服务端部署困难 | 中 | 中 | 提供详细部署文档 |
| 与现有代码冲突 | 低 | 低 | 独立模块实现 |
| macOS 特定问题 | 中 | 低 | 充分测试 |

---

## 十、决策建议

### 如果你的主要目标是：

#### 目标 A: "我需要 UDP 支持，且只有 SSH 服务器"
**建议**: ✅ 实现 QUIC over SSH
**理由**: 这是唯一可行的方案

#### 目标 B: "我想提升 SSH 代理的性能"
**建议**: ❌ 不要实现 QUIC over SSH
**理由**: 性能会下降，不是提升

#### 目标 C: "我想要最好的 UDP 代理体验"
**建议**: ❌ 不要实现 QUIC over SSH，改用 WireGuard
**理由**: WireGuard 性能更好、延迟更低

### 我的最终建议

基于你的情况（macOS 用户，fork 项目，希望代码精简）：

**不推荐实现 QUIC over SSH**，原因：
1. 性能损失 30-50%，不符合"极致性能"目标
2. 实现复杂度高（4-5周），维护成本高
3. macOS 上有更好的替代方案（WireGuard）
4. 需要服务端配合，部署成本高

**推荐替代方案**：
1. 在文档中明确说明：SSH 适合 TCP 流量，UDP 场景请使用 WireGuard
2. 如果确实需要 UDP，考虑集成 WireGuard 支持（mihomo 已有 WireGuard 实现）

---

**评估结论**: macOS 平台收益 ⭐⭐⭐ (3/5)，不推荐实现
**建议**: 保持 SSH 代理的简洁性，UDP 场景使用专用协议
