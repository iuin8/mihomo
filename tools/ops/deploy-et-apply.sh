#!/bin/bash
# 部署 root 助手 et-apply（含 #3 的 kickstart-first 修复）并做验收。
# 用法：sudo tools/ops/deploy-et-apply.sh
#
# 设计原则（按复核意见）：
#  · 只有"新脚本本身"出问题才回滚（sha 不符 / bash -n 失败 / restart 失败 / 就绪超时）；
#  · 回滚/重启后必须把**所有 select 组**恢复成部署前快照（内核重启会把它们全部打回默认）；
#  · 性能（≤2s）、utun 集合、数据面诊断 只断言，不回滚；未达项计入 rc；
#  · 内核本来健康时（sha/bash -n 阶段）回滚只还原文件，不白重启。
set -uo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/et-apply"
DST="/usr/local/libexec/clash-group/et-apply"
LABEL="io.github.clash-verge-rev.clash-verge-rev.service"
SOCK="${CLASH_GROUP_SOCK:-/var/run/clash-verge-service/users/$(id -u)/verge-mihomo.sock}"
ET_NET_MODE="${ET_NET_MODE:-$(cd "$(dirname "$0")" && pwd)/et-net-mode}"
INSTALL="/usr/bin/install"
BACKUP_KEEP=5
TS="$(date +%Y%m%d-%H%M%S)"

die() { echo "⛔ $*" >&2; rm -f "${selector_snapshot:-}"; exit 1; }
[[ $EUID -eq 0 ]] || die "需要 root：sudo $0"
[[ -f "$SRC" ]] || die "源不存在：$SRC"

utun_set() { ifconfig -l | tr ' ' '\n' | grep '^utun' | sort | tr '\n' ' ' || true; }
api_code() { curl -s -o /dev/null -w '%{http_code}' --max-time 2 --unix-socket "$SOCK" "http://localhost$1" 2>/dev/null || true; }
api_get()  { curl -s --max-time 3 --unix-socket "$SOCK" "http://localhost$1" 2>/dev/null || true; }
api_put()  { curl -s -o /dev/null -w '%{http_code}' --max-time 3 -X PUT -H 'Content-Type: application/json' \
              --unix-socket "$SOCK" -d "$2" "http://localhost$1" 2>/dev/null || true; }
urlq() { python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1]))' "$1"; }

wait_ready() {  # 到 API200 + utun1024，最多 $1 秒
  local deadline=$(( $(date +%s) + ${1:-25} ))
  while [[ $(date +%s) -lt $deadline ]]; do
    [[ "$(api_code /version)" == "200" ]] && ifconfig -l | tr ' ' '\n' | grep -qx utun1024 && return 0
    sleep 0.2
  done
  return 1
}

# ---- select 组快照 / 恢复（内核重启会把 13 个 select 组全部打回默认）----
snapshot_selectors() {
  api_get /proxies | python3 -c '
import json,sys
try: d=json.load(sys.stdin).get("proxies",{})
except Exception: sys.exit(0)
for k,v in d.items():
    if str(v.get("type","")).lower() in ("selector","select") and v.get("now"):
        print(k+"\t"+v["now"])
'
}

selector_now() {
  api_get /proxies | python3 -c '
import json,sys
try: d=json.load(sys.stdin).get("proxies",{})
except Exception: print(""); sys.exit(0)
print((d.get(sys.argv[1],{}) or {}).get("now",""))' "$1"
}

