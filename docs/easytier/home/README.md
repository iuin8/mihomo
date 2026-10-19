# 家侧（home）：公告内网 + 替别人落地出网

**它是什么**：家里那台机器上的 mihomo，两个 easytier 节点（一个公告家里网段、一个连出口），
外加一个可选的「**接管宿主机终端**」模式 —— 让家里**所有**设备/终端都走这条隧道 ✓。

| 文件 | 用途 |
| --- | --- |
| [`docker-compose.yml`](docker-compose.yml) | 部署（`ghcr.io/iuin8/mihomo` 镜像；**已按"接管宿主"配好** `network_mode: host`） |
| [`gateway.yaml`](gateway.yaml) | mihomo 配置：两个 easytier 节点 + `tun` + DNS 策略 + 规则（全部 `<改这里…>` 占位 ✓） |

## 前置

* 家里这台机器能跑 Docker ✓；
* 一个**会合点**的公网地址（见 [`../rendezvous/`](../rendezvous/) ✓）；
* 与出口侧一致的 `network-name` / `network-secret` ✓；
* **本 fork 的内核**（镜像 `ghcr.io/iuin8/mihomo` 就是 ✓）：上游内核会**静默忽略** `tun: true` ✗。

## 部署

```bash
# 1) 把两份文件放到一个目录（不要放在 git checkout 里 ✓ 免得被 pull/clean 覆盖 ✗）
mkdir -p /data/docker/mihomo/easytier-gateway && cd /data/docker/mihomo/easytier-gateway

# 2) 改 gateway.yaml 里所有 <改这里…>（密钥 / 会合点 / 家里网段 / geoip 目录）
# 3) 校验后启动
docker run --rm -v "$PWD/gateway.yaml":/root/.config/mihomo/config.yaml:ro \
  -v /data/docker/mihomo/geoip/geoip.metadb:/root/.config/mihomo/geoip/metadb:ro \
  --entrypoint mihomo ghcr.io/iuin8/mihomo:latest -t -d /root/.config/mihomo   # ⇒ test is successful ✓
docker compose -p easytier-gateway up -d

# 4) 起之前确认 host 模式下要占的端口是空的（7890 / 9090 / 11110）
ss -lntp | grep -E ':7890|:9090|:11110' || echo "空闲 ✓"
```

## 判据（做完这四条才算成 ✓）

```bash
# ① 实例起来了、监听口在
docker logs mihomo-gateway 2>&1 | grep -E "easytier.*(running|listener)" | tail -3
# ② 宿主上出现 mihomo 的 TUN（接管生效 ✓）
ip -o link show | grep -c Meta          # ⇒ 1 ✓
# ③ 宿主终端能上国外（这是"接管"的最终判据 ✓）
curl -s -o /dev/null -w '%{http_code}\n' https://www.google.com/     # ⇒ 200 ✓
# ④ ★ 管理面没被一起接管（否则你会把自己的 SSH 送进隧道 ✗）
ip route get <你的 SSH 来源 IP>          # ⇒ 应显示 dev <物理网卡> ✓，不是 Meta ✗
```

## ⚠️ 已知坑（都实测过）

* **`sysctls` 与 host 网络互斥** ✗：host 模式下 Docker 报错 ⇒ 本目录的 compose 已**不含** `sysctls` ✓；
* **端口冲突** ✗：host 模式下容器端口 = 宿主端口；若同机已有别的 easytier 网关占着 `11010`，
  把**本机监听口**改成 `11110` ✓（`peers:` 里的会合点端口**不要**改 ✗）；
* **管理面必须排除** ✗：`tun.route-exclude-address` 里的私网段（`10/8`、`172.16/12`、`192.168/16`）
  一个都不能少 ✓；否则 SSH 来源网段、内网、Docker 网桥会被一起送进隧道 ✗；
* **动宿主网络前先备份 + 挂自动回滚** ✓：
  ```bash
  cp docker-compose.yml gateway.yaml /tmp/backup-$(date +%m%d-%H%M)/
  ( sleep 600; [ -f /tmp/ok ] || docker compose -p easytier-gateway up -d --force-recreate mihomo ) &
  ```

## 回滚

```bash
cp /tmp/backup-*/docker-compose.yml /tmp/backup-*/gateway.yaml .
docker compose -p easytier-gateway up -d --force-recreate mihomo
ip -o link show | grep Meta || echo "TUN 已消失 ✓"    # 确认回滚到位 ✓
```

## ★ 镜像必须固定版本（不可用 `latest`）

家侧运行的内核**必须是我们 fork 的构建**（含 `proxy-networks` 运行期修复）。用 `latest` 会悄悄跑到旧/官方构建，
表现为"公告了网段但完全不生效"。做法（compose 已支持 `${GW_TAG:-latest}`）：

```yaml
# .env（与 docker-compose.yml 同目录）
GW_TAG=v1.19.32-fa.1020      # 固定版本 ✓ 可复现 ✓
```

**自证**（不看 tag，看二进制里的标记）：
```bash
docker inspect mihomo-gateway --format '{{.Config.Image}}'          # 期望 …:v1.19.32-fa.1020 ✓
docker exec mihomo-gateway sh -c 'strings /proc/1/exe | grep -c "forwarded dials go through dialer-proxy"'   # 期望 1 ✓
docker logs mihomo-gateway 2>&1 | grep "proxy networks announced"    # 期望出现你的网段 ✓
```
