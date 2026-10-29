# 会合点（rendezvous）：让两侧 NAT 后也能连上

**它是什么**：一台有公网 IP 的机器上一个 EasyTier 容器（私有模式 ✓）：帮家侧与出口互相发现、必要时中继 ✓。

| 文件 | 用途 |
| --- | --- |
| [`docker-compose.yml`](docker-compose.yml) | 一条命令起会合点（私有模式 + 端口 + `ulimit` + 日志轮转 ✓） |

## 部署

```bash
scp docker-compose.yml root@<你的云服务器>:/root/easytier-rendezvous/
ssh root@<你的云服务器> 'cd /root/easytier-rendezvous && docker compose up -d'
```

* 云安全组 / 防火墙放行 **11010 的 tcp 与 udp** ✓（udp 用于 P2P 探测与中继 ✓）；
* 改 compose 里的 `--network-secret`：**必须与家侧、出口完全一致** ✓，且建议用强随机值 ✓。

## 为什么用私有模式

公共共享节点会接受**任意网络**的连接 ✗（官方文档专门教怎么配 fail2ban 挡滥用 ✗）；
私有模式只允许**同名 + 同密钥**的网络 ✓ ⇒ 不需要 fail2ban ✓，中继流量也只是你自己的 ✓。

## 判据

```bash
# 容器在跑 + 端口在听
docker logs easytier-rendezvous | tail -5
ss -lntup | grep 11010                       # ⇒ tcp 与 udp 都有 ✓
# 端到端：两侧都起来后，在出口侧对家侧的内网地址做一次 TCP 探测 ⇒ 通 ✓
```
