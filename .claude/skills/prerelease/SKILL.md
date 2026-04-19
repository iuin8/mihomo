---
name: prerelease
description: "发布或刷新 mihomo fork 的 Prerelease-Alpha 预发布。触发词：预览版、pre-release、Prerelease-Alpha、刷新预发布。"
---

# Prerelease — 发布预览版

自动化 mihomo fork 的 **Prerelease-Alpha** 发布流程。

这个流程和普通 release 不同：
- **普通 release** = 推 `v*` tag，走 `Upload-Release`
- **Prerelease-Alpha 自动刷新** = `Alpha` 分支非 tag push，且未命中 `paths-ignore`，走 `Upload-Prerelease`
- **pre-release 手动定向刷新** = 手动触发 `build.yml` 的 `workflow_dispatch`，传 `version=Prerelease-Alpha`

> **关键规则：不要为 pre-release 新建 `v*` tag。**
> 新建 `v*` tag 只会发布普通 release，不会刷新 `Prerelease-Alpha`。
> 如果目标是把某个指定提交刷新到 `Prerelease-Alpha`，该提交必须就是 `--ref` 指向 ref 的当前 tip。
> 如果目标提交不是任何远端 ref 的 tip，先创建临时分支或临时 tag 指到该提交，再用那个 ref 做 `workflow_dispatch`。

---

## 整体流程

```text
确认目标提交/分支
  ↓
用目标分支触发 build.yml workflow_dispatch
  ↓
传入 version=Prerelease-Alpha
  ↓
监控 Upload-Prerelease
  ↓
验收 Prerelease-Alpha 页面和产物
```

---

## 执行步骤

### 1. 确认目标分支和提交

pre-release 必须基于**包含目标修复的分支**触发，不能默认使用仓库默认分支。

```bash
git branch --show-current
git rev-parse HEAD
git branch -r --contains <COMMIT_SHA>
```

验收条件：
- 目标提交就是某个远端 ref 当前指向的 tip
- 用于触发 workflow 的 `--ref` 必须精确指向该 ref

如果目标提交还没推上去，或虽然已被分支包含但不是该分支 HEAD，先处理成一个可精确引用的远端 ref。

常见做法：

```bash
# 情况 1：目标提交就是当前分支 HEAD
git push -u origin <branch>

# 情况 2：目标提交不是任何远端 ref 的 tip，先建临时分支
git branch prerelease/<topic> <COMMIT_SHA>
git push -u origin prerelease/<topic>
```

然后用这个 ref 去触发 workflow。

```bash
gh workflow run build.yml \
  -R iuin8/mihomo \
  --ref prerelease/<topic> \
  -f version=Prerelease-Alpha
```

---

### 2. 触发 pre-release workflow

**正确命令：**

```bash
gh workflow run build.yml \
  -R iuin8/mihomo \
  --ref <branch> \
  -f version=Prerelease-Alpha
```

示例：

```bash
gh workflow run build.yml \
  -R iuin8/mihomo \
  --ref fa/v1.19.23-fa.0 \
  -f version=Prerelease-Alpha
```

**不要这样做：**

```bash
# 错误：这会创建普通 release，不会更新 pre-release
git tag v1.19.23-fa.1019 && git push origin v1.19.23-fa.1019
```

---

### 3. 校验 run 是否跑在正确分支上

触发后立刻检查 run：

```bash
gh run list \
  -R iuin8/mihomo \
  --workflow build.yml \
  --limit 5 \
  --json databaseId,event,headBranch,headSha,status,conclusion,displayTitle
```

再查看本次 run：

```bash
gh run view <RUN_ID> \
  -R iuin8/mihomo \
  --json status,conclusion,jobs,headBranch,headSha,event,url
```

**必须确认：**
1. `event = workflow_dispatch`
2. `headBranch = <目标分支>`
3. `headSha = <目标提交>`

如果第一次触发没带 `--ref`，常见现象是：
- workflow 跑在仓库默认分支上
- `headBranch` / `headSha` 不是你刚修的那版
- `Prerelease-Alpha` 被默认分支代码刷新，内容不对

