# 规格：EasyTier 出站 TUN 模式（fork-only）

> 状态：草案 → 实现中。本文是 SDD 产物：先定契约与验收标准，再写测试，最后写实现。
> 分支：`fa/easytier-tun`。上游共享文件的改动必须小块 + `// FORK:` 标注。

## 1. 问题与目标

mihomo 内嵌的 EasyTier 核（WASI）**永远强制 `no_tun = true`**（`component/easytier/toml.go` 的
`ApplyRequiredFlags`），于是它没有 L3 接口，只能把每条流交给宿主逐协议转发。实测后果：

- 作为**客户端**（主动拨号）可用：与 native 家侧配对时 20MB @ 8.4 MB/s；
- 作为**被访问端**不可用：WASI↔WASI 的 TCP 通路建立不起来（家侧宿主零中继调用）。

目标：给 `easytier` 出站一个可选的 **TUN 模式**——宿主（mihomo）用 `sing-tun` 建一块 TUN 设备，
用 `SendPacket`/`ReceivePacket` 把裸 IP 包在设备与 EasyTier 实例之间搬运。这样节点成为真正的 L3
接口，**TCP/UDP/ICMP 一视同仁**，家侧 mihomo 也能充当内网网关。

## 2. 范围

**In scope（v1）**
- `tun` 开关：建立宿主 TUN + 双向包面桥接 + 生命周期管理。
- `tun-routes`：静态前缀列表 → `Inet4RouteAddress`（哪些网段 pin 进这块 TUN）。
- `no_tun` 条件化：`tun: true` 时不再强制 `no_tun = true`，改为显式 `no_tun = false`。
- 单测（不依赖特权）+ 实验室 E2E 验收。

**Out of scope（v1，明确不做）**
- 运行期动态增删路由（跟随规则集 / peer 广告）。理由：mihomo 自己对 TUN 路由表也是"建时确定、
  配置变更时重建"（见 `docs/easytier_home_gateway.md` 与 `listener/sing_tun/server.go`）。
- 客户端侧自动路由管理（B 方案的动态部分）。
- Windows 专项验证（sing-tun 支持 wintun，但本版本不承诺）。
- DNS：不写代码，复用 `et://` + `nameserver-policy`（见第 6 节）。

## 3. 契约

### 3.1 新增出站选项

| 选项 | 类型 | 默认 | 语义 |
| --- | --- | --- | --- |
| `tun` | bool | `false` | 建立宿主 TUN 设备并接上包面。`false` 时行为与今天**完全一致**（回归红线）。 |
| `tun-routes` | []CIDR | 空 | pin 进这块 TUN 的前缀列表，例如 `["10.0.0.0/24"]`。空表示不装任何路由（家侧网关场景无需路由）。 |

复用既有选项：`ipv4`（TUN 设备地址，**`tun: true` 时必填**）、`mtu`（设备 MTU，默认 1380）。

### 3.2 生成的 TOML

| 条件 | `[flags]` 输出 |
| --- | --- |
| `tun: false` | `no_tun = true`（现有行为，含覆盖用户写入的值） |
| `tun: true` | `no_tun = false`（新增：覆盖 `true`，缺失时注入） |

`bind_device = false` 两条路径都保持强制。

### 3.3 设备参数

| 参数 | 值 | 依据 |
| --- | --- | --- |
| `Name` | `sing-tun` 自动命名（前缀 `easytier`），避免与 mihomo 自己的 TUN 撞名 | `listener/sing_tun/tun_name_*.go` 同类做法 |
| `Inet4Address` | `[ipv4]` | 官方范例 `examples/tun` |
| `MTU` | `mtu`（默认 1380） | 官方范例 `tunMTU = 1380` |
| `AutoRoute` | `false` | 官方范例；绝不抢默认路由 |
| `StrictRoute` | `false` | 同上 |
| `GSO` | `false` | 官方范例 |
| `Inet4RouteAddress` | `tun-routes` | sing-tun 负责安装，不手写 `route add` |
| `InterfaceMonitor` | v1 用 no-op stub（与官方范例一致）；后续如需接口变化维护，复用 `tun.NewDefaultInterfaceMonitor` | YAGNI |

