# 运维 SOP(全自动改配置 + 重建内核)

> 目标:**改配置这件事不需要人点 GUI**;需要 root 的动作收敛到一个**极窄的特权助手**里;
> **先回滚,再查因**。适用于服务模式(macOS / Linux 上把内核交给特权服务托管)的部署。

## 0. 为什么需要它

服务模式下的内核**不读** `clash-verge.yaml`(那是 GUI 的生成物 ✓),它读的是服务托管的运行时目录:

```text
-runtime: <服务数据目录>/users/<uid>/runtime     ← 配置文件在这里,root-only ✗
```

该目录**用户不可写**,而且内核的配置重载 API 只接受**这个目录内**的路径
(实测报错:`path is not subpath of home directory or SAFE_PATHS`)。GUI(如 Clash Verge Rev)背后的
特权服务有 `stage-runtime` / `start` / `stop` 命令,但它们的鉴权依赖 **GUI 每会话生成的 token**
(服务端只存 hash),**外部无法伪造** —— 这是**该有的安全边界**,不要绕。

⇒ 因此"自动化"的正确形态是:一次性安装一个**只做一件事**的 root helper + 一条 sudoers 白名单。

## 1. 一次性安装(只需一次管理员授权)

两个文件:一个 `sh` 脚本(`stage|restart|status|rollback|core-upgrade|core-rollback` **六个子命令**,
**不接受自由参数**)+ 一条白名单规则。

```bash
sudo install -d -o root -g wheel -m 755 /usr/local/libexec/clash-group
sudo install -o root -g wheel -m 755 et-apply /usr/local/libexec/clash-group/et-apply
sudo visudo -c -f sudoers          # ★ 先校验,再落盘(防坏规则弄坏 sudo)
sudo install -o root -g wheel -m 440 sudoers /etc/sudoers.d/clash-group-et-apply
```

白名单(**枚举子命令,不用通配符**):

```text
<用户> ALL=(root) NOPASSWD: /usr/local/libexec/clash-group/et-apply stage
<用户> ALL=(root) NOPASSWD: /usr/local/libexec/clash-group/et-apply restart
<用户> ALL=(root) NOPASSWD: /usr/local/libexec/clash-group/et-apply status
<用户> ALL=(root) NOPASSWD: /usr/local/libexec/clash-group/et-apply rollback
<用户> ALL=(root) NOPASSWD: /usr/local/libexec/clash-group/et-apply core-upgrade
<用户> ALL=(root) NOPASSWD: /usr/local/libexec/clash-group/et-apply core-rollback
```

> ⚠️ **不要把"零参数约束"`""` 加到规则末尾** ✗ —— 实测某些 sudo 版本上它**不匹配**(`sudo -l` 会列出规则,
> 但真正调用时仍要密码)。这里的安全性不靠参数约束:helper **只认 `$1` 的六个固定词**、忽略其余参数、
> 所有路径与 URL 写死、**不接受任何"用户指定的文件/URL"** ⇒ 传参改变不了它能做什么 ✓。

脚本必须满足四条(**缺一条就不要装**):

1. **root 属主、模式 755、用户不可写** ✓(否则用户改脚本 = 提权 ✗);
2. **落盘前用内核自己的二进制校验**:`<core> -t -d <runtime> -f <candidate>` ✓(坏配置直接丢弃 ✗);
3. **改动前自动备份** + **重启后不健康自动回滚** ✓;
4. **审计日志**(如 `/var/log/clash-group-et-apply.log`)+ 日志/输出不含秘密 ✓;
5. **内核升级路径必须自我校验 + 自动回滚** ✓:从**固定发布通道**取版本 ⇒ 用发布方给出的 **sha256 摘要**校验 ⇒
   让候选二进制**自证版本**(`-v`)⇒ 用**当前生产配置**跑一遍解析(`-t -d <runtime> -f <config>`)⇒
   备份旧核 ⇒ 原子替换 ⇒ ad-hoc 重签(`codesign --force --sign - --timestamp=none`,**绝不加** `--options runtime` ✗)⇒
   重启并**按版本回读核对**(不是"进程活着"就算 ✓)⇒ 任一步失败**自动回滚旧核** ✓。

