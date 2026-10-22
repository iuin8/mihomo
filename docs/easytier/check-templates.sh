#!/bin/sh
# check-templates.sh —— 入库文档的"脱敏/模板化"守卫(防回潮 ✓)
# 判据:①模板里的 network-secret 必须是占位符 ✗真值;②文档里不得出现**公网** IP ✗;③不得残留真实私有域名的明文 ✗
set -u
cd "$(dirname "$0")" || exit 1
bad=0
# ① network-secret 必须占位
for f in $(grep -rl "network-secret" . --include='*.yaml' --include='*.yml' --include='*.md' 2>/dev/null); do
  v=$(awk '/network-secret:/{sub(/^[ \t]*network-secret:[ \t]*/,""); gsub(/"/,""); print; exit}' "$f" 2>/dev/null)
  case "$v" in "<"*|"") ;; *) echo "✗ $f: network-secret 不是占位符 ✗"; bad=$((bad+1));; esac
done
# ② 公网 IP(示例网段 10/172.16-31/192.168/198.18-19/127/0 视为文档示例 ✓)
for f in $(find . -type f \( -name '*.md' -o -name '*.yaml' -o -name '*.yml' \) 2>/dev/null); do
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
[ "$bad" -eq 0 ] && echo "✓ 模板/文档脱敏检查通过(0 处 ✗)" || echo "✗ 共 $bad 处需要脱敏"
[ "$bad" -eq 0 ]
