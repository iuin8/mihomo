#!/bin/sh
# check-templates.sh —— 入库文档的"脱敏/模板化"守卫(防回潮 ✓)
# 判据:①模板里的 network-secret 必须是占位符 ✗真值;②文档里不得出现**公网** IP ✗;③不得残留真实私有域名的明文 ✗
set -u
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR" || exit 1
SCAN_ROOTS="$SCRIPT_DIR $(cd "$SCRIPT_DIR/../examples/easytier" && pwd)"
bad=0
# ① network-secret 必须占位
for f in $(grep -rl "network-secret" $SCAN_ROOTS --include='*.yaml' --include='*.yml' --include='*.md' 2>/dev/null); do
  v=$(awk '/network-secret:/{sub(/^[ \t]*network-secret:[ \t]*/,""); gsub(/"/,""); print; exit}' "$f" 2>/dev/null)
  case "$v" in "<"*|"") ;; *) echo "✗ $f: network-secret 不是占位符 ✗"; bad=$((bad+1));; esac
done
# ② 公网 IP(示例网段 10/172.16-31/192.168/198.18-19/127/0 视为文档示例 ✓)
for f in $(find $SCAN_ROOTS -type f \( -name '*.md' -o -name '*.yaml' -o -name '*.yml' \) 2>/dev/null); do
  for ip in $(grep -rhoE '\b[0-9]{1,3}(\.[0-9]{1,3}){3}\b' "$f" 2>/dev/null | sort -u); do
    case "$ip" in
      10.*|127.*|0.*|198.18.*|198.19.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*|255.*) ;;
      1.1.1.1|8.8.8.8|8.8.4.4|223.5.5.5|223.6.6.6|119.29.29.29|9.9.9.9) ;;   # 公开 DNS = 文档标准值 ✓
      169.254.*|172.32.0.*) ;;                                              # 链路本地/文档示例 ✓
      128.0.0.0|128.0.0.1) ;;                                               # 默认路由右半区 128.0.0.0/1 ✓
      69.171.235.22) ;;                                                     # 被投毒的 DNS 答案(证据示例)✓
      *) echo "✗ $f: 出现疑似公网 IP($ip ✗)⇒ 请改成占位符"; bad=$((bad+1));;
    esac
  done
done

# ③ 真实内网目标（denylist 在**本机**，绝不入 git ✓）
#    有该文件才检查；敏感字样因此不会出现在仓库里 ✓
# 默认路径从脚本位置推导（mihomo/docs/easytier → 项目根/local-state），
# 这样在任何机器/目录结构下都成立；可用 ET_DENYLIST 覆盖。
DENY="${ET_DENYLIST:-$(cd "$SCRIPT_DIR/../../.." && pwd)/local-state/sensitive.txt}"
if [ -r "$DENY" ]; then
  hits=0
  while IFS= read -r pat; do
    case "$pat" in ''|'#'*) continue ;; esac
    # 文档/模板 + 仓库内运维工具（.sh / 无扩展名）都要扫；命中文件名即可，不回显整行
    for f in $(grep -rlF "$pat" $SCAN_ROOTS --include='*.md' --include='*.yaml' --include='*.yml' . 2>/dev/null) \
             $(grep -rlF "$pat" "$SCRIPT_DIR/../../tools/ops" 2>/dev/null); do
      hits=$((hits+1))
      echo "✗ $f: 出现真实内网目标（denylist 第 $hits 条命中）⇒ 请改占位符"
      bad=$((bad+1))
    done
  done < "$DENY"
  [ "$hits" -eq 0 ] && echo "✓ 未出现 denylist 中的真实目标"
else
  # 缺文件是 fail-open；但必须**可见**，不能把"没检查"混成"检查通过"
  echo "⚠️ 未找到 denylist（${DENY}）⇒ 跳过真实目标检查（可用 ET_DENYLIST 指定）"
fi

[ "$bad" -eq 0 ] && echo "✓ 模板/文档脱敏检查通过(0 处 ✗)" || echo "✗ 共 $bad 处需要脱敏"
[ "$bad" -eq 0 ]
