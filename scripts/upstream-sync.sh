#!/usr/bin/env bash
# upstream-sync.sh — 将上游最新 release tag 合并到新的 fa/<tag>-fa.0 分支
#
# 用法:
#   ./scripts/upstream-sync.sh              # 自动取最新上游 release tag
#   ./scripts/upstream-sync.sh v1.19.24     # 指定目标 tag
#   ./scripts/upstream-sync.sh --check-only # 仅预检，不合并
#
# 退出码:
#   0  成功（构建通过，无未解决冲突）
#   1  参数或环境错误
#   2  存在需要 AI 智能合并的冲突（输出冲突文件列表 + AI_HINTS 到 stdout）
#   3  构建失败（已合并但编译不通过）
#
# 与 .claude/skills/upstream-sync/SKILL.md 是协作关系：
#   - 脚本：机械可重复操作（拉 tag、建分支、git merge、固定策略 --ours/rm、构建）
#   - skill+AI：需要语义理解的操作（delta 复审、智能三向合并、连带影响排查、汇报）
#
# 依赖: git, go

set -euo pipefail

# ── 失败时打印行号，便于 AI 立即定位中断点 ──────────────────────────────────
trap 'rc=$?; echo "[SCRIPT_FAILED] line $LINENO exit $rc" >&2' ERR

# ── 依赖检查 ─────────────────────────────────────────────────────────────────
for dep in git go; do
  command -v "$dep" >/dev/null 2>&1 || { echo "错误: 找不到依赖 $dep" >&2; exit 1; }
done

# ── 锚定仓库根（用 cd+pwd 避免 dirname 相对路径分歧）────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# ── 参数解析 ─────────────────────────────────────────────────────────────────
CHECK_ONLY=false
TARGET_TAG=""
for arg in "$@"; do
  case "$arg" in
    --check-only) CHECK_ONLY=true ;;
    v*)           TARGET_TAG="$arg" ;;
    *)            echo "未知参数: $arg" >&2; exit 1 ;;
  esac
done

# ── 0. 确认目标 tag + 推导 PREV_TAG ──────────────────────────────────────────
echo "[0/5] 拉取上游 tags..."
git fetch upstream --tags --force --quiet

if [[ -z "$TARGET_TAG" ]]; then
  TARGET_TAG="$(git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)"
fi
[[ -z "$TARGET_TAG" ]] && { echo "错误: 找不到 v*.*.* tag" >&2; exit 1; }

BRANCH="fa/${TARGET_TAG}-fa.0"

# 上次同步的基线：最近一次 "merge upstream <tag>" commit 中的 tag。
# 用作 skill Step 2-A 的 delta-review 基线，让 AI 直接用 PREV_TAG..TARGET_TAG。
PREV_TAG="$(git log --merges --first-parent --grep='merge upstream' -1 --format=%s 2>/dev/null \
            | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"

echo "    上次基线 : ${PREV_TAG:-<none>}"
echo "    目标 tag : $TARGET_TAG"
echo "    目标分支 : $BRANCH"

# ── 1. 预检：上游 ∩ fork 改动文件 ───────────────────────────────────────────
echo ""
echo "[1/5] 预检交叉修改文件..."

UPSTREAM_CHANGED="$(git diff --name-only HEAD "$TARGET_TAG" 2>/dev/null || true)"
# git show 对不存在路径返回 128；用 subshell + || true 隔离，避免 pipefail 中断
FORK_MODIFIED_UPSTREAM="$(
  set +e
  git log upstream/Alpha..HEAD --name-only --pretty=format: \
    | sort -u \
    | while read -r f; do
        [[ -z "$f" ]] && continue
        git show "upstream/Alpha:$f" &>/dev/null && echo "$f"
      done
  exit 0
)"

# 过滤空行后再 comm，避免空字符串被当作匹配项
OVERLAP="$(comm -12 \
  <(echo "$UPSTREAM_CHANGED"        | grep -v '^$' | sort) \
  <(echo "$FORK_MODIFIED_UPSTREAM"  | grep -v '^$' | sort) || true)"

