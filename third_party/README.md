# third_party/ — fork 补丁过的依赖

## 为什么有 `easytier-go/`

`component/easytier` 通过 `github.com/easytier/easytier/easytier-go` 把 EasyTier 以 **WASI guest**（wazero）方式跑在内核里。
该依赖内部在 `internal/engine/host.go` 用 **`wazero.NewRuntime(lifetime)`** 创建运行时 —— 那是 **编译器运行时（JIT）**，
且**没有对外注入口子**（`Options` 里没有 runtime 配置）。

macOS 上这会致命：

- 服务模式（特权服务 approval/stage 内核）会给内核副本重签名，签名带上 **hardened runtime** 而**没有 `com.apple.security.cs.allow-jit`**；
- easytier 是 mihomo 里**唯一**使用 JIT 的路径，JIT 生成的可执行页被判定为非法页；
- 结果内核被系统 SIGKILL，崩溃报告为 `EXC_BAD_ACCESS` + `SIGKILL (Code Signature Invalid)` +
  `termination: {namespace: CODESIGNING, indicator: Invalid Page}`，表现为"一碰 easytier 内核就重启"。

所以本 fork 把该依赖的那一行改为**解释器运行时**，彻底不生成机器码：

```go
// 原：runtime := wazero.NewRuntime(lifetime)                                      // 编译器 / JIT
// 现：runtime := wazero.NewRuntimeWithConfig(lifetime, wazero.NewRuntimeConfigInterpreter())
```

行为差异只在性能：WASI 走解释器会慢一些，但 **TUN 模式下数据面走原生路径**，家里内网吞吐不受影响
（实测：解释器版首次请求含实例懒启动 0.58s，之后 `10.0.0.1` 88ms）。

## 内容

| 文件 | 说明 |
| --- | --- |
| `easytier-go/` | `github.com/easytier/easytier/easytier-go` 的副本，仅改了上面那一行（`testdata/` 为省体积已删，只影响该依赖自身的测试） |
| `easytier-go-interpreter.patch` | 相对原始模块的 diff（`internal/engine/host.go`），用于升级依赖时重新套用 |
| `setup-easytier-interpreter.sh` | 从模块缓存重新生成 `easytier-go/` 并套用补丁（依赖升级后跑它） |

`go.mod` 里对应：

```
replace github.com/easytier/easytier/easytier-go => ./third_party/easytier-go
```

## 上游升级依赖时

```bash
# 1) 更新 go.mod 里的 require 版本
go get github.com/easytier/easytier/easytier-go@<new-version>
# 2) 重新生成补丁版副本（脚本会从模块缓存取对应版本并套用补丁）
third_party/setup-easytier-interpreter.sh
# 3) 若补丁冲突，手工比对 internal/engine/host.go 里创建 runtime 的那一行
go build -tags with_gvisor -trimpath -ldflags '-w -s'
```

上游若自行提供了运行时选择（例如 `Options` 里加 runtime 配置），应改为走上游接口并删除本目录。
