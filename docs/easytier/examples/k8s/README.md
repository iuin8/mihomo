# Kubernetes 网关（et-k8s）

**它是什么**：在集群内以 `hostNetwork` 运行的 EasyTier 网关。它建 TUN、把
Service/Pod 网段公告给 overlay，外部客户端通过 `et-k8s` 访问集群服务（当前环境的实际节点名按你的配置替换）。

| 文件 | 用途 |
|---|---|
| [`config.example.yaml`](config.example.yaml) | 完整脱敏的 mihomo 配置（网关节点） |
| [`deployment.example.yaml`](deployment.example.yaml) | PVC + Deployment（hostNetwork/TUN/probe） |
| [`kustomization.yaml`](kustomization.yaml) | 把 config 生成 ConfigMap，一并 apply |

## 使用

```bash
# 1) 编辑 config.example.yaml：NET_SECRET / overlay IP / rendezvous
#    以及 proxy-networks、route-exclude 里的集群 CIDR
# 2) 按集群实际修改 deployment.example.yaml 的 StorageClass / 网卡 / 镜像 tag
# 3) 一次 apply
kubectl apply -k docs/easytier/examples/k8s
```

或先渲染查看：

```bash
kubectl kustomize docs/easytier/examples/k8s
```

## 必须核对

- `hostNetwork: true` + `/dev/net/tun` + `NET_ADMIN`：缺一不可；
- `tun.route-exclude-address` 必须包含集群/宿主私网，否则会把集群流量截进 TUN；
- `proxy-networks` 写**集群 Service/Pod 网段**（示例 `10.68.0.0/16` / `172.20.0.0/16`）；
- `external-controller: 127.0.0.1:9095` 是 probe 的依据；
- `state-dir` 必须落在 PVC 挂载点下，否则 Pod 重建会丢节点标识。

## 验收

```bash
kubectl -n default get pods -l app=easytier-gateway
kubectl -n default exec deploy/easytier-gateway -- wget -qO- http://127.0.0.1:9095/version
# 外部客户端：规则 DOMAIN-SUFFIX,cluster.local → et-k8s，再访问集群服务
```
