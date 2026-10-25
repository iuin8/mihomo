# tools/ops —— 本项目的运维工具（fork 专有，已纳入版本管理）

这些工具**不是** mihomo 上游代码，放在 fork 仓库只为可审计/可回滚；它们与内核版本强耦合。

## 组成

| 文件 | 角色 | 运行身份 |
| --- | --- | --- |
| `et-apply` | 唯一 root 助手：`stage/restart/status/rollback/core-upgrade/core-rollback` | root（经 sudoers） |
| `et-config` | overlay → 候选配置；apply 后整轮复验与自动回滚 | 用户 |
| `et-net-mode` | 网络模式切换 + 三步订阅自检 | 用户 |
| `install-helpers.sh` | 安装 `et-apply` + 生成 `/etc/clash-group/et-apply.conf` 与 sudoers + 链接用户工具 | 用户（dry-run）/ root（`--apply`） |
| `install-core.sh` | 把本 fork 内核装进 Clash Verge 服务侧（默认）/App 侧（`--app`），含自证与备份 | root（`--apply`） |
| `deploy-et-apply.sh` | 部署助手并做验收（快照/恢复 13 组、restart 计时） | root |

## 配置来源（不使用任何个人路径）

- `et-apply`：`CLASH_GROUP_CONFIG_SRC` → `/etc/clash-group/et-apply.conf`（`CLASH_GROUP_STATE_DIR` / `CLASH_GROUP_UID`）→ `SUDO_USER` 的 home；三者都没有则**明确失败**（不猜）。
- `et-config` / `et-net-mode`：`CLASH_GROUP_ROOT`（默认从脚本位置推导项目根）、`ET_PROBES`、`CLASH_GROUP_SOCK`；`users/<uid>` 由 `id -u` 得出。
- 凭据/私有状态**不进仓库**：`local-state/`（probes/denylist/home-egress cred）、overlay、sudoers 都由安装器在本机生成（0600/0440）。

## 安装 / 升级 / 回滚

```bash
# 1) root 助手 + sudoers + 用户工具（先 dry-run 看计划）
tools/ops/install-helpers.sh            # dry-run
sudo tools/ops/install-helpers.sh --apply
# 2) 内核（先 dry-run；默认只写服务侧）
tools/ops/install-core.sh /path/to/verge-mihomo --apply   # 需要 sudo
# 3) 配置
et-config build && sudo -n et-apply stage && sudo -n et-apply restart
# 回滚
sudo -n et-apply rollback            # 配置
sudo -n et-apply core-rollback       # 内核
```

## 安全边界

- sudoers 是**单条裸规则**，安全性由 `et-apply` 的 `$1` 白名单保证：只认六个固定子命令、忽略其余参数、路径/URL 写死、未知参数 `exit 64`。
- `--app` 会覆盖 App bundle 内可执行文件，**破坏整包签名**；默认只装服务侧。
- 所有脚本 `bash -n`/`py_compile` 通过后才会被安装器拷入系统路径。
