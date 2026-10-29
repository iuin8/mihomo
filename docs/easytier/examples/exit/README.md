# 出口（exit）：把家侧的包用自己的网络送出去

**它是什么**：一台网络好的机器上的 mihomo，跑一个 EasyTier 出口节点：
① 让家侧访问它所在网段；② 替家侧落地出网。

| 文件 | 用途 |
|---|---|
| [`complete.example.yaml`](complete.example.yaml) | 完整独立配置（脱敏）：TUN、出口节点、DNS、规则 |

## 必须替换的值

- `network-name` / `network-secret`：与家侧、客户端一致；
- `ipv4`：本机在 overlay 里的唯一地址（带前缀）；
- `proxy-networks`：要公告给家侧的**本机所在网段**，不能与家侧/集群/overlay 重叠；
- `interface-name`：持有本机 LAN 地址、且不是 `tun/utun/easytier*` 的物理网卡；
- `peers`：会合点；
- `dialer-proxy`：出口替对端拨号所走的**本机代理组**（示例里是 `exit-proxy`，需替换成你的机场组/节点）。

## 两条硬性要求

1. **`dialer-proxy` 必须指向可用代理组**：不写/只写 `DIRECT` 就是裸直连，国内通、国外 503/504；
2. **`dns.nameserver-policy` 必须经代理组**：否则替对端解析国外域名会被投毒。

## 验收

```bash
curl -s --unix-socket <内核 socket> http://localhost/proxies/et-fa | grep -o '"dialer-proxy":"[^"]*"'
curl -s -o /dev/null -w '%{http_code}\n' https://www.google.com/       # 本机自身 ⇒ 200
# 家侧对 et-fa 用 https://www.google.com/ 做延迟测试 ⇒ 有毫秒数
```

日志应出现 `forwarded conn purpose=4 ... wrote=... read=...` 且 `read>0`。

## 回滚

改回原 profile / Merge 项并重载配置；内核问题见 [`../../service-mode-sigkill.md`](../../service-mode-sigkill.md)。
