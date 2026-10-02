#!/bin/sh
# mihomo 镜像入口（本 fork 构建的内核；镜像里不含任何配置，出站/规则/TUN 全部来自挂载的 YAML）
#
# 两种模式：
#   1) **纯 mihomo（默认）**：没有传任何网关相关环境变量时走这里 —— 直接 exec 内核，
#      不做 NAT、不触发出站、不开看门狗。适合"挂自己的配置当容器里的代理"。
#   2) **家侧网关模式**：只要传了 NAT_INTERFACE / TRIGGER_PROXY / WATCHDOG / SKIP_NAT / API_BASE
#      中的任意一个（或显式 GATEWAY=1），就启用下面三件事。家侧的 docker-compose.yml 正是这样传的。
#
# 网关模式为什么要这三件事（家侧这个"服务器角色"特有）：
#   ① NAT：TUN 模式让家侧成为真路由器，内网主机看到的是 overlay 源地址，回包必须能被 NAT 回去。
#      容器内 /proc/sys 只读，ip_forward 由 compose 的 sysctls 注入，NAT 只能在这里加（幂等）。
#   ② 触发懒启动：mihomo 的出站是懒启动的，而家侧**没有任何流量会把这个出站当代理用**
#      （家侧自己出网走直连），所以实例永远不会自启、TUN 也不会创建 ——
#      表现为"容器起来了但网关并不在"，客户端根本连不进来。客户端那侧相反：保持懒启动更好。
#   ③ 看门狗：只在"本地 easytier 网卡消失"时优雅重启实例；会合点掉线/DNS 抖动/API 报错都不触发
#      （实例自己会重连，重启反而有害），并且带跨重启限流，绝不允许重启风暴。
#
# 环境变量：
#   GATEWAY=auto|1|0   模式（默认 auto：按是否传了下面这些变量推断）
#   MIHOMO_DIR         配置目录（默认 /root/.config/mihomo）
#   MIHOMO_CONFIG      配置文件（默认 $MIHOMO_DIR/config.yaml）
#   NAT_INTERFACE      连内网的那张网卡（默认 eth0；多网卡时用 ip -o -4 addr show 确认后改）
#   SKIP_NAT=1         完全跳过 NAT（例如家侧本来就是内网网关时）
#   TRIGGER_PROXY      要触发的出站名（默认 et-home，与示例配置一致）
#   API_BASE           内核 API 地址（默认 http://127.0.0.1:9090；改了配置里的 external-controller 时同步改这里）
#   WATCHDOG / WATCHDOG_INTERVAL / WATCHDOG_FAILS / WATCHDOG_MAX_RESTARTS / WATCHDOG_WINDOW
set -e

MIHOMO_DIR="${MIHOMO_DIR:-/root/.config/mihomo}"
MIHOMO_CONFIG="${MIHOMO_CONFIG:-$MIHOMO_DIR/config.yaml}"
IFACE="${NAT_INTERFACE:-eth0}"
PROXY="${TRIGGER_PROXY:-et-home}"
API="${API_BASE:-http://127.0.0.1:9090}"

GATEWAY="${GATEWAY:-auto}"
if [ "$GATEWAY" = "auto" ]; then
    if [ -n "${NAT_INTERFACE:-}${TRIGGER_PROXY:-}${WATCHDOG:-}${SKIP_NAT:-}${API_BASE:-}" ]; then
        GATEWAY=1
    else
        GATEWAY=0
    fi
fi

if [ "$GATEWAY" != "1" ]; then
    echo "[entrypoint] plain mihomo mode; dir=$MIHOMO_DIR config=$MIHOMO_CONFIG"
    echo "[entrypoint] (set GATEWAY=1 or any of NAT_INTERFACE/TRIGGER_PROXY/WATCHDOG to enable the home-gateway behavior)"
    exec /usr/local/bin/mihomo -d "$MIHOMO_DIR" -f "$MIHOMO_CONFIG"
fi

echo "[entrypoint] home gateway mode (NAT interface: $IFACE, config: $MIHOMO_CONFIG)"

