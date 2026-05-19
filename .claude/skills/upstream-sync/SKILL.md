---
name: upstream-sync
description: "同步上游 MetaCubeX/mihomo 最新 release tag 到新的 fa/<tag>-fa.0 分支。触发词：上游同步、sync upstream、合并上游、升级版本。"
---

# Upstream Sync — mihomo fork 升级流程

## 核心原则

> **冲突文件不能无脑取 ours，也不能无脑取 theirs。**
> 每个有冲突或被脚本自动策略接管的文件，都必须经过"上游 delta 复审 → 决策 → 应用 → 汇报"四步。
> 哪怕最终决定仍然 `--ours`，理由必须出现在最后的汇报中，给用户留下复盘依据。

## 执行流程

```
脚本（机械操作）─→ 退出码 0：进入 Step 2 复审
                  退出码 2：→ 进入 Step 2 复审 + Step 3 智能合并
                  退出码 3：构建失败 → 进入 Step 4 验证修复
```

---

## Step 1：运行脚本

```bash
./scripts/upstream-sync.sh
```

- **退出码 0**：脚本已自动 commit 但仍需 Step 2 复审。
- **退出码 2**：脚本输出 `SMART_MERGE_REQUIRED` 标记 + 冲突文件列表，进入 Step 2 + Step 3。
- **退出码 3**：构建失败，进入 Step 4。

> ⚠️ 脚本会对一组"已知策略"文件自动 `--ours`（见下方表格）。**这是省力起点，不是终点** —
> Step 2 必须复审这些文件，确认上游本次的改动不需要吸收。

---

## Step 2：复审脚本自动处理的文件（每次必做）

脚本默认对 fork 维护的 CI、更新器等文件取 `--ours`。但上游可能在这些文件里加入了
fork 也需要的修复（Go 版本升级、依赖安装、安全补丁、构建参数等），因此必须复审。

### 2-A. 确定上次同步基线

```bash
# 优先从上一次 "merge upstream" commit 的 message 中提取 tag
PREV_TAG=$(git log --merges --first-parent --grep="merge upstream" -1 --format=%s \
  | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)

# 兜底：用上次 merge commit 的第二 parent 反查 tag
[[ -z "$PREV_TAG" ]] && PREV_TAG=$(git describe --exact-match --tags \
  $(git log --merges --first-parent -1 --pretty=format:%P \
    | awk '{print $2}') 2>/dev/null)

echo "上次基线: $PREV_TAG → 本次目标: $TARGET_TAG"
```

### 2-B. 对每个脚本自动 --ours 的文件，看上游 delta

```bash
for f in .github/workflows/build.yml .github/workflows/test.yml \
         component/updater/update_core.go; do
  echo "=== $f ==="
  # 上游本次 tag 区间的提交摘要
  git log --oneline "$PREV_TAG".."$TARGET_TAG" -- "$f"
  # 上游侧的具体改动
  git diff "$PREV_TAG".."$TARGET_TAG" -- "$f"
done
```

### 2-C. 应用决策矩阵

| 上游 delta 类型 | 决策 | 操作 |
|----------------|------|------|
| 仅排版/注释/无关步骤 | 维持 `--ours` | 在汇报中标"无需吸收" |
| 安全补丁、CVE 修复、bug fix | **必须吸收** | 把上游 hunk 应用到 fork 版本 |
| Go 版本/依赖升级 | **多数要吸收**（除非 fork 故意锁定）| 同上 |
| 上游新功能 step | 视 fork 需求 | 若 fork 用得到，吸收 |
| fork 故意删除的功能 | 维持 `--ours` | 在汇报中说明为何不要 |
| 模糊地带 | 标记 `NEEDS_USER_REVIEW` | 暂保留 `--ours`，汇报中列出供用户拍板 |

吸收时用 `git show "$TARGET_TAG":"$f"` 取上游版本，手工把需要的 hunk 合到 fork 版本，
然后 `git add "$f"`。

### 2-D. 同时复审"git 自动合并成功"的文件

> 这是上次同步暴露的盲点。git 三向合并成功 ≠ 语义正确。
> 当上游对一个 fork 也修改过的文件做了"非冲突但有连带影响"的变更（例如换包路径、改函数签名），
> git 不会报冲突，但 fork 自有文件（如 `ssh_resilience.go`）会在编译时炸。