> 权限面说明:配置来源文件由普通用户提供 ⇒ 与 GUI 自身的权限**等价**(GUI 本来就让该用户 stage 配置),
> 所以没有**新增**权限面;但不要把这个 helper 扩成"任意命令" ✗。

## 2. 日常操作

```bash
# ★ 唯一手改的文件:overlay.yaml(我们这层的小改动,~1.3 KB)
<改配置>   $EDITOR <状态目录>/et-apply/overlay.yaml     # 只写"我们的增量",不复制整份配置 ✗
<看变化>   et-config diff                              # 只读:列出相对 CVR 当前产物会做什么 ✓
<生效>     et-config apply                             # build → stage(-t 校验) → restart ✓ 幂等 ✓
<看状态>   sudo -n et-apply status
<回滚>     sudo -n et-apply rollback

# 内核（全自动,无需人工授权/无需点界面）
<升级内核> sudo -n et-apply core-upgrade     # 取最新发布 → 校验 → 备份 → 装 → 重签 → 重启 → 按版本核对
<回滚内核> sudo -n et-apply core-rollback    # 回到上一个自动备份的内核
```

> 说明:内核升级**不需要**经过 GUI ✗ —— helper 直接在文件/进程层完成(换二进制 + 重建内核进程)✓,
> 因此不依赖辅助功能权限,也不依赖任何界面操作 ✓。

**"完整配置"从哪来**:GUI 的生成物(订阅 + 所有 Merge/本地 profile 的合并结果)。
只改自己的 profile 时,也可以在其副本上改。

## 3. 测试纪律(血换的,违反就会得出错误结论)

```text
顺序:重载/重建 → 明确切到目标节点 → **回读确认选中** → 才测
页面级:用真浏览器自动化(不要用探测 API 代替页面的成功)
链路级:注意"有没有系统代理"—— 系统代理关闭时,**没有"直连",只有"经 TUN",两者别混叫**
地面真相:内核的 /connections(rule / chains / up / down 字节计数)
```

| 仪器 | 判定 |
| --- | --- |
| `/connections` 的 up/down 字节计数 | ✅ 可信 |
| 经 TUN 的 `curl`(= 浏览器同路) | ✅ 可信 |
| 真浏览器自动化 | ✅ 页面级唯一可信 |
| `/proxies/<**组**>/delay` | ❌ **不走该组选中的出站** ⇒ 结论无效 |
| `/proxies/<节点>` 的 `alive` 字段 | ❌ 常为陈旧读数 ⇒ 要**实测** |
| 用 ICMP ping 判连通 | ❌ 隧道/代理场景不可信(一律用 TCP) |

## 4. 已知故障模式

| 症状 | 原因 | 处置 |
| --- | --- | --- |
| 内网域名超时 | 分流组被重置到错误项 ✗ / 该域名被 fake-ip 换成假地址 ✗ | 组里第一项放正确默认 ✓;把业务域名加入 `dns.fake-ip-filter` ✓ |
| **借出口(对端借本机上网)超时** | **TUN 设备代际残留**:重载后旧设备未回收,`/24` 路由仍指向旧设备 ✗ | 急用:`et-apply restart`(整进程重建)✓;**根因已在源码侧修复**(路由夺回 ✓ + 桥 finalizer ✓)⇒ 正常重载不再留残留 ✓ |
| 内网流量被送去机场 | 内网规则指向了一个带 `dialer-proxy` 的**出口**节点 ✗ | 出口节点**不得**放进分流组 ✓ |
| 一台机器加第二个节点后借网断 | 两个节点**抢同一段 overlay 路由** ✗ | **一机一节点** ✓ |
| 重载后行为突变 | 重载会重置选择器 ✗ | 组内第一项 = 正确默认值 ✓ |
| 手工改 runtime 后又变回去 | GUI 的"重载配置"会用它的合并结果覆盖 ✓(正常) | 要长期生效 ⇒ 改 **profile 源文件** ✓ |

