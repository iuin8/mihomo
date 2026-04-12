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
#   2  存在需要 AI 智能合并的冲突（输出冲突文件列表到 stdout，供 Claude 读取）
#   3  构建失败（已合并但编译不通过）
#
# 依赖: git, go

set -euo pipefail

REPO_ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
cd "$REPO_ROOT"

CHECK_ONLY=false
TARGET_TAG=""
for arg in "$@"; do
  case "$arg" in
    --check-only) CHECK_ONLY=true ;;
    v*) TARGET_TAG="$arg" ;;
  esac
done

# ── 0. 确认目标 tag ──────────────────────────────────────────────────────────
echo "[0/5] 拉取上游 tags..."
git fetch upstream --tags --force --quiet

if [[ -z "$TARGET_TAG" ]]; then
  TARGET_TAG="$(git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)"
fi
[[ -z "$TARGET_TAG" ]] && { echo "错误: 找不到 v*.*.* tag" >&2; exit 1; }

BRANCH="fa/${TARGET_TAG}-fa.0"
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

OVERLAP="$(comm -12 <(echo "$UPSTREAM_CHANGED" | sort) <(echo "$FORK_MODIFIED_UPSTREAM" | sort) || true)"
if [[ -n "$OVERLAP" ]]; then
  echo "    ⚠️  双方均有修改的文件（合并时需智能分析）:"
  echo "$OVERLAP" | sed 's/^/      /'
else
  echo "    无交叉修改文件"
fi

if $CHECK_ONLY; then
  echo ""; echo "预检完成（--check-only）"; exit 0
fi

# ── 2. 创建分支 ──────────────────────────────────────────────────────────────
echo ""
echo "[2/5] 分支 $BRANCH..."
if git show-ref --verify --quiet "refs/heads/$BRANCH"; then
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
  echo "    冲突文件: $(echo "$CONFLICT_FILES" | wc -l | tr -d ' ') 个"
fi

# ── 4. 自动解决已知固定策略冲突 ─────────────────────────────────────────────
echo ""
echo "[4/5] 自动解决固定策略冲突..."

auto_resolve_ours() {
  local f="$1"; local reason="$2"
  if echo "$CONFLICT_FILES" | grep -qxF "$f"; then
    echo "    $f → --ours（$reason）"
    git checkout --ours "$f" && git add "$f"
  fi
}
auto_resolve_rm() {
  local f="$1"
  if echo "$CONFLICT_FILES" | grep -qxF "$f"; then
    echo "    $f → git rm（fork 已删除）"
    git rm "$f" --quiet
  fi
}

auto_resolve_ours ".github/workflows/build.yml"       "fork 精简 CI"
auto_resolve_ours ".github/workflows/test.yml"        "fork 精简 CI"
auto_resolve_ours "component/updater/update_core.go"  "仅含仓库 URL 替换"
auto_resolve_rm   ".github/workflows/trigger-cmfa-update.yml"

# ── 输出剩余冲突（供 AI 智能合并）──────────────────────────────────────────
REMAINING="$(git diff --name-only --diff-filter=U 2>/dev/null || true)"
if [[ -n "$REMAINING" ]]; then
  echo ""
  echo "SMART_MERGE_REQUIRED"         # 机器可读标记
  echo "---"
  echo "TARGET_TAG=$TARGET_TAG"
  echo "CONFLICT_FILES:"
  echo "$REMAINING"
  echo "---"
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
