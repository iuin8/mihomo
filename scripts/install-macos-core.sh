#!/usr/bin/env bash
#
# FORK: 把自定义编译的内核装进 macOS 上的 Clash Verge 服务目录。
#
# 为什么需要这个脚本（每一步都是踩过的坑）:
#   1) 服务模式真正运行的内核不在 App 目录里，而是
#      /Library/Application Support/clash-verge-service/cores/<name>
#      （用 `pgrep -fl clash-verge-service/cores` 确认；只换 App 目录不会生效）
#   2) arm64 上所有可执行文件必须至少有 ad-hoc 签名；
#      直接 cp 覆盖会丢掉签名，内核一启动就被 SIGKILL（Killed: 9）。
#      => 必须先按原件（App 侧那份）的签名参数签好，再拷进去。
#   3) 已经在跑的进程不受签名复检影响，所以症状是"服务看着正常、内核一重启就起不来"。
#
# 用法:
#   ./scripts/install-macos-core.sh <新内核路径> [内核名]
#   例: ./scripts/install-macos-core.sh /tmp/verge-mihomo-alpha-new verge-mihomo-alpha
#
set -euo pipefail

NEW="${1:-}"
NAME="${2:-verge-mihomo-alpha}"
[ -n "$NEW" ] && [ -f "$NEW" ] || { echo "用法: $0 <新内核路径> [内核名]"; exit 1; }

APP_REF="/Applications/Clash Verge.app/Contents/MacOS/$NAME"
DST="/Library/Application Support/clash-verge-service/cores/$NAME"
[ -e "$APP_REF" ] || { echo "✗ 找不到参考原件: $APP_REF"; exit 1; }

echo "⓪ 拷前断言"
SIG=$(codesign -dv "$NEW" 2>&1 | grep CodeDirectory || true)
echo "   $SIG"
echo "$SIG" | grep -q 'adhoc' || { echo "✗ 新内核没有 adhoc 签名 => 会 SIGKILL，拒绝安装"; exit 1; }
"$NEW" -v >/dev/null 2>&1 || { echo "✗ 新内核跑不起来 => 拒绝安装"; exit 1; }
echo "   ✓ 已签名且可运行"

TS=$(date +%Y%m%d-%H%M%S)
echo "① 备份现有内核"
for p in "$APP_REF" "$DST"; do [ -f "$p" ] && cp "$p" "/tmp/$(basename "$p").bak-$TS" && echo "   $p ⇒ /tmp/$(basename "$p").bak-$TS"; done

echo "② 停掉正在跑的内核（服务会自己拉起）"
PID=$(pgrep -f "clash-verge-service/cores/$NAME" || true)
[ -n "$PID" ] && { kill "$PID"; echo "   已 kill PID=$PID"; sleep 3; } || echo "   （没有在跑的内核）"

echo "③ 安装（App 侧 + 服务侧两处都要换）"
chmod +x "$NEW"
cp "$NEW" "$APP_REF"
cp "$NEW" "$DST"

echo "④ 拷后复验"
codesign -dv "$DST" 2>&1 | grep CodeDirectory | sed 's/^/   /'
echo "   指纹断言: $(strings "$DST" | grep -c 'ssh-config-path') 处 ssh-config-path（fork 内核应为 ≥1）"

echo "⑤ 拉起内核"
launchctl kickstart -k system/io.github.clash-verge-rev.clash-verge-rev 2>/dev/null || true
for _ in $(seq 1 12); do
  P=$(pgrep -f "clash-verge-service/cores/$NAME" || true)
  [ -n "$P" ] && { echo "   ✓ 已起来 PID=$P"; break; }
  sleep 2
done
P=$(pgrep -f "clash-verge-service/cores/$NAME" || true)
[ -n "$P" ] || { echo "   ✗ 内核没起来 => 请在 App 里点一次「重启内核」"; exit 1; }

echo "⑥ 最终验证"
"$DST" -v 2>&1 | head -1 | sed 's/^/   /'
echo "✅ 完成"
