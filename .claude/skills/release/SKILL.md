---
name: release
description: >-
  Use when 用户要发布 mihomo fork 普通新版本、创建或推送 `v*` 版本 tag、监控 build.yml 的
  Upload-Release 构建，或需要在上游已发布更新时判断是否应该先做 upstream sync。
  不用于刷新 `Prerelease-Alpha`。
---

# Release — 发布新版本

自动化 mihomo fork 的发布流程：先确认版本基线，再推 tag，最后验收 GitHub Release。

> **边界说明：** 这个 skill 只负责普通 release。
> 如果用户要的是 **预览版 / pre-release / Prerelease-Alpha**，不要新建 `v*` tag，改走项目级 `prerelease` skill。
> 普通 release = `v*` tag → `Upload-Release`。
> `Prerelease-Alpha` 会在 `Alpha` 分支非 tag push 且未命中 `paths-ignore` 时自动刷新，也可以通过 `workflow_dispatch(version=Prerelease-Alpha)` 手动定向刷新。
> 但只要目标是“发布一个新的版本号 release”，就仍然使用本 skill。

## 整体流程

```text
检查当前 fork 版本基线
  ↓
检查上游是否已有更新
  ├─ 有 → 询问：先同步上游 / 忽略继续
  └─ 无 → 继续
  ↓
建议新版本号 → 用户确认 → 推送 tag → 监控 build.yml → 验收 Release
                                           ↓ 失败
                                        诊断修复 → 新 tag → 重试（最多3次）
```

---

## 执行步骤

### 1. 获取当前版本信息

```bash
git fetch origin --tags --force
git fetch upstream --tags --force

LATEST_TAG=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-fa\.[0-9]+$' | head -1)
FORK_BASE=$(printf '%s\n' "$LATEST_TAG" | sed -E 's/^(v[0-9]+\.[0-9]+\.[0-9]+)-fa\.[0-9]+$/\1/')

echo "最新 fork tag: $LATEST_TAG"
echo "当前 fork base: $FORK_BASE"
```

### 1.5. 检查上游版本（必须在建议版本前执行）

```bash
UPSTREAM_LATEST=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)
NEWER=$(printf '%s\n%s\n' "${FORK_BASE#v}" "${UPSTREAM_LATEST#v}" | sort -V | tail -1)

if [ "$NEWER" != "${FORK_BASE#v}" ] && [ "$UPSTREAM_LATEST" != "$FORK_BASE" ]; then
  echo "上游有新版本: $UPSTREAM_LATEST (当前 fork base: $FORK_BASE)"
fi
```

**如果上游版本更新，立即停下来提醒用户：**

```text
⚠️ 检测到上游有新版本，当前 fork 尚未合并。

上游最新版本：v{UPSTREAM_LATEST}
当前 fork base：v{FORK_BASE}

建议先完成上游同步，再发布新版本。
未合并直接发布会导致：fork 版本号与实际代码基线不符，遗漏上游修复/新功能。
```

使用 AskUserQuestion 询问用户：

```text
上游 MetaCubeX/mihomo 已发布 v{UPSTREAM_LATEST}，
当前 fork base 仍为 v{FORK_BASE}，尚未合并上游更新。

请选择：
A) 暂停发布，我先处理上游同步（推荐）
B) 忽略上游更新，仍以 v{FORK_BASE}-fa.{increment} 发布当前代码
```

- 选 **A**：停止发布流程，不做任何 tag 操作。建议转去执行 `/upstream-sync` 或 `./.claude/skills/upstream-sync/upstream-sync.sh`。
- 选 **B**：继续发布，但后续确认文案里必须明确标注：
  `⚠️ 此版本基于 upstream {FORK_BASE}，上游 {UPSTREAM_LATEST} 尚未合并`。

### 2. 建议版本号

根据最新 fork tag 自动递增（increment 为4位数）：

- 最新 `v1.19.23-fa.1015` → 建议 `v1.19.23-fa.1016`
- 如果刚完成上游同步到 `v1.19.24` → 建议 `v1.19.24-fa.1001`