if [[ -n "$OVERLAP" ]]; then
  echo "    ⚠️  双方均有修改的文件（合并时需智能分析 / 编译期审查）:"
  echo "$OVERLAP" | sed 's/^/      /'
else
  echo "    无交叉修改文件"
fi

if $CHECK_ONLY; then
  echo ""; echo "预检完成（--check-only）"; exit 0
fi

# ── 2. 创建分支（先排查未完成的 merge）──────────────────────────────────────
echo ""
echo "[2/5] 分支 $BRANCH..."

# 上次脚本中途失败可能留下 MERGE_HEAD；继续 merge 会让 auto_resolve_ours 在
# 已解决文件上二次应用，造成静默回退。这里硬退出让 AI / 用户判断如何收尾。
if [[ -e "$REPO_ROOT/.git/MERGE_HEAD" ]]; then
  echo "错误: 检测到未完成的 merge（.git/MERGE_HEAD 存在）" >&2
  echo "      可能是上次脚本中途失败遗留。请先 git merge --abort 或人工解决" >&2
  echo "      冲突后再重跑（脚本不自动 abort，避免误删已完成的 AI 智能合并）。" >&2
  exit 1
fi

if git show-ref --verify --quiet "refs/heads/$BRANCH"; then
  echo "    分支已存在，继续在其上工作"
  git checkout "$BRANCH"
else
  git checkout -b "$BRANCH"
fi

# ── 3. 合并 ──────────────────────────────────────────────────────────────────
echo ""
echo "[3/5] 合并 $TARGET_TAG..."
MERGE_OK=true
git merge "$TARGET_TAG" --no-edit 2>&1 || MERGE_OK=false

CONFLICT_FILES=""
if ! $MERGE_OK; then
  CONFLICT_FILES="$(git diff --name-only --diff-filter=U)"
  CONFLICT_COUNT="$(echo "$CONFLICT_FILES" | grep -cv '^$' || true)"
  echo "    冲突文件: ${CONFLICT_COUNT} 个"
fi

# ── 4. 自动解决已知固定策略冲突 ─────────────────────────────────────────────
echo ""
echo "[4/5] 自动解决固定策略冲突..."

# 记录脚本实际应用的策略，作为 AI 汇报模板（skill Step 5-C）的输入
SCRIPT_AUTO_RESOLVED=()
SCRIPT_RESURRECTED=()

auto_resolve_ours() {
  local f="$1"; local reason="$2"
  if echo "$CONFLICT_FILES" | grep -qxF "$f"; then
    echo "    $f → --ours（${reason}）"
    git checkout --ours "$f" && git add "$f"
    SCRIPT_AUTO_RESOLVED+=("$f|ours|${reason}")
  fi
}

auto_resolve_rm() {
  local f="$1"
  if echo "$CONFLICT_FILES" | grep -qxF "$f"; then
    echo "    $f → git rm（fork 已删除）"
    git rm "$f" --quiet
    SCRIPT_AUTO_RESOLVED+=("$f|rm|fork 已删除")
  elif [[ -f "$f" ]]; then
    # fork HEAD 不再有此文件，但上游 merge 之后又出现 —— 说明上游重新引入了。
    # 不当作冲突处理，但标记给 AI 复审：要么再删一次，要么决定接受。
    echo "    ⚠️  $f 已被 fork 删除，但上游又引入 → 留作业 AI 决定"
    SCRIPT_RESURRECTED+=("$f")
  fi
}

auto_resolve_ours ".github/workflows/build.yml"       "fork 精简 CI"
auto_resolve_ours ".github/workflows/test.yml"        "fork 精简 CI"
auto_resolve_ours "component/updater/update_core.go"  "仅含仓库 URL 替换"
auto_resolve_rm   ".github/workflows/trigger-cmfa-update.yml"

