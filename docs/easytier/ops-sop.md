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

## 3.1 重启类验收必须留间隔（2026-10-25 实测）

```text
launchd 对同一 job 的连续重启有 ~10s 节流：背靠背 restart 三次实测 1.68 / 10.36 / 10.38s，
留 12s 间隔后是 1.83 / 1.39 / 1.30s。
⇒ 验收"重启有多快"时必须间隔 ≥12s；背靠背测到的是节流，不是重启本身。
⇒ 生产用法本来就是单次重启，所以 1.3s 这个数量级才是该对标的读数。
```

## 4. 已知故障模式

| 症状 | 原因 | 处置 |
| --- | --- | --- |
| 内网域名超时 | 分流组被重置到错误项 ✗ / 该域名被 fake-ip 换成假地址 ✗ | 组里第一项放正确默认 ✓;把业务域名加入 `dns.fake-ip-filter` ✓ |
| **借出口(对端借本机上网)超时** | **TUN 设备代际残留**:重载后旧设备未回收,`/24` 路由仍指向旧设备 ✗ | 急用:`et-apply restart`(整进程重建)✓;**根因已在源码侧修复**(路由夺回 ✓ + 桥 finalizer ✓)⇒ 正常重载不再留残留 ✓ |
| 内网流量被送去机场 | 内网规则指向了一个带 `dialer-proxy` 的**出口**节点 ✗ | 出口节点**不得**放进分流组 ✓ |
| 一台机器加第二个节点后借网断 | 两个节点**抢同一段 overlay 路由** ✗ | **一机一节点** ✓ |
| 重载后行为突变 | 重载会重置选择器 ✗ | 组内第一项 = 正确默认值 ✓ |
| 手工改 runtime 后又变回去 | GUI 的"重载配置"会用它的合并结果覆盖 ✓(正常) | 要长期生效 ⇒ 改 **profile 源文件** ✓ |

### 4.1 某公网目标 000，但家侧直连正常（airport 节点/外部故障，2026-10-25）

```text
症状：经 mixed-port 与 --noproxy 都是 000（TLS 握手超时/EOF），但 `nc` 能连上 443，家侧直连 302。
判断顺序：
  1) 查 `/connections` 的 chains：若命中 airport 节点（如 `<机场节点>`）⇒ 先怀疑节点到该目标的通路；
  2) 家侧直连同一目标（`ssh 家侧 curl --noproxy '*'`）⇒ 能通说明服务端与线路没问题；
  3) 再查规则：`/rules` 里是否有把该域名送进隧道的规则（见 4.2）。
⇒ 结论写法要区分"路径错"与"外部故障"，别把外部故障当成配置回归。
```

### 4.2 overlay 里 `rules_prepend` 与 `rules_remove` 同时出现同一条规则（会自我抵消）

```text
现象：明明在 rules_remove 里删了某条域名规则，运行配置里它还在（且排在前面）。
原因：rules_prepend 又把同一条加回来了 ⇒ 删除无效。
修法：删规则时**同时**检查两个键；判据 = `/rules` 里搜不到该 payload，
      再用 `/connections` 的 chain 证明流量不再进隧道（不能只看"我改过文件"）。
```

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

overlay 的操作模型是幂等的：`+` 并集 / `=` 设值 / `prepend` 前插 / `remove` 精确删；
顶层键按语义合并（列表按值去重、`proxies`/`proxy-groups` 按 name upsert），
`sniffer` 是**整体替换**（不是合并；base 将来若带 sniffer 会被覆盖）。
  · 没有 diff/上下文匹配 ⇒ 不会"打半份"✗
  · 删掉 overlay 里的一条 ⇒ 下一次它就不再出现 ✓（无历史残留）
  · 基座缺失/结构变化 ⇒ 校验失败 ⇒ **不落盘** ✓
  · overlay 含 network-secret/凭据 ⇒ **0600、不入 git** ✓

⚠️ **apply 会重启内核 ⇒ 所有 select 分组的运行期选择被重置** ✗（回到候选列表首项）。
   ⇒ 每次 `apply` 之后必须重设：`内网入口`（现场 DIRECT / 离网入口节点）、`🇨🇳 国内网站` 等 ✓
   ⇒ 否则会误判成"隧道坏了"（本次踩过：国内组被重置到死节点 ⇒ 公网站 000 ✗）
