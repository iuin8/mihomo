# 本机可直接用的 K8s 配置（不入库）

`easytier-gateway.local.yaml` 是当前环境**带真实 overlay secret/CIDR** 的单文件
ConfigMap + PVC + Deployment，已通过 `kubectl apply --dry-run=client -f` 和
`mihomo -t` 校验。

该文件被本地 `.git/info/exclude` 忽略，**永远不要提交**。

```bash
kubectl apply -f k8s/easytier-gateway.local.yaml
# 改配置后重新 apply；ConfigMap 更新需要 rollout restart
kubectl -n default rollout restart deploy/easytier-gateway
```

通用脱敏版本在：

```text
docs/easytier/examples/k8s/
```

如果换集群，优先复制脱敏版本并替换真实值；不要把 `*.local.yaml` 暴露到公开渠道。
