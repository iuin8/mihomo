---
name: prerelease
description: >-
  Use when 发布或刷新 iuin8/mihomo 的 Prerelease-Alpha、Alpha 预览版、pre-release，
  或需要用 build.yml workflow_dispatch 刷新预发布资产。不用于新建 v* 普通 release。
---

> **发布线**：mihomo fork 的默认分支是 **`fa/trunk`**（fork 的发布主线）✓ —— 发布/预发布类触发一律
> `--ref fa/trunk` ✓；feature 线先合进 `fa/trunk` ✓ 再打 tag ✓。

# Prerelease — mihomo

## 边界

- **不要为 Prerelease-Alpha 新建 `v*` tag**；`v*` tag 会走普通 release。
- 正确入口：`build.yml` 的 `workflow_dispatch`，参数 `version=Prerelease-Alpha`。
- `--ref` 必须精确指向包含目标提交的远端 ref tip；目标提交不是任何远端 ref 的 tip 时，先推临时分支。

## 流程

```mermaid
flowchart TD
  A[选择目标提交] --> B{是远端 ref tip?}
  B -->|否| C[推临时分支指向该提交]
  B -->|是| D[workflow_dispatch build.yml]
  C --> D
  D --> E[version=Prerelease-Alpha + --ref target]
  E --> F[校验 run branch/SHA]
  F --> G[后台 watch build + Upload-Prerelease]
  G --> H{Upload-Prerelease success?}
  H -->|否| I[读取失败日志并最小修复]
  I --> G
  H -->|是| J[验收 prerelease assets]
```

## 必跑步骤

### 1. 确认目标 ref

```bash
git branch --show-current
git rev-parse HEAD
git branch -r --contains <COMMIT_SHA>
```

- 目标提交必须是 `--ref` 指向的远端 ref 当前 tip。
- 若不是，先推临时分支：

```bash
git branch prerelease/<topic> <COMMIT_SHA>
git push -u origin prerelease/<topic>
```

### 2. 触发 workflow

```bash
gh workflow run build.yml \
  -R iuin8/mihomo \
  --ref <branch-or-temp-ref> \
  -f version=Prerelease-Alpha
```

### 3. 校验 run 对应正确代码

```bash
gh run list -R iuin8/mihomo --workflow build.yml --limit 5 \
  --json databaseId,event,headBranch,headSha,status,conclusion,displayTitle

gh run view <RUN_ID> -R iuin8/mihomo \
  --json status,conclusion,jobs,headBranch,headSha,event,url
```

必须满足：

- `event == workflow_dispatch`
- `headBranch == <目标 ref>`
- `headSha == <目标提交>`

### 4. 等待和监控

```bash
gh run watch <RUN_ID> -R iuin8/mihomo --interval 60 --exit-status
```

重点 job：

- `build (...)` 全部成功。
- `Upload-Prerelease == success`。
- `Upload-Release == skipped`；若它成功，说明走错普通 release 流程。

### 5. 验收 Prerelease-Alpha

```bash
gh release view Prerelease-Alpha -R iuin8/mihomo \
  --json isDraft,isPrerelease,tagName,name,assets,url,targetCommitish
```

验收标准：

| 字段 | 期望 |
| --- | --- |
| `isDraft` | `false` |
| `isPrerelease` | `true` |
| `tagName` | `Prerelease-Alpha` |
| assets | 至少包含 darwin-arm64、linux-amd64-v3、linux-arm64、windows-amd64、`checksums.txt`、`version.txt` |

`targetCommitish` 可能显示默认分支；以本次 workflow run 的 `headBranch` / `headSha` 为主验收依据。

## 本技能实测坑位（2026-10-03 实测）

### `version.txt` 的内容就是 tag 名 ✗ → 下游更新器**分辨不出新构建**

`Prerelease-Alpha` 是**滚动 tag** ✓ —— 它的 `version.txt` 恒为 `Prerelease-Alpha` ✓，
而客户端更新器（clash-verge-rev fork）比较的是"远端 `version.txt` vs 已安装内核版本串" ✗ →
**两者永远相同** ✓ → 点「升级内核」会**静默不下载** ✗✓（实测：资产已更新到 12:48 构建 ✓，本机仍跑 06:35 ✗，`host-id` 指纹为 0 ✓）。

**所以刷新预发布之后** ✓：

- 客户端**不会**自动拿到新内核 ✗ → 要么**同时发一个 App 版本** ✓（推荐 ✓），要么让用户**手动替换** ✓；
- 手动替换的关键点 ✗✓：**服务模式实际运行的是 `clash-verge-service/cores/` 下那份** ✓（root 所有 ✓），
  **不是** app 包里的副本 ✗ —— 两处都要换 ✓：

```bash
SVC="/Library/Application Support/clash-verge-service/cores/verge-mihomo-alpha"
APP="/Applications/Clash Verge.app/Contents/MacOS/verge-mihomo-alpha"
sudo cp core "$SVC" && sudo chmod +x "$SVC"
sudo cp core "$APP" && sudo chmod +x "$APP"
sudo strings "$SVC" | grep -c <本次新增的字面量>      # 确认运行物真换了 ✓
```

### 验收要**打开产物**并用"本次新增的字面量"当指纹 ✗✓

只验 `isDraft/isPrerelease/资产数` 不够 ✓（名字会骗人 ✗）。做法 ✓：下载本渠道产物 → `-v` 看构建时间 ✓ →
再 grep **本次改动新增的字符串** ✓（例如本次的 `host-id` ✓）—— 这样证明的是"**发布物里确实有这次改动**" ✓。

**不要**断言"另一通道的串不存在" ✗（同一个二进制里 alpha/stable 命名可能都合法 ✓，见 workspace 验证纪律第 8 条 ✓）。

## 失败诊断不变量

- 失败必须引用具体 job、step、原始错误片段；不要只按 job 名猜。
- run 未完成时，`gh run view --log-failed` 可能不可用；先定位失败 job/step。
- run 完成后再用：

```bash
gh run view <RUN_ID> -R iuin8/mihomo --log-failed
gh run view <RUN_ID> -R iuin8/mihomo --job <JOB_ID> --log
gh api repos/iuin8/mihomo/actions/jobs/<JOB_ID>/logs > job.log
```

优先看：`Delete current release assets`、`Tag Repo`、`Upload Prerelease`、各平台 build job。

## Red Flags

| 错误动作 | 修正 |
| --- | --- |
| 推 `v*` tag | 这是普通 release；改用 `workflow_dispatch -f version=Prerelease-Alpha` |
| 不带 `--ref` | 会跑默认分支；重新用目标分支触发 |
| 手动 `git tag Prerelease-Alpha` | 不要手动 tag；让 `Upload-Prerelease` 更新 release/tag |
| 目标提交不是远端 ref tip | 先推临时分支，再用该分支 `--ref` |

## 汇报模板

```text
✅ Prerelease-Alpha 已刷新
- workflow: build.yml (workflow_dispatch)
- ref/headSha: <branch> / <sha>
- Upload-Prerelease: success
- assets: <count>
- release: https://github.com/iuin8/mihomo/releases/tag/Prerelease-Alpha
```