# test.yml patch 文件引用检查：用 POSIX 字符类避免误匹配 GitHub Actions
# matrix 插值（如 go${{matrix.go-version}}.patch）。grep -P 不可移植，且原
# \S+ 会贪婪吞掉 ${{...}}。
if [[ -f .github/workflows/test.yml ]]; then
  PATCH_WARNING=false
  while IFS= read -r patch_ref; do
    [[ -z "$patch_ref" ]] && continue
    if [[ ! -f "$patch_ref" ]]; then
      echo "    ⚠️  test.yml 引用了不存在的 patch: $patch_ref"
      PATCH_WARNING=true
    fi
  done < <(grep -oE '\.github/patch/[A-Za-z0-9_/.-]+\.patch' .github/workflows/test.yml 2>/dev/null || true)
  if $PATCH_WARNING; then
    echo "    → 上游已删除该 patch 文件，需从 test.yml 中移除对应步骤后再提交"
  fi
fi

# ── 机器可读 hint 给 AI（skill Step 2 / 5-C 消费）───────────────────────────
emit_ai_hints() {
  echo ""
  echo "--- AI_HINTS ---"
  echo "PREV_TAG=${PREV_TAG:-}"
  echo "TARGET_TAG=$TARGET_TAG"
  echo "BRANCH=$BRANCH"

  # 4-A 输入：脚本自动 --ours / rm 的文件 → AI 必须按 skill Step 2-B 复审
  if [[ ${#SCRIPT_AUTO_RESOLVED[@]} -gt 0 ]]; then
    echo "SCRIPT_AUTO_RESOLVED:"
    printf '  %s\n' "${SCRIPT_AUTO_RESOLVED[@]}"
  else
    echo "SCRIPT_AUTO_RESOLVED: (none)"
  fi

  # 2-D 输入：git 自动合并成功但双方都改过的文件 → AI 必须做连带影响复审
  echo "GIT_OVERLAP_FILES:"
  if [[ -n "$OVERLAP" ]]; then
    echo "$OVERLAP" | sed 's/^/  /'
  else
    echo "  (none)"
  fi

  # 异常信号：fork 已删但上游又引入的文件
  if [[ ${#SCRIPT_RESURRECTED[@]} -gt 0 ]]; then
    echo "RESURRECTED_BY_UPSTREAM:"
    printf '  %s\n' "${SCRIPT_RESURRECTED[@]}"
  fi
  echo "--- END AI_HINTS ---"
}

# ── 输出剩余冲突（供 AI 智能合并）──────────────────────────────────────────
REMAINING="$(git diff --name-only --diff-filter=U 2>/dev/null || true)"
if [[ -n "$REMAINING" ]]; then
  echo ""
  echo "SMART_MERGE_REQUIRED"
  echo "CONFLICT_FILES:"
  echo "$REMAINING" | sed 's/^/  /'
  emit_ai_hints
  echo ""
  echo "以下文件需要 AI 智能合并（已自动策略无法处理）:"
  echo "$REMAINING" | sed 's/^/  /'
  exit 2
fi

# 无残余冲突才提交
if ! $MERGE_OK; then
  git commit -m "chore: merge upstream $TARGET_TAG

Auto-resolved: build.yml/test.yml/update_core.go (--ours), removed trigger-cmfa-update.yml."
fi

emit_ai_hints

# ── 5. 验证构建 ──────────────────────────────────────────────────────────────
echo ""
echo "[5/5] 验证构建..."
if go build -tags with_gvisor -trimpath -ldflags '-w -s' ./... 2>&1; then
  echo "    构建成功 ✓"
else
  echo "构建失败 — 提示: git diff $TARGET_TAG HEAD -- go.mod / go mod tidy" >&2
  exit 3
fi

echo ""
echo "完成 ✓  分支: $BRANCH  HEAD: $(git rev-parse --short HEAD)"
echo "下一步: git push -u origin $BRANCH"
