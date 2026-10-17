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
EXIT_ADDR=<家侧网关 overlay 地址> GROUP=内网入口 OVERLAY=10.144.0.0/24 \
HOME_CIDR=192.168.1.0/24 CORP_CIDR=10.0.0.0/8 CORP_DOMAIN=<内网域名> \
CLUSTER_DOMAIN=cluster.local DNS=<内网DNS> sh render.sh > my-profile.yaml
```

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