# ---- ① NAT（幂等）----
if [ "${SKIP_NAT:-0}" != "1" ]; then
    if iptables -t nat -C POSTROUTING -o "$IFACE" -j MASQUERADE 2>/dev/null; then
        echo "[entrypoint] MASQUERADE already present on $IFACE"
    elif iptables -t nat -A POSTROUTING -o "$IFACE" -j MASQUERADE 2>/dev/null; then
        echo "[entrypoint] MASQUERADE added on $IFACE"
    else
        echo "[entrypoint] WARN: 无法在 $IFACE 上加 MASQUERADE（缺 NET_ADMIN？内网回包可能不通）" >&2
    fi
    iptables -P FORWARD ACCEPT 2>/dev/null || true
else
    echo "[entrypoint] NAT skipped (SKIP_NAT=1)"
fi

# ---- ② 启动内核 + 触发懒启动 ----
echo "[entrypoint] starting mihomo (ip_forward: $(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo '?'))"
/usr/local/bin/mihomo -d "$MIHOMO_DIR" -f "$MIHOMO_CONFIG" &
MIHOMO_PID=$!
trap 'kill -TERM "$MIHOMO_PID" 2>/dev/null || true' TERM INT

# 等 API 就绪（最多 30s）
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

# ---- ③ 看门狗 ----
WATCHDOG="${WATCHDOG:-1}"
WATCHDOG_INTERVAL="${WATCHDOG_INTERVAL:-60}"
WATCHDOG_FAILS="${WATCHDOG_FAILS:-3}"
WATCHDOG_MAX_RESTARTS="${WATCHDOG_MAX_RESTARTS:-3}"
WATCHDOG_WINDOW="${WATCHDOG_WINDOW:-3600}"
WATCHDOG_STATE="$MIHOMO_DIR/easytier/.watchdog-restarts"

watchdog() {
    fails=0
    warned=0
    while kill -0 "$MIHOMO_PID" 2>/dev/null; do
        sleep "$WATCHDOG_INTERVAL"
        if ip -o link show 2>/dev/null | grep -q ": easytier"; then
            if [ "$fails" -gt 0 ]; then
                echo "[watchdog] gateway is back after $fails failed check(s)"
            fi
            fails=0
            warned=0
            continue
        fi
        fails=$((fails + 1))
        if [ "$warned" -eq 0 ]; then
            echo "[watchdog] easytier 网卡不见了（$fails/$WATCHDOG_FAILS）；只有本地信号会计数，会合点掉线不计" >&2
            warned=1
        fi
        [ "$fails" -lt "$WATCHDOG_FAILS" ] && continue

        now=$(date +%s)
        [ -f "$WATCHDOG_STATE" ] || : > "$WATCHDOG_STATE"
        awk -v now="$now" -v w="$WATCHDOG_WINDOW" '$1 > now - w' "$WATCHDOG_STATE" >"$WATCHDOG_STATE.tmp" 2>/dev/null || true
        mv "$WATCHDOG_STATE.tmp" "$WATCHDOG_STATE" 2>/dev/null || true
        count=$(wc -l <"$WATCHDOG_STATE" 2>/dev/null | tr -d ' ')
        if [ "${count:-0}" -ge "$WATCHDOG_MAX_RESTARTS" ]; then
            echo "[watchdog] ${WATCHDOG_WINDOW}s 内已自动恢复 ${count} 次，停止自动恢复以免反复重启；请人工排查：docker compose logs mihomo" >&2
            return
        fi
        echo "$now" >>"$WATCHDOG_STATE"
        echo "[watchdog] 连续 $WATCHDOG_FAILS 次未见网卡 → 优雅停止 mihomo，交由 restart 策略重建实例（本窗口第 $((count + 1)) 次）" >&2
        kill -TERM "$MIHOMO_PID" 2>/dev/null || true
        return
    done
}

if [ "$WATCHDOG" = "1" ]; then
    watchdog &
    echo "[watchdog] enabled (interval ${WATCHDOG_INTERVAL}s, ${WATCHDOG_FAILS} fails, max ${WATCHDOG_MAX_RESTARTS}/${WATCHDOG_WINDOW}s)"
else
    echo "[watchdog] disabled (WATCHDOG=0)"
fi

wait "$MIHOMO_PID"