```bash
# 列出本次合并中由 git 自动消解的"双方都改过的文件"
git log -1 --merge --name-only --pretty=format: | sort -u
# 对每个文件再跑一遍 delta 审查
git log --oneline "$PREV_TAG".."$TARGET_TAG" -- <file>
```

对 fork 修改过的共享文件（特别是 `adapter/outbound/ssh.go`），逐个验证：
- 上游有没有改 import 路径？fork 自有文件用同样的包吗？
- 上游有没有改函数签名？fork 自有文件调用它的方式还兼容吗？
- 上游有没有新增/删除字段？fork 自有文件读写的字段还在吗？

发现连带影响时，把 fork 自有文件同步改造，作为合并完整性的一部分。

---

## Step 3：智能合并（脚本退出码 2 时）

对未被脚本自动策略覆盖、仍带冲突标记的文件（典型：`adapter/outbound/ssh.go`）。

### 3-A. 读取冲突上下文

```bash
# 1. 读含冲突标记的完整文件
cat <conflict-file>

# 2. 上游在 PREV_TAG..TARGET_TAG 改了什么
git diff "$PREV_TAG".."$TARGET_TAG" -- <conflict-file>

# 3. fork 相对上游基线改了什么（理解 fork 意图）
git diff upstream/Alpha...HEAD -- <conflict-file>
```

### 3-B. 分析逻辑（以 `adapter/outbound/ssh.go` 为例）

冲突来源：
- **上游** 修复 bug、添加新功能或重构
- **Fork** 加了 SSH 系统代理扩展（`socksExt`、`useSystemSsh`、`closed` 字段，
  以及 `UseSshConfigAlias`/`UseSystemSocks` 选项）

识别原则：
- `<<<<<<< HEAD` 块中哪些是 fork 扩展（保留）
- `>>>>>>> <TAG>` 块中哪些是上游改动（吸收）
- 两者修改同一逻辑段时（需融合）

**Fork 扩展的识别标志**（必须保留）：
- 字段/变量名含：`socksExt`、`systemSocks`、`useSystemSsh`、`closed`
- 选项名含：`UseSshConfigAlias`、`UseSystemSocks`、`SshUser`、`SshFlags`
- 注释含：`系统`、`system ssh`、`ssh-config`

**合并原则**：
1. 上游的结构性改动（函数签名变更、新增参数）优先吸收
2. Fork 的扩展字段/方法不丢失
3. 上游重构 + fork 修改同一函数 → 在上游新结构上重新植入 fork 扩展
4. 合并结果必须可编译

### 3-C. 写入合并结果

用 Edit 工具去除所有 `<<<<<<<`/`=======`/`>>>>>>>` 标记。

### 3-D. 标记已解决

```bash
git add <resolved-files>
```

---

## Step 4：验证（每次必须执行）

### 4-A. 编译验证

```bash
go build -tags with_gvisor -trimpath -ldflags '-w -s' ./...
```

失败时常见原因（按出现频率）：
1. **上游切换了第三方包**（如 `golang.org/x/crypto/ssh` → `github.com/metacubex/ssh`），
   fork 自有文件未同步 → 同步 import 路径
2. **上游改了函数签名**，fork 自有文件调用方式过时 → 改 fork 调用方
3. **上游新增依赖**：`git diff "$TARGET_TAG" HEAD -- go.mod`，跑 `go mod tidy`

### 4-B. 测试验证

```bash
# fork 核心模块
go test ./adapter/outbound/... -timeout 60s

# 全量回归
go test ./... -timeout 180s
```

测试失败时：判断是上游破坏性变更还是合并引入的问题，针对性修复。

---

## Step 5：提交与汇报

### 5-A. 合并 commit

```bash
git commit -m "chore: merge upstream <TAG>

<summary line per file class>"
```

如果合并 commit 已由脚本自动创建，Step 2-4 的修复作为独立 follow-up commits 提交：
- `fix(<module>): <adapt to upstream change>` — 包路径/签名同步
- `fix(scripts): <bug>` — 脚本本身的 bug 修复

### 5-B. 推送

```bash
git log --oneline -5
git push -u origin fa/<TAG>-fa.0
```

### 5-C. 汇报模板（给用户）