```

### 6.1 按域名走家侧出口（2026-10-25 实测）

适用：某内网域名在本机出口被阻断、家侧可达，且**不想**把公网 IP 写死。
原理、A/B 与边界见 `feature-matrix.md`「二·补三」。

步骤：

1. **先**给家侧 mixed-port 开认证（否则等于开放代理；家侧是 host 网络，7890 监听 `*`）：
   ```yaml
   authentication:
     - <user>:<pass>
   ```
   改完 `docker compose restart`（bind mount 改文件**不会**自动重启）；验证无凭据 407。
2. 本机 overlay 加三样（完整 YAML 见 `feature-matrix.md`「二·补三」）：
   - `et-home`：`type: http` + 家侧 overlay IP:7890 + `username/password` + **`dialer-proxy: et-lan`**
   - `rules_prepend: DOMAIN-SUFFIX,<后缀>,et-home`
   - `sniffer`：`enable: true` + `TLS: [443]` + `HTTP: [80]`
     · 本项目**实测最小集就够**：删掉 `parse-pure-ip`、`force-dns-mapping`、扩端口后，
       TUN 与 mixed 两路仍 302（见 feature-matrix 的 A/B）
     · 若换内核版本后 TUN 路径不生效，再按序加回 `parse-pure-ip: true` → `force-dns-mapping: true`
       并每次复测；不要一次全开（否则无法归因）
3. 备份 overlay 并记 sha（**et-apply 备份的是 runtime config，不含 overlay**），
   命名要能区分"变更前基线"与"中途编辑态"：
   ```bash
   cp -p ~/.local/state/clash-group/et-apply/overlay.yaml \
         ~/.local/state/clash-group/et-apply/overlay.yaml.baseline-<用途>-$(date +%Y%m%d-%H%M%S)
   shasum -a 256 ~/.local/state/clash-group/et-apply/overlay.yaml.baseline-*
   ```
   ⚠️ 回滚点是**变更前**那份；中途编辑出来的备份不能当基线（否则回滚后 et-home 仍在）。
4. `et-config apply`（不要加 `--no-verify`；用 mixed-port 探针把关）→ 选择器若被重启重置，按快照恢复。
5. 判据：TUN 与 mixed-port 两路 200/302；`/rules` 首条为域名规则、旧 `→内网入口` 规则 0 条；
   内网目标与其它公网回归不变；家侧无凭据 407。
6. 回滚：把**变更前** overlay 恢复回去 → `et-config apply`（只恢复文件不 apply 等于没回滚）。

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

## 8. 本机私有状态：`local-state/`（2026-10-25 立）

```text
不入 git 的东西统一放**项目根目录**的 local-state/，便于检索，且不会被仓库打包带走。

  目录 0700、文件 0600
  probes.conf    验收探针（<url> <期望码[,码...]>；any = 非 000）
  sensitive.txt  仓库脱敏守卫的 denylist（真实内网域名/IP/网段）

引用方：
  ~/.local/bin/et-net-mode   → PROBES
  ~/.local/bin/et-config     → PROBES
  docs/easytier/check-templates.sh → ET_DENYLIST（默认值指向该目录）

⚠️ 项目根目录本身不是 git 仓库；不要把 local-state/ 交给任何仓库或打包外发。
```

## 9. root 动作的执行姿势（无 TTY 时，2026-10-25 实测）

```text
① 需要管理员权限时，用 macOS 原生授权 UI（密码只进系统对话框，不经过脚本/agent）：

   osascript -e 'with timeout of 300 seconds' \
             -e 'do shell script "/绝对路径/脚本.sh" with administrator privileges' \
             -e 'end timeout'

   失败时 AppleScript 只报错误号、看不到 stdout ⇒ 让脚本把输出重定向到 /tmp/<log> 再读。
② 不要索取、转存或回显用户密码；也不要用 askpass 之类把密码塞进环境变量。
③ root 的 PATH 可能被重置/很小（不要假设它包含 /opt/homebrew 等）⇒ 脚本内一律用绝对路径调外部命令。

④ **shell 变量后紧跟中文/全角字符时必须写成 `${var}`**（本轮部署脚本踩到，已实测复现）：

   /bin/bash 3.2.57，UTF-8 locale：
     set -u; bak=ABC; echo "$bak（"   ⇒ bak�: unbound variable   ✗（即使已赋值）
     set -u; bak=ABC; echo "${bak}（" ⇒ 正常                         ✓
   C/POSIX locale 下按字节处理，非 ASCII 字节会终止变量名 ⇒ 同一行不报错（易误判）。
   ⇒ 与 locale + set -u 相关，且报错只显示变量名、该行恰好带中文，极易误判成"变量未初始化"。
   ⇒ 规范：变量先初始化 + 引用一律 `${var}`（尤其后面紧跟中文/全角字符时）。
   注：`$DENY）` 这类写法在本轮守卫脚本里也真实触发过一次。
```

