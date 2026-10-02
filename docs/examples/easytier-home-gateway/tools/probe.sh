#!/usr/bin/env bash
# 通用探针：通过一个 SOCKS/HTTP 混合入口同时验证 TCP 与 UDP 数据面。
#
# 用法：
#   bash probe.sh <proxy_host:port> <target_ip> <tcp_port> <udp_port> [tcp_path]
# 例：
#   bash probe.sh 127.0.0.1:17890 192.168.1.10 80 53 /index.html
#
# 退出码：0 = TCP 与 UDP 都通；2 = 至少一项失败（细节打印在输出里）。
set -uo pipefail

PROXY="${1:?proxy host:port}"
TARGET="${2:?target ip}"
TCP_PORT="${3:?tcp port}"
UDP_PORT="${4:?udp port}"
TCP_PATH="${5:-/}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "── TCP 经 $PROXY → $TARGET:$TCP_PORT$TCP_PATH ──"
CODE="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 12 -x "http://$PROXY" "http://$TARGET:$TCP_PORT$TCP_PATH" 2>/tmp/probe-tcp.err)"
if [[ "$CODE" =~ ^(2|3|4)[0-9][0-9]$ ]]; then
  echo "PASS[tcp] HTTP $CODE（能拿到状态码就说明 TCP 链路通）"
  TCP_OK=1
else
  echo "FAIL[tcp] code=$CODE $(head -1 /tmp/probe-tcp.err 2>/dev/null)"
  TCP_OK=0
fi

echo "── UDP 经 $PROXY → $TARGET:$UDP_PORT ──"
UDP_OUT="$(python3 "$HERE/socks5-udp-probe.py" --socks "$PROXY" --target "$TARGET" --port "$UDP_PORT" \
  --payload "probe-$(date +%s)" --timeout 6 2>&1)"
echo "$UDP_OUT" | sed 's/^/  /'
UDP_OK=0; [[ "$UDP_OUT" == *"PASS[udp]"* ]] && UDP_OK=1

echo
printf 'TCP=%s UDP=%s\n' "$([[ $TCP_OK == 1 ]] && echo PASS || echo FAIL)" "$([[ $UDP_OK == 1 ]] && echo PASS || echo FAIL)"
[[ $TCP_OK == 1 && $UDP_OK == 1 ]]
