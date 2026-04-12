---
name: upstream-sync
description: "同步上游 MetaCubeX/mihomo 最新 release tag 到新的 fa/<tag>-fa.0 分支。触发词：上游同步、sync upstream、合并上游、升级版本。"
---

# Upstream Sync — mihomo fork 升级流程

## 执行流程

```
脚本（机械操作）─────────────────→ 退出码 0：完成
                                  退出码 2：→ Claude 智能合并 → 验证
```

---

## Step 1：运行脚本

```bash
./scripts/upstream-sync.sh
```

- **退出码 0**：自动完成，跳到 Step 4（推送）。
- **退出码 2**：脚本输出 `SMART_MERGE_REQUIRED` 标记和冲突文件列表，进入 Step 2。
- **退出码 3**：构建失败，进入 Step 3（构建修复）。

---

## Step 2：Claude 执行智能合并（退出码 2 时）

脚本已处理所有"固定策略"冲突，剩余文件需要理解逻辑后合并。

### 2-A. 读取冲突上下文

对每个冲突文件，依次执行：

```bash
# 1. 读取含冲突标记的完整文件
cat <conflict-file>

# 2. 查看上游在此次 tag 改了什么（只看这个文件）
git diff HEAD...<TAG> -- <conflict-file>

# 3. 查看 fork 相对上游基线改了什么（理解 fork 的意图）
git diff upstream/Alpha...HEAD -- <conflict-file>
```

### 2-B. 分析逻辑（以 `adapter/outbound/ssh.go` 为例）

该文件是最常见的冲突点，冲突来源：
- **上游** 修复 bug、添加新功能或重构
- **Fork** 在同一文件加了 SSH 系统代理扩展（`socksExt`、`useSystemSsh`、`closed` 字段，以及 `UseSshConfigAlias`/`UseSystemSocks` 选项）

分析时要识别：
- `<<<<<<< HEAD` 块中哪些是 fork 的扩展（保留）
- `>>>>>>> <TAG>` 块中哪些是上游的改动（吸收）
- 两者是否修改了同一逻辑段（需要真正理解后融合）

**Fork 扩展的识别标志**（必须保留）：
- 字段/变量名含：`socksExt`、`systemSocks`、`useSystemSsh`、`closed`
- 选项名含：`UseSshConfigAlias`、`UseSystemSocks`、`SshUser`、`SshFlags`
- 注释含：`系统`、`system ssh`、`ssh-config`

**合并原则**：
1. 上游的结构性改动（如函数签名变更、新增参数）优先吸收
2. Fork 的扩展字段/方法不丢失
3. 如果上游重构了某个函数而 fork 也修改了该函数，则在上游新结构的基础上重新植入 fork 的扩展逻辑
4. 合并结果必须可编译

### 2-C. 写入合并结果

用 Edit 工具直接写入解决冲突后的文件（去除所有 `<<<<<<<`/`=======`/`>>>>>>>` 标记）。

### 2-D. 标记已解决并提交

```bash
git add <resolved-files>
git commit -m "chore: merge upstream <TAG>

Auto-resolved: build.yml/test.yml/update_core.go (--ours).
Smart-merged: <list of AI-resolved files> — preserved fork SSH system proxy
extensions, absorbed upstream changes."
```

---

## Step 3：验证（每次合并后必须执行）

### 3-A. 编译验证

```bash
go build -tags with_gvisor -trimpath -ldflags '-w -s' ./...
```

失败时：
```bash
# 检查依赖变化
git diff <TAG> HEAD -- go.mod
go mod tidy
go build -tags with_gvisor -trimpath -ldflags '-w -s' ./...
```

### 3-B. 测试验证

```bash
# fork 核心模块的测试
go test ./adapter/outbound/... -run TestSsh -v -timeout 30s

# 更广泛的回归测试
go test ./... -timeout 120s
```

测试失败时：分析失败的测试，判断是上游破坏性变更还是合并引入的问题，针对性修复后重新运行。

---

## Step 4：推送

```bash
git log --oneline -5           # 确认提交历史
git push -u origin fa/<TAG>-fa.0
```

---

## Fork 改动范围说明

该 fork 在上游基础上只做了一件事：**SSH 系统级代理**（读 `~/.ssh/config`，调用系统 `ssh` 二进制）。

**Fork 新增文件**（上游无，不会产生冲突）：
```
adapter/outbound/ssh_system.go
adapter/outbound/ssh_system_helper.go
adapter/outbound/ssh_system_socks.go
adapter/outbound/ssh_resilience.go
```

**Fork 修改的上游共享文件**（冲突高发区）：

| 文件 | Fork 的改动 | 处理方式 |
|------|------------|---------|
| `adapter/outbound/ssh.go` | 扩展 `Ssh` struct 和 `SshOption`，加了系统代理分支 | **Claude 智能合并** |
| `component/updater/update_core.go` | 替换下载 URL 指向 fork 仓库 | `--ours`（脚本自动） |

**Fork 维护的文件**（固定取 HEAD，但需要后验证）：
```
.github/workflows/build.yml
.github/workflows/test.yml
```

> ⚠️ `test.yml` 取 HEAD 后需检查：`grep "patch/" .github/workflows/test.yml` — 确认引用的 patch 文件都存在于 `.github/patch/`。上游升级时可能删除旧 patch（如 `issue77975.patch`），fork 的 test.yml 如果还保留对应步骤就会报错。

---

## 快速参考：冲突文件决策

| 冲突文件 | 策略 |
|---------|------|
| `.github/workflows/*.yml`（CI） | `--ours`（脚本自动） |
| `component/updater/update_core.go` | `--ours`（脚本自动） |
| `adapter/outbound/ssh.go` | Claude 智能合并 |
| Fork 新增文件（`ssh_system*.go` 等） | `--ours`（上游不应有此文件，若出现则异常） |
| 其他上游文件（fork 未改过） | `--theirs`（上游新功能，fork 无修改） |
