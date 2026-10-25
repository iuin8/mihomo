#!/usr/bin/env bash
# install-helpers.sh —— 安装用户侧工具 + 生成 root helper 与 sudoers（不把真实用户名/规则放进仓库）。
#
#   tools/ops/install-helpers.sh [--user NAME] [--apply]
#
# dry-run 打印计划；--apply 需要 root（安装 /usr/local/libexec 与 /etc/sudoers.d）。
# 会做：
#   1) install -m 755 tools/ops/et-apply → /usr/local/libexec/clash-group/et-apply
#   2) 生成 /etc/clash-group/et-apply.conf（0600 root）：STATE_DIR/UID（供 root 助手定位用户目录）
#   3) 校验并安装 sudoers（0440 root:wheel）：单条裸规则，安全性由助手 $1 白名单保证
#   4) 把 et-config/et-net-mode 链接进用户 ~/.local/bin（已存在则先备份）
set -euo pipefail
PATH=/usr/bin:/bin:/usr/sbin:/sbin; export PATH
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
APPLY=0; USER_NAME=${SUDO_USER:-$(id -un)}
while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1 ;;
    --user) USER_NAME="${2:?--user 需要值}"; shift ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 64 ;;
  esac; shift
done
case "$USER_NAME" in ''|*[!A-Za-z0-9._-]*) echo "✗ --user 只允许 [A-Za-z0-9._-]: $USER_NAME" >&2; exit 64 ;; esac
id -u "$USER_NAME" >/dev/null 2>&1 || { echo "✗ 用户不存在: $USER_NAME" >&2; exit 1; }
HOME_U=$(dscl . -read "/Users/$USER_NAME" NFSHomeDirectory 2>/dev/null | awk '{print $2}')
[ -n "$HOME_U" ] || { echo "✗ 无法解析用户 $USER_NAME 的 home" >&2; exit 1; }
UID_N=$(id -u "$USER_NAME")
STATE="$HOME_U/.local/state/clash-group/et-apply"
SVC=/usr/local/libexec/clash-group/et-apply
SUDOERS=/etc/sudoers.d/clash-group-et-apply
CONF=/etc/clash-group/et-apply.conf

echo "计划："
echo "  1) $HERE/et-apply → $SVC (755 root)"
echo "  2) $CONF (0600 root)：CLASH_GROUP_STATE_DIR=$STATE CLASH_GROUP_UID=$UID_N"
echo "  3) $SUDOERS (0440 root:wheel)：$USER_NAME ALL=(root) NOPASSWD: $SVC"
echo "  4) $HERE/{et-config,et-net-mode} → $HOME_U/.local/bin/ (符号链接)"
[ "$APPLY" -eq 1 ] || { echo "（dry-run：加 --apply 执行；第 1/2/3 步需要 sudo）"; exit 0; }
[ "$(id -u)" = 0 ] || { echo "✗ 需要 root：sudo $0 --apply" >&2; exit 1; }

BK=$(mktemp -d /tmp/clash-group-bak.XXXXXX) || { echo "✗ 无法创建备份目录" >&2; exit 1; }
chmod 700 "$BK"
install -d -o root -g wheel -m 755 /usr/local/libexec/clash-group
[ -f "$SVC" ] && cp -p "$SVC" "$BK/et-apply.bak"
install -o root -g wheel -m 755 "$HERE/et-apply" "$SVC"
install -d -o root -g wheel -m 700 /etc/clash-group
umask 077
printf 'CLASH_GROUP_STATE_DIR=%s\nCLASH_GROUP_UID=%s\n' "$STATE" "$UID_N" > "$CONF"
chown root:wheel "$CONF"; chmod 600 "$CONF"

TMP=$(mktemp /tmp/clash-group-sudoers.XXXXXX)
printf '%s ALL=(root) NOPASSWD: %s\n' "$USER_NAME" "$SVC" > "$TMP"
visudo -c -f "$TMP" >/dev/null
install -o root -g wheel -m 440 "$TMP" "$SUDOERS"; rm -f "$TMP"

install -d -o "$USER_NAME" -g staff -m 755 "$HOME_U/.local/bin"
umask 022          # 否则 root 的 077 会把符号链接建成 700 root ⇒ 用户 Permission denied
for t in et-config et-net-mode; do
  DST="$HOME_U/.local/bin/$t"
  [ -e "$DST" ] && cp -p "$DST" "$BK/$t.bak"
  ln -sfn "$HERE/$t" "$DST"
done
echo "✅ 完成：sudo -n $SVC status 可验证；用户工具已链接"