```markdown
## 上游同步报告：v<TAG>（基线 v<PREV_TAG>）

### 1. 脚本自动 --ours 文件复审
| 文件 | 上游本次 delta | 决策 | 理由 |
|------|---------------|------|------|
| .github/workflows/build.yml | <一句话摘要> | 维持 ours / 部分吸收 / NEEDS_REVIEW | <理由> |
| .github/workflows/test.yml | ... | ... | ... |
| component/updater/update_core.go | ... | ... | ... |

### 2. 智能合并文件
| 文件 | 上游意图 | fork 意图 | 融合策略 |
|------|---------|----------|---------|
| adapter/outbound/ssh.go | <摘要> | <摘要> | <摘要> |

### 3. 自动合并成功但触发连带修复的文件
| fork 文件 | 上游连带影响 | 修复 |
|----------|-------------|------|
| adapter/outbound/ssh_resilience.go | 上游 ssh.go 切包 golang.org/x/crypto/ssh → github.com/metacubex/ssh | 同步 import |

### 4. 验证结果
- 编译：✓ / ✗
- 测试：N 个 passed / 失败列表
- 推送：origin/fa/v<TAG>-fa.0 @ <short sha>

### 5. NEEDS_USER_REVIEW（如有）
- <文件>：<上游改动> — <为何拿不准，问用户>

### 6. 异步事项（可选清理，不阻塞当前任务）
- <死代码 / 索引过期 / 文档修订建议>
```

> 汇报必须出现在主对话最后一条消息，**不写到任何文件**。

---

## Fork 改动范围说明

该 fork 在上游基础上做两件事：
1. **SSH 系统级代理**（读 `~/.ssh/config`，调用系统 `ssh` 二进制）
2. **自定义传输协议** `sudoku`、`trusttunnel`

**Fork 新增文件**（上游无，不会产生冲突）：
```
adapter/outbound/ssh_system.go
adapter/outbound/ssh_system_helper.go
adapter/outbound/ssh_system_socks.go
adapter/outbound/ssh_resilience.go
adapter/outbound/sudoku.go
adapter/outbound/trusttunnel.go
transport/sudoku/...
transport/trusttunnel/...
listener/sudoku/... listener/trusttunnel/...
listener/inbound/sudoku.go listener/inbound/trusttunnel.go
```

> ⚠️ 上述 fork 自有文件不会出现合并冲突，但**可能因上游改动间接受影响**
> （例：基类签名变更、共享 import 路径切换）。Step 2-D 与 Step 4-A 是兜底点。

**Fork 修改的上游共享文件**（冲突高发区）：

| 文件 | Fork 的改动 | 默认处理 | 必做复审 |
|------|------------|---------|---------|
| `adapter/outbound/ssh.go` | 扩展 `Ssh`/`SshOption`，加系统代理分支 | Claude 智能合并 | 是 |
| `component/updater/update_core.go` | 替换下载 URL 指向 fork 仓库 | `--ours` | Step 2-B 复审 |
| `.github/workflows/build.yml` | fork 精简 CI | `--ours` | Step 2-B 复审 |
| `.github/workflows/test.yml` | fork 精简 CI | `--ours` | Step 2-B 复审 + patch 文件引用核查 |

> ⚠️ `test.yml` 取 ours 后必查 `grep -oE '\.github/patch/[A-Za-z0-9_/-]+\.patch'`，
> 确认所有静态引用的 patch 文件都存在于 `.github/patch/`。
> 变量插值如 `go${{matrix.go-version}}.patch` 看 matrix 是否会展开成实际文件。

---

## 快速参考：冲突文件决策

| 冲突文件 | 默认策略 | 复审强度 |
|---------|---------|---------|
| `.github/workflows/*.yml`（CI） | `--ours`（脚本自动） | **必做** Step 2-B |
| `component/updater/update_core.go` | `--ours`（脚本自动） | **必做** Step 2-B |
| `adapter/outbound/ssh.go` | Claude 智能合并 | Step 3 |
| Fork 新增文件（`ssh_system*.go`、`sudoku*.go` 等） | `--ours` | Step 2-D 检查上游是否新增同名文件（异常信号）|
| 其他上游文件（fork 未改过） | `--theirs` | Step 4 编译/测试兜底 |
| **git 自动合并成功的 fork 也改过的文件** | （无冲突标记） | **必做** Step 2-D — 上次同步在此踩过坑 |
