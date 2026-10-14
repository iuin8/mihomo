# 出口（exit）：把家侧的包用自己的网络送出去

**它是什么**：一台"网络好"的机器上的 mihomo（本方案里是 Mac），跑一个 easytier 节点：
① 让家侧能访问它所在网段 ✓　② **替家侧落地出网** ✓。

| 文件 | 用途 |
| --- | --- |
| [`mihomo-profile.yaml`](mihomo-profile.yaml) | 要并入你 Clash 配置的片段（节点 + DNS 策略 + 规则 ✓） |

## 前置

* **本 fork 构建的内核** ✓ —— 上游内核会**静默忽略** `tun: true`（配置校验还照样通过 ✗），
  且 macOS 服务模式下还需要 fork 的解释器补丁，否则内核会被 `CODESIGNING` SIGKILL ✗
  ⇒ 详见 [`../service-mode-sigkill.md`](../service-mode-sigkill.md) ✓；
* 与家侧一致的 `network-name` / `network-secret` ✓；
* 一个**可用的代理组**（出口替家侧拨号时要经它出去 ✓）。

## 怎么并入（二选一 ✓，都不需要改订阅本身 ✓）

* **推荐**：CVR 的「扩展配置 / **Merge**」项 —— 用 `prepend-proxies` / `append-rules`
  （这样它可以**叠加在任意订阅上** ✓，也方便分享给第二台机器 ✓）；
* 或者：直接编辑自己的 profile（`profiles/<uid>.yaml`）✓。

本机（macOS 服务模式）改完若要生效：**改 profile 不会通知 CVR** ✗ ⇒ 在 GUI 里点一次
「重载配置」/ 重启内核 ✓。

## 两条硬性要求（少一条就"看着通、其实没通"✗）

1. **`dialer-proxy` 必须写** ✓ —— 出口替家侧拨号走的是 mihomo 的 **dialer**（不是规则引擎 ✗），
   不写 = 裸直连 ⇒ **国内通 ✓、国外一律 503/504 ✗**；
2. **`dns.nameserver-policy` 必须经你的代理组** ✓ —— 否则替家侧解析国外域名会被国内 DNS 投毒 ✗
   （实测被解析成 `69.171.235.22` ✗）。注意 `#名字` 的含义是"经这个**出站**去解析" ✓，
   出口侧要写**自己的代理组** ✓（写成 `#et-fa` 就绕回 overlay 自己了 ✗）。

## 判据

```bash
# ① 出口实例活着、这两个键真的生效了（用运行产物自证 ✓，不要看配置文件 ✗）
curl -s --unix-socket <内核 socket> http://localhost/proxies/et-fa | grep -o '"dialer-proxy":"[^"]*"'
# ② 本机自己的网照旧（不能有回归 ✓）
curl -s -o /dev/null -w '%{http_code}\n' https://www.google.com/       # ⇒ 200 ✓
# ③ 替对端拨号那条连接**真的走了代理**（本 fork 会在日志里给字节数 ✓）
#    期望形如：forwarded conn purpose=4 -> 142.251.x.x:443: wrote=… read=…  ⇒ read>0 ✓
#    read=0 ⇒ 回程没回来（问题在出口上游 ✗）；连一行都没有 ⇒ 没走到这条路径 ✗
# ④ 家侧那边：对该节点做延迟测试，url 用 https://www.google.com/ ⇒ 有毫秒数 ✓
```

## 回滚

改回原来的 profile/合并项 ⇒ GUI 里重载配置 ✓。内核回滚见 `../service-mode-sigkill.md` ✓。
