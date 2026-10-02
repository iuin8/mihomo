#!/bin/sh
# mihomo 镜像入口（本 fork 构建的内核；镜像里不含任何配置，出站/规则/TUN 全部来自挂载的 YAML）
#
# 两种模式：
#   1) **纯 mihomo（默认）**：没有传任何网关相关环境变量时走这里 —— 直接 exec 内核，
#      不做 NAT、不开存活看护。适合"挂自己的配置当容器里的代理"。
#   2) **家侧网关模式**：传了 NAT_INTERFACE / SKIP_NAT / API_BASE / LIVENESS_* 中的任意一个
#      （或显式 GATEWAY=1）就启用下面两件事。家侧的 docker-compose.yml 正是这样传的。
#
# 网关模式只做两件事（其余职责都在内核里，见下）：
#   ① NAT：TUN 模式让家侧成为真路由器，内网主机看到的是 overlay 源地址，回包必须能被 NAT 回去。
#      容器内 /proc/sys 只读，ip_forward 由 compose 的 sysctls 注入，NAT 只能在这里加（幂等）。
#   ② 存活看护：只有"整个内核挂死（API 不通）"它才动手 —— 判据与 compose 的 healthcheck 完全一致，
#      连续失败就 TERM 掉内核（PID 1），交给 `restart: unless-stopped` 重建。
#
# 这里**不再**做的事，以及为什么：
#   * **不再**看 easytier 网卡是否消失 —— 那是"实例已经停了"的表象，现在由内核自己的监督循环
#     （upstream #3215 的 loop()/serve()）秒级检测并按退避重建：只重建实例、不断其它连接、带日志，
#     还不会像旧 shell 看门狗那样"一小时 3 次之后永久放弃"。
#   * **不再**用 API 去"触发懒启动" —— 那是给懒启动打的补丁（还要靠一次注定失败的 delay 探测）。
#     现在家侧配置直接写 `prewarm: true`，内核构造完就把实例起起来（只调一次 ensureStarted，
#     后续重建同样交给 loop()）。
#   * **不再**自己限流重启 —— 重启风暴由 Docker 的 restart 退避策略兜底，比脚本计数器可靠。
#
# 环境变量：
#   GATEWAY=auto|1|0      模式（默认 auto：按是否传了下面这些变量推断）
#   MIHOMO_DIR            配置目录（默认 /root/.config/mihomo）
#   MIHOMO_CONFIG         配置文件（默认 $MIHOMO_DIR/config.yaml）
#   NAT_INTERFACE         连内网的那张网卡（默认 eth0；多网卡时用 ip -o -4 addr show 确认后改）
#   SKIP_NAT=1            完全跳过 NAT（例如家侧本来就是内网网关时）
#   API_BASE              内核 API 地址（默认 http://127.0.0.1:9090；改了配置里的 external-controller 时同步改）
#   LIVENESS_INTERVAL     存活探测间隔秒数（默认 30；设 0 关闭存活看护）
#   LIVENESS_FAILS        连续失败多少次才重启内核（默认 3）
set -e

MIHOMO_DIR="${MIHOMO_DIR:-/root/.config/mihomo}"
MIHOMO_CONFIG="${MIHOMO_CONFIG:-$MIHOMO_DIR/config.yaml}"
IFACE="${NAT_INTERFACE:-eth0}"
API="${API_BASE:-http://127.0.0.1:9090}"

GATEWAY="${GATEWAY:-auto}"
if [ "$GATEWAY" = "auto" ]; then
    if [ -n "${NAT_INTERFACE:-}${SKIP_NAT:-}${API_BASE:-}${LIVENESS_INTERVAL:-}${LIVENESS_FAILS:-}" ]; then
        GATEWAY=1
    else
        GATEWAY=0
    fi
fi

if [ "$GATEWAY" != "1" ]; then
    echo "[entrypoint] plain mihomo mode; dir=$MIHOMO_DIR config=$MIHOMO_CONFIG"
    echo "[entrypoint] (set GATEWAY=1 or any of NAT_INTERFACE/SKIP_NAT/API_BASE/LIVENESS_* to enable the home-gateway behavior)"
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

# ---- ② 存活看护（判据 = API 是否应答，与 compose 的 healthcheck 一致）----
LIVENESS_INTERVAL="${LIVENESS_INTERVAL:-30}"
LIVENESS_FAILS="${LIVENESS_FAILS:-3}"

liveness() {
    fails=0
    # 等内核先起来，别把启动时间算成失败
    i=0
    while [ "$i" -lt 60 ]; do
        wget -q -O /dev/null --timeout=2 "$API/version" 2>/dev/null && break
        i=$((i + 1))
        sleep 1
    done
    while :; do
        sleep "$LIVENESS_INTERVAL"
        if wget -q -O /dev/null --timeout=3 "$API/version" 2>/dev/null; then
            fails=0
            continue
        fi
        fails=$((fails + 1))
        echo "[liveness] API 无响应（$fails/$LIVENESS_FAILS）：$API/version" >&2
        if [ "$fails" -ge "$LIVENESS_FAILS" ]; then
            echo "[liveness] 连续 $LIVENESS_FAILS 次无响应 → TERM 内核，交由 restart 策略重建" >&2
            # exec 之后内核就是 PID 1；容器内 PID 1 需要自己装了信号处理器才收得到信号，mihomo 装了。
            kill -TERM 1 2>/dev/null || true
            fails=0
        fi
    done
}

if [ "$LIVENESS_INTERVAL" = "0" ]; then
    echo "[liveness] disabled (LIVENESS_INTERVAL=0)"
else
    liveness &
    echo "[liveness] enabled (interval ${LIVENESS_INTERVAL}s, ${LIVENESS_FAILS} fails → restart core)"
fi

echo "[entrypoint] starting mihomo (ip_forward: $(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo '?'))"
exec /usr/local/bin/mihomo -d "$MIHOMO_DIR" -f "$MIHOMO_CONFIG"