版本号格式：`v{upstream_version}-fa.{increment}`

### 3. 询问用户确认

#### 3-A. 上游已对齐时

使用 AskUserQuestion 工具询问：

```text
当前最新版本: v1.19.23-fa.1015
Fork base: v1.19.23（已确认与上游一致）
建议新版本: v1.19.23-fa.1016

请选择：
1. 使用建议版本 v1.19.23-fa.1016
2. 自定义版本号
```

#### 3-B. 用户选择忽略上游更新时

```text
当前最新版本: v1.19.23-fa.1015
Fork base: v1.19.23
⚠️ 上游最新版本: v1.19.24（尚未合并）
建议新版本: v1.19.23-fa.1016

请选择：
1. 使用建议版本 v1.19.23-fa.1016
2. 自定义版本号
```

### 4. 创建并推送 tag

```bash
TAG=v1.19.23-fa.1016 && git tag $TAG && git push origin $TAG
```

### 5. 监控构建（每30秒轮询，超时5分钟）

推送成功后只监控**这次 tag 对应**的 `build.yml`，不要直接取最近一个 `push` run，因为 `Alpha` 分支 push 也会触发同一个 workflow。

```bash
TAG=v1.19.23-fa.1016

# 先拿到 tag 对应提交
TAG_SHA=$(git rev-list -n 1 "$TAG")

# 只选 headBranch = tag 且 headSha = tag 对应提交 的 run
RUN_ID=$(gh run list \
  -R iuin8/mihomo \
  --workflow=build.yml \
  --limit=20 \
  --json databaseId,headBranch,headSha,event,displayTitle \
  --jq 'map(select(.event == "push" and .headBranch == '"\"$TAG\""' and .headSha == '"\"$TAG_SHA\""')) | .[0].databaseId')

# 查看当前状态
gh run view "$RUN_ID" \
  -R iuin8/mihomo \
  --json status,conclusion,jobs \
  --jq '{status,conclusion,jobs:[.jobs[]|{name,status,conclusion}]}'
```

如果 `RUN_ID` 为空，先重新执行一次 `gh run list` 检查最近 run，确认 tag push 已被 GitHub 接收。

**状态判断：**

| status                   | conclusion              | 动作             |
| ------------------------ | ----------------------- | ---------------- |
| `in_progress` / `queued` | —                       | 等待，30秒后再查 |
| `completed`              | `success`               | 进入验收步骤     |
| `completed`              | `failure` / `cancelled` | 进入诊断修复     |
| 超过5分钟仍未完成        | —                       | 视为超时并通知用户 |

监控时向用户实时报告进度，例如：

```text
[第30秒] 构建进行中 — build(darwin-arm64) 🔄  build(linux-amd64-v3) 🔄
[第2分钟] 构建进行中 — build(darwin-arm64) ✅  build(linux-amd64-v3) 🔄  Upload-Release ⏳
[第4分30秒] 仍未完成，准备按超时处理
```

**超时策略：**

5分钟还没结束就不要继续傻等，直接告诉用户：

```text
⚠️ build.yml 已监控 5 分钟仍未完成。
这次先按超时处理，请手动决定是否继续等待，或让我继续排查卡在哪个 job。
```

### 6. 验收 Release

构建成功后，验证 Release 页面内容：

```bash
gh release view <TAG> --json isDraft,assets \
  --jq '{isDraft, assetCount: (.assets | length)}'
```

**验收标准（全部满足才算通过）：**

1. `isDraft: false` — 已发布，非草稿
2. `assetCount >= 4` — 至少有 darwin-arm64、linux-amd64-v3、linux-arm64、windows-amd64 四个平台产物

验收通过后告知用户：

```text
✅ Release v1.19.23-fa.1016 发布成功
   - 平台产物: 5 个文件
   - https://github.com/iuin8/mihomo/releases/tag/v1.19.23-fa.1016
```

