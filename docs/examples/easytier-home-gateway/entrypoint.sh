#!/bin/sh
# mihomo 镜像入口（本 fork 构建的内核；镜像里不含任何配置，出站/规则/TUN 全部来自挂载的 YAML）
#
# 两种模式：
#   1) **纯 mihomo（默认）**：没有传任何网关相关环境变量时走这里 —— 直接 exec 内核，什么都不加。
#      适合"挂自己的配置当容器里的代理"。
#   2) **家侧网关模式**：传了 NAT_INTERFACE 或 SKIP_NAT（或显式 GATEWAY=1）才启用 —— 只做一件事：
#      给"当路由器"补上 SNAT。家侧的 docker-compose.yml 正是这样传的。
#
# 网关模式为什么只需要这一件事（其余职责都不该由入口脚本承担）：
#   * **实例停止 / 静默失败** → 内核自己的监督循环（upstream #3215 的 loop()/serve()）秒级检测、
#     按退避重建、带日志，而且只重建实例、不断其它连接。旧的自研看门狗因此已删除（见 SOP §6.12）。
#   * **内核退出 / 崩溃** → 容器随 PID 1 退出 → `restart: unless-stopped` 重建（退避限流交给 Docker）。
#   * **进程挂死（API 不通）** → 不自动处理：compose 的 healthcheck 会把它标成 unhealthy（可见 ✓），
#     恢复是一条命令（`docker compose restart mihomo`，见 SOP §6.13 的取舍说明）。
#   * **实例的懒启动** → 家侧配置里写 `prewarm: true` 即可，不再需要在入口里用 API 触发。
#   * **NAT 不是"补丁"而是拓扑后果**：TUN 模式下家侧是真路由器，转发出去的包带的是客户端的 overlay
#     源地址（10.144.0.x），家里内网的机器无法回包；MASQUERADE 把源改写成本机的内网地址后回包才通。
#     想彻底不用 NAT：把这台机器改成"被路由"的方式（SKIP_NAT=1 + 在需要访问的机器上加一条
#     `10.144.0.0/24 via <本机内网IP>` 的静态路由），详见 SOP §6.13。
#
# 环境变量：
#   GATEWAY=auto|1|0   模式（默认 auto：按是否传了 NAT_INTERFACE / SKIP_NAT 推断）
#   MIHOMO_DIR         配置目录（默认 /root/.config/mihomo）
#   MIHOMO_CONFIG      配置文件（默认 $MIHOMO_DIR/config.yaml）
#   NAT_INTERFACE      连内网的那张网卡（默认 eth0；多网卡时用 ip -o -4 addr show 确认后改）
#   SKIP_NAT=1         跳过 SNAT（仅当家里机器已按上面的方式加了静态路由时才正确）
set -e

MIHOMO_DIR="${MIHOMO_DIR:-/root/.config/mihomo}"
MIHOMO_CONFIG="${MIHOMO_CONFIG:-$MIHOMO_DIR/config.yaml}"
IFACE="${NAT_INTERFACE:-eth0}"

GATEWAY="${GATEWAY:-auto}"
if [ "$GATEWAY" = "auto" ]; then
    if [ -n "${NAT_INTERFACE:-}${SKIP_NAT:-}" ]; then
        GATEWAY=1
    else
        GATEWAY=0
    fi
fi

if [ "$GATEWAY" != "1" ]; then
    echo "[entrypoint] plain mihomo mode; dir=$MIHOMO_DIR config=$MIHOMO_CONFIG"
    echo "[entrypoint] (set GATEWAY=1 or NAT_INTERFACE/SKIP_NAT to enable the home-gateway behavior)"
    exec /usr/local/bin/mihomo -d "$MIHOMO_DIR" -f "$MIHOMO_CONFIG"
fi

echo "[entrypoint] home gateway mode (NAT interface: $IFACE, config: $MIHOMO_CONFIG)"

# SNAT（幂等）：见文件头对"为什么需要"的说明
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
    echo "[entrypoint] SNAT skipped (SKIP_NAT=1)；请确认家里机器已加 10.144.0.0/24 的静态路由" >&2
fi

echo "[entrypoint] starting mihomo (ip_forward: $(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo '?'))"
exec /usr/local/bin/mihomo -d "$MIHOMO_DIR" -f "$MIHOMO_CONFIG"