restore_selectors() {   # $1=快照文件；逐项 PUT 回快照并断言（3×0.2s 重试）
  local f="$1" name now cur code fails=()
  [[ -s "$f" ]] || { echo "  （无 select 组快照，跳过恢复）"; return 0; }
  while IFS=$'\t' read -r name now; do
    [[ -z "$name" || -z "$now" ]] && continue
    code="$(api_put "/proxies/$(urlq "$name")" "$(python3 -c 'import json,sys;print(json.dumps({"name":sys.argv[1]}))' "$now")")"
    cur=""
    for _ in 1 2 3; do sleep 0.2; cur="$(selector_now "$name")"; [[ "$cur" == "$now" ]] && break; done
    if [[ "$cur" == "$now" ]]; then
      echo "  ✓ $name = $now"
    else
      echo "  ✗ ${name}：期望 ${now}，实际 ${cur:-空}（PUT=${code:-无}）" >&2
      fails+=("$name")
    fi
  done < "$f"
  ((${#fails[@]})) || return 0
  echo "⛔ select 组恢复失败：${fails[*]}" >&2
  return 1
}

bak=""; installed=0; completed=0; restarted=0; selector_snapshot="$(mktemp /tmp/et-selectors.XXXXXX)"

restore() {   # 还原文件；内核本来健康则不重启；需要重启时按快照恢复 select 组
  if [[ $installed -eq 0 ]]; then echo "（尚未安装，无需回滚）"; return 0; fi
  [[ -n "$bak" && -f "$bak" ]] || { echo "⛔ 首次安装失败，无旧版可回滚，需人工" >&2; return 1; }
  cp -p "$bak" "$DST" || return 1
  kicked=0
  if wait_ready 2; then
    echo "已还原旧版（内核仍健康，未重启）"
  else
    kicked=1
    launchctl kickstart -k "system/$LABEL" >/dev/null 2>&1 || true
    if wait_ready 30; then echo "已还原旧版并恢复服务"; else echo "⛔ 回滚后仍不可用，需人工：launchctl print system/$LABEL" >&2; fi
  fi
  # 只有真正重启过内核才会重置 select 组；用快照恢复并断言
  if [[ $kicked -eq 1 || $restarted -eq 1 ]]; then
    if [[ -s "$selector_snapshot" ]]; then
      echo "恢复 select 组（快照 $(wc -l < "$selector_snapshot") 项）："
      restore_selectors "$selector_snapshot" || echo "⛔ 回滚后分组恢复失败，需人工" >&2
    else
      echo "⛔ 无 select 组快照，回滚未恢复分组（需人工核对）" >&2
    fi
  else
    echo "本次部署未重启内核，select 组无需恢复"
  fi
}

trap 'if [[ $completed -eq 0 ]]; then echo "中断 ⇒ 回滚" >&2; restore; fi; rm -f "$selector_snapshot"; exit 130' INT TERM

# 0) 边界：服务必须已加载
launchctl print "system/$LABEL" >/dev/null 2>&1 || die "服务未加载（system/${LABEL}）⇒ 先 launchctl bootstrap，再重跑"

# 1) 基线：utun 集合 + 全部 select 组快照
baseline_utun="$(utun_set)"
baseline_count=$(echo "$baseline_utun" | wc -w | tr -d ' ')
snapshot_selectors > "$selector_snapshot"
[[ -s "$selector_snapshot" ]] || die "无法快照 select 组（API/解析异常）⇒ 中止部署，避免重启后无法恢复"
grep -q $'^内网入口\t' "$selector_snapshot" || echo "⚠️ 快照里没有已选中的 内网入口（当前未选中，属正常）"
echo "基线：utun=${baseline_count} [$baseline_utun] · select 组快照 $(wc -l < "$selector_snapshot") 项"
echo "  内网入口.now = $(selector_now "内网入口")"

# 2) 备份 + 指纹
if [[ -f "$DST" ]]; then
  bak="$DST.bak-$TS"; cp -p "$DST" "$bak" || die "备份失败"
  echo "备份：${bak}（旧 sha256=$(shasum -a 256 "$DST" | awk '{print $1}')）"
  old_backups=(); while IFS= read -r f; do old_backups+=("$f"); done < <(ls -1t "$DST".bak-* 2>/dev/null | tail -n +$((BACKUP_KEEP+1)))
  ((${#old_backups[@]})) && rm -f "${old_backups[@]}"
else
  echo "首次安装（无旧文件可备份）"
fi

# 3) 整份安装 + sha + 语法（失败均回滚）
installed=1   # 先置位：BSD install 可能先截断再写，失败也要能用备份恢复
"$INSTALL" -m 755 -o root -g wheel "$SRC" "$DST" || { restore; die "install 失败"; }
new_sha="$(shasum -a 256 "$DST" | awk '{print $1}')"
src_sha="$(shasum -a 256 "$SRC" | awk '{print $1}')"
[[ "$new_sha" == "$src_sha" ]] || { restore; die "安装后 sha256 与源不一致"; }
echo "已安装（sha256=${new_sha}）"
bash -n "$DST" || { restore; die "bash -n 失败"; }

# 4) 验收：restart ×3 计时；restart 失败/就绪超时 ⇒ 回滚；性能/utun 只断言
perf_fail=0; utun_fail=0
# 每次 restart 之间留 12s：launchd 对同一 job 的连续重启有 ~10s 节流，
# 背靠背重启测到的是节流而不是"重启有多快"（实测背靠背 1.68/10.36/10.38s，
# 间隔 12s 时 1.36/1.31/1.34s）；生产用法本来就是一次重启。
for i in 1 2 3; do
  [ "$i" -gt 1 ] && sleep 12
  restarted=1   # 放在尝试之前：即使本次失败，也要保证回滚路径恢复 select 组
  t0=$(python3 -c 'import time;print(time.time())')
  if ! "$DST" restart; then restore; die "第 $i 次 restart 失败"; fi
  if wait_ready 25; then
    t=$(python3 -c 'import time;print(round(time.time()-'"$t0"',2))')
    echo "第 $i 次：API200+utun1024 = ${t}s"
    python3 -c "import sys; sys.exit(0 if $t <= 2 else 1)" || perf_fail=1
  else
    restore; die "第 $i 次就绪超时（25s）"
  fi
  cur="$(utun_set)"
  echo "   utun=$(echo "$cur" | wc -w | tr -d ' ') [$cur]"
  [[ "$cur" == "$baseline_utun" ]] || utun_fail=1
done

# 5) 成功路径：把 select 组恢复成**部署前快照**（不硬编码 et-lan）
entry_fail=0
echo "恢复 select 组："
restore_selectors "$selector_snapshot" || entry_fail=1

# 6) 诊断（捕获输出，避免泄露内网目标；失败计入 rc）
diag_fail=0
if [[ -x "$ET_NET_MODE" ]] && "$ET_NET_MODE" check easytier >/dev/null 2>&1; then
  echo "数据面诊断（check easytier）：✓"
else
  echo "数据面诊断（check easytier）：✗（细节：手动跑 $ET_NET_MODE check easytier）"
  diag_fail=1
fi

completed=1
rc=0
[[ $perf_fail -eq 1 ]] && { echo "⚠️ 有 restart 耗时 > 2s（保留新脚本，供人工判断）"; rc=1; }
[[ $utun_fail -eq 1 ]] && { echo "⚠️ utun 集合与基线不一致（保留新脚本，供人工判断）"; rc=1; }
[[ $entry_fail -eq 1 ]] && { echo "⚠️ select 组未能全部恢复（保留新脚本，供人工判断）"; rc=1; }
[[ $diag_fail -eq 1 ]] && { echo "⚠️ 数据面诊断未通过（保留新脚本，供人工判断）"; rc=1; }
echo "——"
echo "内网入口.now = $(selector_now "内网入口")"
if [[ $rc -eq 0 ]]; then echo "部署完成（验收全过）"; else echo "部署结束（有未达项，见上）"; fi
[[ -n "$bak" ]] && echo "回滚命令：cp -p '$bak' '$DST' && launchctl kickstart -k system/$LABEL"
rm -f "$selector_snapshot"
exit $rc