---

## 自动修复循环（最多3次）

构建失败或验收不通过时，进入修复循环。**每次循环版本号自动递增**。

### 失败类型诊断

```bash
gh run view <RUN_ID> --log-failed
```

**根据失败 job 定位问题：**

| 失败 job             | 可能原因                           | 修复方向 |
| -------------------- | ---------------------------------- | -------- |
| `build`（编译）      | Go 编译错误、依赖变化              | 修复代码，必要时 `go mod tidy` 后重新发布 |
| `build`（patch）     | `.github/patch/` 缺少 patch 文件   | 检查 `build.yml` / `test.yml` 的 patch 引用 |
| `Upload-Release`     | `GITHUB_TOKEN` 权限不足            | 检查 Actions → Workflow permissions |
| `Upload-Prerelease`  | 预发布上传失败                     | 检查 token 权限、workflow 配置 |

### 修复后重新发布

```bash
# 新版本 = 上一次失败版本 + 1
TAG=v1.19.23-fa.1017 && git tag $TAG && git push origin $TAG
```

**循环计数管理：**

```text
第1次尝试: v1.19.23-fa.1016 → 失败 → 修复
第2次尝试: v1.19.23-fa.1017 → 失败 → 修复
第3次尝试: v1.19.23-fa.1018 → 失败 → 停止，通知用户
```

第3次仍失败时，输出完整诊断报告并等待用户介入。

---

## 版本号规则

### Fork 版本格式

`v{upstream_version}-fa.{increment}`（increment 为4位数，从1001开始）

- `v1.19.23-fa.1001` — 该上游版本的第一个 fork 版本
- `v1.19.23-fa.1015` — 当前最新
- `v1.19.24-fa.1001` — 上游升级到 `v1.19.24` 后的第一个 fork 版本

---

## 错误处理

### 问题：版本号已存在

使用 AskUserQuestion 工具询问：

```text
版本号 v1.19.23-fa.1016 已存在。

请选择：
1. 自动递增到下一个版本号 (v1.19.23-fa.1017)
2. 自定义新的版本号
3. 强制覆盖现有版本（不推荐）
```

强制覆盖时：

```bash
git tag -d v1.19.23-fa.1016
git push origin :refs/tags/v1.19.23-fa.1016
TAG=v1.19.23-fa.1016 && git tag $TAG && git push origin $TAG
```

### 问题：构建未被触发

检查 tag 是否匹配 `build.yml` 的触发条件（`tags: ["v*"]`）：

```bash
git ls-remote origin refs/tags/v1.19.23-fa.1016
```

### 问题：上游刚发版但这次必须先发当前代码

允许继续，但必须在确认消息和最终汇报里明确说明：

```text
⚠️ 本次 release 基于 upstream v1.19.23。
⚠️ 上游 v1.19.24 尚未合并。
```

---

## 注意事项

1. **优先确保当前分支就是要发布的代码**
2. **如果上游有新版，默认先做 upstream sync，再回来发布**
3. **5分钟超时只是停止自动盯盘，不代表构建一定失败**
4. **如果 CI 改过 patch 逻辑，发布前要检查 `.github/patch/` 与 workflow 引用是否一致**

---

## 相关文件

- `.github/workflows/build.yml` — 主构建和发布 workflow
- `.github/workflows/test.yml` — 测试 workflow
- `.github/patch/` — CI 使用的 patch 文件
- `.claude/skills/upstream-sync/SKILL.md` — 上游同步流程

---

## 示例对话

用户："帮我发布一个新版本"

助手执行：

1. 获取最新版本：`v1.19.23-fa.1015`
2. 检查上游：若已到 `v1.19.24`，先询问要不要暂停去做 upstream sync
3. 用户确认版本后，执行 `TAG=v1.19.23-fa.1016 && git tag $TAG && git push origin $TAG`
4. 每30秒监控一次 `build.yml`，最多 5 分钟
5. 构建成功后验收 Release 产物
6. ✅ Release 正常