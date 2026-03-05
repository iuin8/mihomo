# 上游代码优化记录 (Upstream Optimizations)

本文档专门用于记录分析 [MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo) 官方上游代码时发现的潜在缺陷或架构瓶颈。
作为 Fork 版本，为了保障与上游代码的高兼容性，我们选择不直接侵入式修改这些核心流程，而是将其记录在此，作为未来提交 PR (Pull Request) 或本地深度重构的参考。

## 1. SSH Adapter 并发拨号性能瓶颈 (Thundering Herd 惊群效应)

### 诊断位置
`adapter/outbound/ssh.go` 的 `s.connect()` 方法。

### 缺陷详解
在官方设计中，建立 SSH 隧道的方法被一把全局级的互斥锁（`cMutex`）完全包裹：

```go
func (s *Ssh) connect(ctx context.Context, addr string) (client *ssh.Client, err error) {
	s.cMutex.Lock()
	defer s.cMutex.Unlock()
	// ... 极度耗时的网络 I/O (Dial, Handshake, Auth) 都在这里进行
}
```

**问题场景：**
当 Mihomo 代理引擎处于“冷启动”（SSH 隧道尚未建立），且前端浏览器瞬间发起了大量并发请求（例如打开一个包含 20 张图片的网页，或者使用并发下载工具）时，这 20 个请求会被同时路由到该 SSH 节点并并发调用 `s.connect()`。

**致命表现：**
1. 第 1 个请求竞争到了 `s.cMutex` 的锁，开始长达数秒（甚至 10 秒以上）的底层 SSH 拨号握手。
2. 剩余的 19 个并发请求全部在 `s.cMutex.Lock()` 处被**物理阻塞 (Block)**，进入死等状态。
3. 这些死等的请求不但毫无道理地增加了外层代理链的超时负担，而且它们在排队过程中，极有可能因为外层的 `Context` 已经达到了 `DialContext` 的默认超时时间（通常是 5 秒）而被系统掐断。
4. 结果就是：虽然隧道最终依靠第 1 个请求建立起来了，但在队列里排队的其余请求大概率会因为超时而在客户端报错 `timeout` 或 `DeadlineExceeded`。

### 工业级优化方案 (Singleflight 机制)

针对这种**“多个并发请求抢占同一个远程耗时资源”**的场景，标准的解法是使用 Go 并发模型中的 `golang.org/x/sync/singleflight`。

**重构思路：**
1. **缩减锁粒度**：`cMutex` 的作用应该仅仅是“检查和存取 `s.client` 指针状态”，这是一个内存级极速操作。
2. **读写分离**：
    - 先用 `cMutex.RLock()` 检查隧道是否已建好，建好了直接用。
    - 没建好，则调用 `singleflight.Group.Do`。
3. **流量合并 (Coalescing)**：`singleflight` 会保证对于同一个 `Ssh` 节点，无论瞬间涌入多少个 `connect` 请求，底层的网络拨号耗时函数只会被执行**一次**。
4. **统一广播**：当那唯一一次的拨号（真正耗时的 5 秒）完成后，`singleflight` 会瞬间把建立好的隧道句柄广播（唤醒）给门外等待的全部 19 个挂起请求。

**预计收益：**
- 彻底消除冷启动或网络抖动重连瞬间的并发排队报错。
- 极大释放 Goroutine 调度器压力，避免高并发下无意义的长时间 Mutex 争抢。
- 真正发挥 Golang 的高并发分发优势。

> **备注**：目前在当前 Fork 版本中暂不实施此优化，为了避免与官方主分支强耦合导致合并冲突。未来可作为独立 PR 提交给 Mihomo 官方。
