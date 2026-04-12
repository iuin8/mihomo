---
name: release
description: >-
  发布 mihomo fork 新版本 - 自动获取当前版本，建议新版本号，
  询问用户确认，创建 tag 并推送，监控 GitHub Actions 构建（每分钟轮询，超时30分钟），
  构建失败时自动诊断修复并重试，最多循环3次直到 Release 正常。
  TRIGGER when: 用户说"发布"、"release"、"新版本"、"打tag"或请求发布操作。
  DO NOT TRIGGER when: 用户只是询问版本信息或查看构建状态。
---

# Release — 发布新版本

自动化 mihomo fork 的完整发布流程，从版本号确认到 GitHub Release 验收。

## 整体流程

```
建议版本号 → 用户确认 → 推送 tag → [监控循环] → 验收 Release
                                        ↓ 失败
                                     诊断修复 → 新 tag → 重试（最多3次）
```

---

## 执行步骤

### 1. 获取当前版本信息

```bash
git fetch --tags
LATEST_TAG=$(git tag --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-fa\.[0-9]+$' | head -1)
echo "最新 tag: $LATEST_TAG"
```

### 2. 建议版本号

根据最新 tag 自动递增（increment 为4位数）：

- 最新 `v1.19.23-fa.1015` → 建议 `v1.19.23-fa.1016`
- 上游升级到 v1.19.24 → 建议 `v1.19.24-fa.1001`

版本号格式：`v{upstream_version}-fa.{increment}`

### 3. 询问用户确认

使用 AskUserQuestion 工具询问：

```
当前最新版本: v1.19.23-fa.1015
建议新版本: v1.19.23-fa.1016

请选择：
1. 使用建议版本 v1.19.23-fa.1016
2. 自定义版本号
```

### 4. 创建并推送 tag

```bash
TAG=v1.19.23-fa.1016
git tag $TAG
git push origin $TAG
```

### 5. 监控构建（每分钟轮询，超时30分钟）

推送成功后立即进入监控循环，使用 ScheduleWakeup 每 60 秒唤醒一次：

```bash
# 获取本次 tag 触发的 workflow run
gh run list \
  --workflow=build.yml \
  --limit=5 \
  --json databaseId,status,conclusion,headBranch,event \
  --jq '.[] | select(.event == "push")'

# 查看指定 run 状态
gh run view <RUN_ID> --json status,conclusion,jobs \
  --jq '{status,conclusion,jobs:[.jobs[]|{name,status,conclusion}]}'
```

**状态判断：**

| status                   | conclusion              | 动作             |
| ------------------------ | ----------------------- | ---------------- |
| `in_progress` / `queued` | —                       | 等待，60秒后再查 |
| `completed`              | `success`               | 进入验收步骤     |
| `completed`              | `failure` / `cancelled` | 进入诊断修复     |
| 超过30分钟仍未完成       | —                       | 超时，通知用户   |

监控时向用户实时报告进度，例如：

```
[第3分钟] 构建进行中 — build(darwin-arm64) 🔄  build(linux-amd64-v3) 🔄  ...
[第15分钟] 构建进行中 — build ✅  Upload-Release 🔄
[第22分钟] 构建完成 ✅
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

```
✅ Release v1.19.23-fa.1016 发布成功
   - 平台产物: 5 个文件
   - https://github.com/iuin8/mihomo/releases/tag/v1.19.23-fa.1016
```

---

## 自动修复循环（最多3次）

构建失败或验收不通过时，进入修复循环。**每次循环版本号自动递增**（如 1016 失败 → 修复后发 1017）。

### 失败类型诊断

```bash
# 查看失败 job 的日志
gh run view <RUN_ID> --log-failed
```

**根据失败 job 定位问题：**

| 失败 job          | 可能原因                          | 修复方向                            |
| ----------------- | --------------------------------- | ----------------------------------- |
| `build`（编译）   | Go 编译错误、依赖问题             | 修复代码，`go mod tidy` 后重新发布  |
| `build`（patch）  | `.github/patch/` 中缺少 patch 文件 | 检查 test.yml/build.yml 引用的 patch |
| `Upload-Release`  | GITHUB_TOKEN 权限不足             | 检查 Actions → Workflow permissions  |
| `Upload-Prerelease` | 预发布上传失败                  | 检查 token 权限或网络问题           |

### 修复后重新发布

修复完成后，递增版本号重新走完整流程：

```bash
# 新版本 = 上一次失败版本 + 1
TAG=v1.19.23-fa.1017
git tag $TAG
git push origin $TAG
```

**循环计数管理：**

```
第1次尝试: v1.19.23-fa.1016 → 失败 → 修复
第2次尝试: v1.19.23-fa.1017 → 失败 → 修复
第3次尝试: v1.19.23-fa.1018 → 失败 → 停止，通知用户
```

第3次仍失败时，输出完整诊断报告并等待用户介入：

```
❌ 已尝试3次，均未成功。

失败摘要：
- v1.19.23-fa.1016: build job 失败 — patch 文件缺失
- v1.19.23-fa.1017: 同上
- v1.19.23-fa.1018: 同上

建议检查：
1. .github/patch/ 目录中是否有 build.yml/test.yml 引用的所有 patch 文件
2. repo Settings → Actions → Workflow permissions 是否为 Read and write
```

---

## 版本号规则

### Fork 版本格式

`v{upstream_version}-fa.{increment}`（increment 为4位数，从1001开始）

- `v1.19.23-fa.1001` — 该上游版本的第一个 fork 版本
- `v1.19.23-fa.1015` — 当前最新
- `v1.19.24-fa.1001` — 上游升级到 v1.19.24 后的第一个版本

---

## 错误处理

### 问题：版本号已存在

使用 AskUserQuestion 工具询问：

```
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
TAG=v1.19.23-fa.1016
git tag $TAG
git push origin $TAG
```

### 问题：构建未被触发

检查 tag 是否匹配 `build.yml` 的触发条件（`tags: ["v*"]`）：

```bash
# 确认 tag 已推送到远程
git ls-remote origin refs/tags/v1.19.23-fa.1016

# 手动触发（如 workflow_dispatch 支持）
gh workflow run build.yml
```

---

## 相关文件

- `.github/workflows/build.yml` — 主构建和发布 workflow（tag 触发 Upload-Release）
- `.github/workflows/test.yml` — 测试 workflow
- `.github/patch/` — CI 使用的 patch 文件（需与 workflow 引用保持同步）

---

## 示例对话

用户："帮我发布一个新版本"

助手执行：

1. 获取最新版本：`v1.19.23-fa.1015`，建议 `v1.19.23-fa.1016`
2. 询问用户确认
3. 执行 `TAG=v1.19.23-fa.1016 && git tag $TAG && git push origin $TAG`
4. 推送成功，开始每分钟监控 GitHub Actions `build.yml` workflow
5. 约20分钟后构建完成，验收 Release 内容
6. ✅ Release 正常：有产物（≥4个平台）、非草稿