## 6. 配置变更入口（用户侧 `et-config`，2026-10-21 立）

```text
为什么：CVR 每次应用订阅都会重写合并产物（机场节点域名会轮换）。
        整份快照会把旧数据冻结回写 ✗；叠加模式每次都从**当前产物**重算 ✓。

分层：CVR 合并产物（基座，只读） + overlay.yaml（我们的改动，~1.3 KB）
      └─ et-config build ─▶ 候选 config.yaml ─▶ sudo et-apply stage（-t 校验）─▶ restart
      ⇒ root 助手保持"只校验只落盘"，不理解业务语义 ✓

命令：
  et-config diff     只读：打印相对基座会产生哪些变化 ✓
  et-config build    只生成候选，不安装 ✓
  et-config apply    build + stage + restart（幂等：重复执行结果逐字节相同 ✓）
  et-config show     打印 overlay（敏感值打码 ✓）

overlay 只支持 4 种幂等操作：`+` 并集 / `=` 设值 / `prepend` 前插 / `remove` 精确删。
  · 没有 diff/上下文匹配 ⇒ 不会"打半份"✗
  · 删掉 overlay 里的一条 ⇒ 下一次它就不再出现 ✓（无历史残留）
  · 基座缺失/结构变化 ⇒ 校验失败 ⇒ **不落盘** ✓
  · overlay 含 network-secret ⇒ **0600、不入 git** ✓

⚠️ **apply 会重启内核 ⇒ 所有 select 分组的运行期选择被重置** ✗（回到候选列表首项）。
   ⇒ 每次 `apply` 之后必须重设：`内网入口`（现场 DIRECT / 离网入口节点）、`🇨🇳 国内网站` 等 ✓
   ⇒ 否则会误判成"隧道坏了"（本次踩过：国内组被重置到死节点 ⇒ 公网站 000 ✗）
```

## 7. 两个订阅：平级、互斥、三步自检（2026-10-24 立）

```text
① frp+ssh 订阅   ：只用于管理家侧 mihomo（改配置）
② easytier 订阅  ：被验收的产品通路
两者平级互斥：同一时刻只应有一个在配置里。不存在"规则互相遮蔽"的问题——不要写兼容补丁。
```

**标准流程**（每步都要看结果，而不是只看"能连上"）：

| 步骤 | 动作 | 期望 | 自检 |
| --- | --- | --- | --- |
| 1 | 激活 frp+ssh 订阅 | 能连家侧 | `et-net-mode check bypass` |
| 2 | **停用** frp+ssh 订阅 | **连不上**家侧 | `et-net-mode check isolated` |
| 3 | 换成 easytier 订阅 | 内网目标可达 | `et-net-mode check easytier` |

`check easytier` 的三个判据（缺一不可）：

1. `utun1024` 存在（TUN 缺失时流量绕过内核，读数无意义）；
2. frp+ssh 订阅的节点**不在**运行配置里（还在 = 没停干净）；
3. 内网探测目标返回 `200`。

**两个真实缺陷（已踩过）**

1. **TUN 是延迟创建的**：重启后 6 秒还没有、10 秒才出现 ⇒ 立刻采样会得到假阴性 ✗
   ⇒ preflight 轮询最多 20 秒；20 秒仍缺失才判失败。
2. **utun 设备会累积**（实测每次重启约泄漏 1 个）⇒ 内核侧待修缺陷，不阻塞验收但要记账。

## 5. 回滚入口

```text
本机 runtime : et-apply rollback(恢复最近一次备份 ✓)
内核二进制   : `et-apply core-rollback`(自动备份回滚 ✓);手工兜底见 service-mode-sigkill.md ✓
对端网关     : 部署目录的备份 ⇒ docker compose -p <项目> up -d --force-recreate <服务>
对端宿主终端 : ip -o link show | grep Meta 应为空 ✓、default via <原网关> ✓
```