这时不要重试 tag，直接按正确分支重新触发一次。

只有在仓库默认分支上的 workflow 文件本身和目标分支差异较大时，才可能出现 job 行为异常；主症状通常不是 `skipped`，而是**构建了错误的代码**。

---

### 4. 监控构建

```bash
gh run watch <RUN_ID> -R iuin8/mihomo --exit-status
```

或轮询：

```bash
gh run view <RUN_ID> \
  -R iuin8/mihomo \
  --json status,conclusion,jobs \
  --jq '{status,conclusion,jobs:[.jobs[]|{name,status,conclusion}]}'
```

**重点看这两个 job：**
- `build (...)`，各平台构建
- `Upload-Prerelease`，上传预发布

**预期行为：**
- `Upload-Prerelease = success`
- `Upload-Release = skipped`

如果 `Upload-Release` 成功，说明走错流程了。

---

### 5. 验收 Prerelease-Alpha

```bash
gh release view Prerelease-Alpha \
  -R iuin8/mihomo \
  --json isDraft,isPrerelease,tagName,name,assets,url,targetCommitish
```

**验收标准：**
1. `isDraft: false`
2. `isPrerelease: true`
3. `tagName: Prerelease-Alpha`
4. `assets >= 4`
5. 至少包含以下核心平台产物：
   - `mihomo-darwin-arm64-Prerelease-Alpha.gz`
   - `mihomo-linux-amd64-v3-Prerelease-Alpha.gz`
   - `mihomo-linux-arm64-Prerelease-Alpha.gz`
   - `mihomo-windows-amd64-Prerelease-Alpha.zip`

建议再确认：
- `checksums.txt` 存在
- `version.txt` 存在

**注意：**
`targetCommitish` 可能仍显示默认分支，这不是本流程的主验收依据。
真正要核对的是：
- 本次 `workflow_dispatch` 的 `headBranch`
- 本次 `workflow_dispatch` 的 `headSha`
- `Upload-Prerelease` 是否成功
- release 资产是否已刷新

---

## 常见误区

### 误区 1：把 pre-release 当普通 release 发

现象：
- 推了 `v*` tag
- `Upload-Release` 成功
- 生成了新的版本 release
- `Prerelease-Alpha` 没更新

修正：
- 不删 tag 也可以
- 直接按本 skill 重新触发 `build.yml workflow_dispatch`
- `version=Prerelease-Alpha`
- `--ref` 指向正确分支

### 误区 2：没带 `--ref`

现象：
- run 跑在默认分支
- `headBranch` / `headSha` 不对
- `Prerelease-Alpha` 被错误代码刷新

修正：

```bash
gh workflow run build.yml \
  -R iuin8/mihomo \
  --ref <正确分支> \
  -f version=Prerelease-Alpha
```

### 误区 3：拿 `Prerelease-Alpha` 当版本号 tag

不要执行：

```bash
git tag Prerelease-Alpha
git push origin Prerelease-Alpha
```

正确流程由 workflow 里的 `Upload-Prerelease` 自动更新 `Prerelease-Alpha` tag/release。

---

## 失败诊断

### `Upload-Prerelease` 失败

```bash
gh run view <RUN_ID> -R iuin8/mihomo --log-failed
```

优先检查：
- `Delete current release assets`
- `Tag Repo`
- `Upload Prerelease`

常见方向：
- `GITHUB_TOKEN` 权限问题
- release/tag 已存在但状态异常
- workflow 引用的 action 行为变化

### build job 失败

优先检查：
- 编译错误
- patch 文件引用
- 平台矩阵新增后未适配

```bash
gh run view <RUN_ID> -R iuin8/mihomo --log-failed
```

---

## 成功回报模板

```text
✅ Prerelease-Alpha 已刷新成功
- workflow: build.yml (workflow_dispatch)
- branch: <branch>
- commit: <sha>
- Upload-Prerelease: success
- release: https://github.com/iuin8/mihomo/releases/tag/Prerelease-Alpha
```
