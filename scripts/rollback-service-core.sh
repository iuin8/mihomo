#!/bin/sh
# FORK: 把服务侧内核回滚到最近一次 install-service-core.sh 之前的版本。
set -e
NAME="${1:-verge-mihomo-alpha}"
SVC="/Library/Application Support/clash-verge-service/cores/$NAME"
BK=$(ls -t /tmp/core-backup-"$NAME"-* 2>/dev/null | head -1)
[ -n "$BK" ] || { echo "✗ 找不到备份（/tmp/core-backup-$NAME-*）"; exit 1; }
echo "① 用备份: $BK"
cp -p "$SVC" "/tmp/core-rolledback-$NAME-$(date +%Y%m%d-%H%M%S)"
cp -p "$BK" "$SVC"
chmod 755 "$SVC"
echo "   ✓ 已回滚"
echo ""
echo "⚠ 请在 Clash Verge 里点一次「重启内核」。"