### 3.4 生命周期与错误

1. `instance.Start()` 成功后：解析 `ipv4` → 建 TUN → 启动两条循环。
2. 循环：`device.Read` → `instance.SendPacket`；`instance.ReceivePacket` → `device.Write`。
   两者都在出站 `ctx` 取消时干净退出（与包面 API 的 context 语义一致）。
3. `ipv4` 缺失或非法：**返回明确错误**，不建设备、不 panic。
4. TUN 创建失败（无 `root`/`CAP_NET_ADMIN`）：返回可读错误（含"需要提升权限"提示），
   并保证 `shutdown` 不泄漏设备/goroutine。
5. `Close()`/`shutdown()`：取消 ctx → 等循环退出 → 关闭设备（幂等）。

## 4. 验收标准（可执行）

| 编号 | 判据 | 手段 |
| --- | --- | --- |
| AC1 | `go build -tags with_gvisor ./...` 通过 | CI/本地 |
| AC2 | `tun: false` → TOML 含 `no_tun = true`；`tun: true` → 含 `no_tun = false` | `component/easytier/toml_test.go` |
| AC3 | `tun: true` 且 `ipv4` 缺失 → 明确错误 | `adapter/outbound/easytier_test.go` |
| AC4 | 包面桥接两方向 + ctx 取消干净退出（假设备 + 假实例） | 新增单测 |
| AC5 | E2E：家侧 mihomo（`tun: true`，发布 `proxy-networks`）+ 本机 Clash → **TCP 20MB SHA-256 一致** | 实验室 |
| AC6 | E2E：同拓扑 **UDP 40 包 0 丢包** | 实验室 |
| AC7 | E2E：**`ping` 内网另一台机器成功**（no-TUN 做不到的 ICMP） | 实验室 |

## 5. 复用清单（不新写的部分）

| 能力 | 来源 |
| --- | --- |
| 路由安装 | `tun.Options.Inet4RouteAddress`（sing-tun） |
| 设备创建 | `tun.New`（sing-tun，已是依赖） |
| 包面 | `corehost.Instance.SendPacket/ReceivePacket` |
| 命名 | `listener/sing_tun/tun_name_*.go` 同类做法 |
| 重建模式（将来） | `listener/listener.go:498 ReCreateTun` |
| 规则集→CIDR（将来） | `listener/sing_tun/server.go` 的 `ruleUpdateCallback`/`ToIpCidr` |
| DNS | `et://`（`config/config.go:1255`）+ 出站已注册（`adapter/outbound/easytier.go:163`） |

## 6. DNS 使用方式（文档级，零代码）

```yaml
dns:
  nameserver-policy:
    "et.net.": "et://home-overlay"        # overlay 节点名（A/PTR）
    "+.home.lan": ["10.0.0.1"]            # 家里 DNS，查询经规则/路由到达
  respect-rules: true
  proxy-server-nameserver: ["223.5.5.5"]  # respect-rules 必填，否则启动报错
```

**不要把 53 流量 pin 进这块 TUN**：`dns-hijack` 只作用于 mihomo 自己的 TUN 入站，进 easytier TUN
的 DNS 不会被劫持，会丢掉 fake-ip/域名分流。

## 7. 风险与未验证项

| 项 | 说明 | 处置 |
| --- | --- | --- |
| **未验证** | spike 中 packet 面只出现控制面包，代理子网业务包未见。可能 guest 需要宿主环境快照 | 建完桥接立即用 AC5 判定；若不通则补 `platform.Services.Snapshot`（新文件 ~60 行）后复测 |
| 权限 | TUN 需 root/CAP_NET_ADMIN | 文档说明；家侧容器需 `NET_ADMIN` + `/dev/net/tun` |
| 平台 | macOS utun / Linux tun | sing-tun 已覆盖；本机 macOS + 容器 Linux 双端实测 |
| 与 mihomo TUN 共存 | 两块 TUN | 见 `docs/easytier_home_gateway.md`：建时确定 + 最长前缀匹配，不抢默认路由 |
| 上游冲突 | `toml.go` / `easytier.go` 是共享文件 | 改动小块 + `// FORK:`；新增代码集中在 `component/easytier/tun.go` |
