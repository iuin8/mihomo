#!/bin/sh
# FORK: 把内核装进 macOS 服务目录（**只装这一处**）。
#
# 为什么只装这一处：服务模式跑的内核是
#   /Library/Application Support/clash-verge-service/cores/<name>
# 它**不是** bundle，所以换文件**不会**破坏任何签名 —— 而 App bundle 里那份**不能随便换**：
# 换掉 bundle 内的可执行文件会让整个 bundle 的签名失效，服务启动时会被 Gatekeeper SIGKILL。
#
# 用法: sudo sh install-service-core.sh <新内核路径> [内核名]
#   内核名默认 verge-mihomo-alpha（与 App 的 alpha 通道一致）
set -e
NEW="${1:?用法: sudo sh install-service-core.sh <新内核路径> [内核名]}"
NAME="${2:-verge-mihomo-alpha}"
SVC="/Library/Application Support/clash-verge-service/cores/$NAME"

[ -f "$NEW" ] || { echo "✗ 找不到源文件: $NEW"; exit 1; }

echo "⓪ 源文件核验（三项，任一不过就**拒绝安装** ✓）"
SZ=$(stat -f '%z' "$NEW")
printf '   ① 字节数: %s\n' "$SZ"
if [ "$SZ" -lt 10000000 ]; then echo "   ✗ 太小 ⇒ 拒绝（内核约 60–90MB）"; exit 1; fi
printf '   ② 版本:   %s\n' "$("$NEW" -v 2>&1 | head -1)"
if ! "$NEW" -v >/dev/null 2>&1; then echo "   ✗ 跑不起来 ⇒ 拒绝"; exit 1; fi
FLAGS=$(codesign -dv "$NEW" 2>&1 | grep -oE 'flags=[^)]*\)' || echo '（无签名）')
printf '   ③ 签名:   %s\n' "$FLAGS"
case "$FLAGS" in
  *runtime*) echo "   ✗ flags 里有 **runtime**（Hardened Runtime 会杀 wazero 的 JIT）⇒ 拒绝"; exit 1 ;;
esac
echo "   ✓ 三项通过"

if [ "$(id -u)" != "0" ]; then
  echo "✗ 需要 root 权限才能写入服务目录。"
  echo "  请改用: sudo sh $0 $*"
  exit 1
fi

TS=$(date +%Y%m%d-%H%M%S)
BK="/tmp/core-backup-$NAME-$TS"
echo "① 备份现有内核 → $BK"
cp -p "$SVC" "$BK"
echo "   ✓ 已备份"

echo "② 安装"
cp "$NEW" "$SVC"
chmod 755 "$SVC"
echo "   ✓ 已装"

echo "③ 复验"
printf '   版本: %s\n' "$SVC" >/dev/null
printf '   flags: %s（不应含 runtime ✓）\n' "$(codesign -dv "$SVC" 2>&1 | grep -oE 'flags=[^)]*\)')"

echo ""
echo "✓ 完成。回滚命令（记住这一行即可）："
echo "   sudo cp -p $BK \"$SVC\""
echo ""
echo "⚠ 现在请在 Clash Verge 里点一次「重启内核」——服务**不会**自动拉起被换掉的内核。"
