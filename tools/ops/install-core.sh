#!/usr/bin/env bash
# install-core.sh —— 把本 fork 编译的内核装进 Clash Verge（服务侧/App 侧），并自证。
#
#   install-core.sh <新内核> [--service|--app] [--name NAME] [--apply]
#
# 默认 --service（推荐）：只写 /Library/Application Support/clash-verge-service/cores/<name>，
# 不动 App bundle（改 bundle 内可执行文件会让整包签名失效 → Gatekeeper SIGKILL）。
# --app 会额外覆盖 bundle 内那份，并明确提示签名风险。
# 默认 dry-run；加 --apply 才真正写入（需要 root：sudo install-core.sh ... --apply）。
set -euo pipefail
PATH=/usr/bin:/bin:/usr/sbin:/sbin; export PATH

NEW=""; MODE=service; NAME=verge-mihomo-alpha; APPLY=0; EXPECT_SHA=""
while [ $# -gt 0 ]; do
  case "$1" in
    --service) MODE=service ;;
    --app) MODE=app ;;
    --name) NAME="${2:?--name 需要值}"; shift ;;
    --sha256) EXPECT_SHA="${2:?--sha256 需要值}"; shift ;;
    --apply) APPLY=1 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) [ -z "$NEW" ] && NEW="$1" || { echo "未知参数: $1" >&2; exit 64; } ;;
  esac
  shift
done
[ -n "$NEW" ] && [ -f "$NEW" ] || { echo "用法: $0 <新内核> --sha256 <期望sha256> [--service|--app] [--name NAME] [--apply]" >&2; exit 64; }
case "$NAME" in ''|*[!A-Za-z0-9._-]*) echo "✗ --name 只允许 [A-Za-z0-9._-]: $NAME" >&2; exit 64 ;; esac
case "$EXPECT_SHA" in ''|*[!0-9a-fA-F]*) echo "✗ 必须用 --sha256 固定期望摘要（不再对任意二进制 root 执行 -v）" >&2; exit 64 ;; esac
[ ${#EXPECT_SHA} -eq 64 ] || { echo "✗ --sha256 必须是 64 位十六进制" >&2; exit 64; }

SVC_ROOT="/Library/Application Support/clash-verge-service/cores"
APP_ROOT="/Applications/Clash Verge.app/Contents/MacOS"
SVC="$SVC_ROOT/$NAME"
APP="$APP_ROOT/$NAME"

echo "⓪ 源文件自证"
[ -f "$NEW" ] && [ ! -L "$NEW" ] || { echo "✗ 拒绝符号链接/非普通文件: $NEW" >&2; exit 1; }
SIZE=$(stat -f '%z' "$NEW")
[ "$SIZE" -gt 10000000 ] || { echo "✗ 文件太小（$SIZE B）⇒ 拒绝" >&2; exit 1; }
SHA=$(shasum -a 256 "$NEW" | awk '{print $1}')
[ "$SHA" = "$EXPECT_SHA" ] || { echo "✗ sha256 不匹配：实际 $SHA 期望 $EXPECT_SHA ⇒ 拒绝执行" >&2; exit 1; }
VERSION=$("$NEW" -v 2>&1 | head -1) || { echo "✗ 内核跑不起来 ⇒ 拒绝" >&2; exit 1; }
echo "   size=$SIZE"
echo "   version=$VERSION"
echo "   sha256=$SHA"
SIG=$(codesign -dv "$NEW" 2>&1 | grep CodeDirectory || true)
[ -n "$SIG" ] || { echo "✗ 没有 adhoc 签名 ⇒ arm64 上会被 SIGKILL" >&2; exit 1; }
case "$SIG" in *runtime*) echo "✗ flags 含 runtime（Hardened Runtime 会杀 wazero JIT）⇒ 拒绝" >&2; exit 1;; esac

TARGETS=("$SVC"); [ "$MODE" = app ] && TARGETS+=("$APP")
echo "① 目标（$MODE）"
for t in "${TARGETS[@]}"; do
  [ -f "$t" ] && echo "   $t = $(shasum -a 256 "$t" | awk '{print $1}')" || echo "   $t = (不存在)"
done
[ "$MODE" = app ] && echo "   ⚠️ 覆盖 App bundle 内可执行文件会使整包签名失效；装完若 App 起不来需重装 App" >&2

TS=$(date +%Y%m%d-%H%M%S)
echo "② 备份到 /tmp/core-backup-$NAME-$TS.*"
for t in "${TARGETS[@]}"; do [ -f "$t" ] && echo "   $t"; done

if [ "$APPLY" -eq 0 ]; then
  echo "（dry-run：加 --apply 执行；需要 sudo）"
  exit 0
fi
[ "$(id -u)" = 0 ] || { echo "✗ 需要 root：sudo $0 ... --apply" >&2; exit 1; }

BK=$(mktemp -d "/tmp/core-backup-$NAME.XXXXXX") || { echo "✗ 无法创建备份目录" >&2; exit 1; }
chmod 700 "$BK"
for t in "${TARGETS[@]}"; do
  [ -f "$t" ] && cp -p "$t" "$BK/$(basename "$(dirname "$t")")-$(basename "$t")"
done
echo "   备份目录: $BK"
echo "③ 安装"
for t in "${TARGETS[@]}"; do install -m 755 "$NEW" "$t"; echo "   $t"; done

echo "④ 重启并自证"
launchctl kickstart -k system/io.github.clash-verge-rev.clash-verge-rev 2>/dev/null || true
ok=0
for _ in $(seq 1 15); do
  sleep 2
  P=$(pgrep -f "clash-verge-service/cores/$NAME" || true)
  if [ -n "$P" ]; then
    V=$("$SVC" -v 2>&1 | head -1 || true)
    [ "$V" = "$VERSION" ] && { ok=1; echo "   ✓ pid=$P version=$V"; break; }
  fi
done
[ "$ok" = 1 ] || { echo "✗ 重启后版本不符或没有起来；备份在 $BK" >&2; exit 1; }
echo "✅ 完成"
