# 客户端「内网接入」订阅(模板化 ✓ 可分享 ✓)

> 目标:**导入一份配置**就能 ① 现场用公司内网服务 ② 离网用家侧/公司内网 ③(可选)借出口上网 ——
> 终端与浏览器**都自动**(靠 TUN ✓,不依赖系统代理 ✗)。

## 三步

1. **填值**:`cp mihomo-profile.template.yaml my-profile.yaml`,把 `<...>` 全部替换(或见下"一键渲染")。
2. **导入**:CVR → 订阅/配置 → 导入**本地文件**(或把这个文件放到你自己的静态托管,作为**订阅 URL** 分享 ✓)。
3. **切换**:代理分组 `<内网分组名>` ⇒ 现场选 `DIRECT` ✓;离网选入口节点 ✓。

## 一键渲染(推荐 ✓ 渲染器会**拒绝**未替换的占位符)

```bash
LAN_NODE=et-home NET_NAME=<网络名> NET_SECRET=<secret> RENDEZVOUS=<会合点地址> \
PHYS_NIC=<物理网卡名，如 en0> EXIT_ADDR=<家侧网关 overlay 地址> GROUP=内网入口 OVERLAY=10.144.0.0/24 \
HOME_CIDR=192.168.1.0/24 CORP_CIDR=10.0.0.0/8 CORP_DOMAIN=<内网域名> \
CLUSTER_DOMAIN=cluster.local DNS=<内网DNS> sh render.sh > my-profile.yaml
```

## 完整独立版（无机场也能直接导入）

短模板 `mihomo-profile.template.yaml` 适合**已有机场订阅**的人做内网增量；如果对方没有机场，
或需要一份能独立跑的最小完整配置，用这两个文件/公开 Gist：

- Gist: <https://gist.github.com/iuin8/e3c15017e69138b6ad5d99f605ccba44>
- 本仓库：`mihomo-profile.complete.template.yaml`（占位符）与
  `mihomo-profile.complete.example.yaml`（全脱敏示例）

```bash
# 完整版模板渲染（示例值都是假的，按真实拓扑替换）
TEMPLATE=docs/easytier/client/mihomo-profile.complete.template.yaml \
LAN_NODE=et-home NET_NAME=example-overlay NET_SECRET=CHANGE_ME \
RENDEZVOUS=10.200.0.10 EXIT_ADDR=10.200.0.2 PHYS_NIC=en0 GROUP=内网入口 \
OVERLAY=10.200.0.0/24 HOME_CIDR=192.168.1.0/24 CORP_CIDR=10.0.0.0/8 \
CORP_DOMAIN=corp.example.com CLUSTER_DOMAIN=cluster.local DNS=10.96.0.10 \
sh docs/easytier/client/render.sh > my-profile.yaml
```

完整版直接包含 `mixed-port`、TUN、DNS、proxies、groups、rules；不需要依赖机场。
`NET_SECRET` 仍是 overlay 入网资格，对外分享前必须另建网络/secret，不能直接把真实值给对方。

## 交付前自检(必须全过 ✓)

```bash
sh ../check-templates.sh                 # 本仓库模板里不得出现真实 secret/公网 IP 等 ✗
python3 -c "import yaml,sys;yaml.safe_load(open('my-profile.yaml'))"   # YAML 合法 ✓
sh render.sh >/dev/null && echo ok || echo '缺变量 ✗'                   # 渲染器生效 ✓
```

## 分享纪律(重要 ✗)

* 模板里含 **overlay 入网资格**(`network-secret`)⇒ **分享 = 让对方能进你的 overlay** ✗;
* 对外分享建议:**单独一套 overlay + 单独 secret** ✓,并在对方知情的前提下给 ✓;
* 模板里**不要**写死自己的真实 IP/域名/主机名 ✗ —— 一律用 `<...>` 占位符 ✓,渲染在本地做 ✓。
