# 家侧（home）：公告内网 + 替别人落地出网

**它是什么**：家里那台机器上的 mihomo，两个 EasyTier 节点：
一个公告家里网段，一个连接出口；`tun` 让家侧成为真正的 L3 网关。

| 文件 | 用途 |
|---|---|
| [`gateway.example.yaml`](gateway.example.yaml) | 完整网关配置（脱敏）：两个 EasyTier 节点 + TUN + DNS + 规则 |
| [`docker-compose.example.yml`](docker-compose.example.yml) | 部署示例：`ghcr.io/iuin8/mihomo` 镜像 + host 网络 + `/dev/net/tun` |

## 部署

```bash
# 1) 复制到一个不放在 git checkout 里的目录
mkdir -p /data/docker/mihomo/easytier-gateway && cd /data/docker/mihomo/easytier-gateway
cp <repo>/docs/easytier/examples/home/gateway.example.yaml ./gateway.yaml
cp <repo>/docs/easytier/examples/home/docker-compose.example.yml ./docker-compose.yml
mkdir -p geoip

# 2) 改 gateway.yaml 的 secret / 会合点 / 家里网段 / 网卡
# 3) 校验后启动（GW_TAG 必须钉本 fork 的版本）
GW_TAG=v1.19.32-fa.1030 docker compose up -d
```

## 必须替换的值

`network-secret`、`hostname` / `instance-name`、`ipv4`、`interface-name`、
`peers`、`proxy-networks`、`exit-nodes`、中继/出口 IP、`tun-routes`。
`route-exclude-address` 里的管理面/内网/Docker 网段**不能删**，否则会掐断自己的 SSH。

## 验收

```bash
docker inspect mihomo-gateway --format 'health={{.State.Health.Status}} restarts={{.RestartCount}}'
ip -o link show | grep -c Meta                 # 宿主 TUN 数
curl -s -o /dev/null -w '%{http_code}\n' https://www.google.com/   # 宿主终端 ⇒ 200
```

会合点、私网/内网规则与 `tun-routes` 的细节见 [`../../tun-mode-spec.md`](../../tun-mode-spec.md)；
会合点部署见 [`../rendezvous/`](../rendezvous/)。
