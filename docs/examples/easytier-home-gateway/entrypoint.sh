#!/bin/sh
# 家侧网关入口：① 配好"当路由器"需要的 NAT；② 启动 mihomo 并触发 easytier 出站的懒启动。
#
# 为什么需要第 ② 步：mihomo 的出站是**懒启动**的——家侧没有流量命中它时，实例不会启动，
# TUN 设备也就不会创建，表现为"容器起来了但网关并不在"。这里在 API 就绪后主动触发一次
# delay 请求，把它拉起来（需要配置里开着 external-controller，示例配置已开且绑在 0.0.0.0）。
#
# 为什么要在这里做 NAT：TUN 模式让家侧成为真路由器——内网主机看到的是 overlay 源地址，
# 回包必须能被 NAT 回去。容器内 /proc/sys 只读，所以 ip_forward 由 compose 的 sysctls 注入，
# NAT 只能在这里加。本脚本幂等，重启不会重复插入规则。
#
# 环境变量：
#   NAT_INTERFACE  连内网的那张网卡（默认 eth0；多网卡时用 ip -o -4 addr show 确认后改）
#   SKIP_NAT=1     完全跳过 NAT（例如家侧本来就是内网网关时）
#   TRIGGER_PROXY  要触发的出站名（默认 et-home，与示例配置一致）
set -e

IFACE="${NAT_INTERFACE:-eth0}"
PROXY="${TRIGGER_PROXY:-et-home}"
API="http://127.0.0.1:9090"

if [ "${SKIP_NAT:-0}" != "1" ]; then
    if iptables -t nat -C POSTROUTING -o "$IFACE" -j MASQUERADE 2>/dev/null; then
        echo "[entrypoint] MASQUERADE already present on $IFACE"
    elif iptables -t nat -A POSTROUTING -o "$IFACE" -j MASQUERADE 2>/dev/null; then
        echo "[entrypoint] added MASQUERADE on $IFACE"
    else
        echo "[entrypoint] WARN: 无法在 $IFACE 上添加 MASQUERADE（缺少 NET_ADMIN，或网卡名不对）" >&2
        echo "[entrypoint] WARN: 内网主机的回包可能收不到；检查 cap_add 与 NAT_INTERFACE" >&2
    fi
    iptables -P FORWARD ACCEPT 2>/dev/null || true
fi

echo "[entrypoint] starting mihomo (NAT interface: $IFACE, ip_forward: $(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo '?'))"
/usr/local/bin/mihomo -d /root/.config/mihomo -f /root/.config/mihomo/config.yaml &
MIHOMO_PID=$!
trap 'kill -TERM "$MIHOMO_PID" 2>/dev/null || true' TERM INT

# 等 API 就绪（最多 30s），然后触发出站懒启动；失败不阻塞容器启动，只留下告警。
i=0
while [ "$i" -lt 30 ]; do
    if wget -q -O /dev/null --timeout=2 "$API/version" 2>/dev/null; then
        break
    fi
    i=$((i + 1))
    sleep 1
done

# 触发出站懒启动。注意：不能用这条请求的返回码判断成败——delay 探测本身会失败（家侧没有
# 那个探测目标），API 返回 5xx，但出站已经被拉起来了。所以触发后改为轮询 easytier 网卡是否出现。
wget -q -O /dev/null --timeout=10 "$API/proxies/$PROXY/delay?url=http://127.0.0.1&timeout=3000" 2>/dev/null || true

i=0
while [ "$i" -lt 20 ]; do
    if ip -o link show 2>/dev/null | grep -q ": easytier"; then
        echo "[entrypoint] easytier TUN is up: $(ip -o -4 addr show | awk '/easytier/ {print $2" "$4; exit}')"
        break
    fi
    i=$((i + 1))
    sleep 1
done
if [ "$i" -ge 20 ]; then
    echo "[entrypoint] WARN: 触发后 20s 内未见 easytier TUN；检查 external-controller 是否开启、TRIGGER_PROXY 是否与配置里的出站名一致、以及日志中的 EasyTier 报错" >&2
fi

wait "$MIHOMO_PID"
