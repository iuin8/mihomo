# 发布记录（镜像 tag ↔ 内核版本）

> 只有本 fork 的镜像/内核可用：上游内核会静默忽略 `tun: true`。
> 家侧与出口用同一镜像；升级见 `ops-sop.md`。
> 下文命令中的部署目录/容器名写的是本机示例，换机器时按实际替换。

## v1.19.32-fa.1028（内核 `1.10.0-fa.1028`，2026-10-25）

构建：run `40155504062`（repo `iuin8/mihomo`，ref `fa/trunk`，head `8a923ce1`）→
manifest `sha256:a55fc0030bf85cc96b619b4d4c6510a472e2711e3d872ce4b567a06851dfac23`。

**与 fa.1027 的 guest 差量**（wasm 由 EasyTier `88005748` 构建，sha256 `b05e6174…`）。
在 EasyTier fork 仓执行 `git log --oneline 5af90471..88005748`，共 **5 个提交**：

- `88005748` 本 fork / 上游 PR #2734：wasi 声明 smoltcp 栈 + guest 去掉 ring
- `54f2850d` #2738 test(connector): wait for reply route in foreign network direct connect
- `e307dadb` #2733 fix(connectivity): advertise shared-node running listeners
- `318a0b06` #2710 fix(gateway): make port-forward reload preserve live listeners
- `c7e636f9` #2731 fix(udp): keep native UDP bindings exclusive by default

**宿主侧（同一内核）**：

- `streamConn.Close` 先有界 `shutdown_write` 再释放 ⇒ 出口能收到 FIN/EOF
- 节点级 `log-level` 经 `Host.SetLogLevel` 透传到 guest
- wasm provenance 由 `go generate` 重算，并加三方一致断言（artifact / proto / SHA256SUMS）

**生产口径**：只按"只内网"发布（出口只公告内网网段）；全出口仍是实验特性（`#1b` 未定性），
见 `feature-matrix.md`，在内存问题定性前不用于生产。

**家侧验收（2026-10-25 11:03）**：`start failed=0` · `announced=1` · `peer_added=2` ·
`purpose=4` ×10 · `accepted for proxy` ×4
（debug 窗口内、由本侧 2 次内网请求触发的计数；不是容量/SLA 指标）。
本侧内网探针按 `local-state/probes.conf` 的期望码通过（本次 403/200）。

**发布后 soak（2026-10-25 11:05–11:11，只内网、无操作）**：`RestartCount` 恒为 0；RSS 637–677 MiB（7 次采样，无爬升）。

**升级 / 回滚（照做即可）**：

```bash
cd <家侧部署目录>            # 本机示例：/data/docker/mihomo/easytier-gateway
# 0) 备份（回滚时要连 .env 与 gateway.yaml 一起恢复，不只换 tag）
cp -p .env .env.bak-$(date +%Y%m%d-%H%M%S)
cp -p gateway.yaml gateway.yaml.bak-$(date +%Y%m%d-%H%M%S)

# 1) 升级：pull 与 up 都要带上新 tag，否则 pull 拉的是旧 tag
GW_TAG=v1.19.32-fa.1028 docker compose pull
GW_TAG=v1.19.32-fa.1028 docker compose up -d --pull always

# 2) 升级后自证
docker exec mihomo-gateway /usr/local/bin/mihomo -v        # 期望 1.10.0-fa.1028
docker logs mihomo-gateway 2>&1 | grep -c "start failed" || true   # 期望 0

# 3) 回滚（tag 与配置一起回）
GW_TAG=v1.19.32-fa.1027 docker compose up -d
#    并恢复 .env 与 gateway.yaml 备份（升级期间可能改过 log-level 等）
```

本机内核升级/回滚：

```bash
sudo -n et-apply core-upgrade    # 取最新发布 → 校验 → 备份 → 装 → 重启 → 按版本核对
sudo -n et-apply core-rollback   # 回滚到上一份内核备份
```

## v1.19.32-fa.1027（内核 `1.10.0-fa.1027`，2026-10-21）

- 首次离网验收通过（终端 404/200 + 无缓存浏览器标题）
- 出口侧证据：`syn received` / `accepted for proxy` / `purpose=4` / `expired=0`
- wasm sha256 `b542ef04…`（EasyTier **`5af90471`** + smoltcp 能力补丁）
