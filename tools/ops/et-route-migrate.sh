#!/bin/bash
# Clean up stale overlay interfaces that keep a 10.144.0.0/24 route alive.
#
# Default is READ-ONLY. Nothing is deleted unless --apply is given.
# The operator must supply the mapping; the script never guesses which
# interface is stale.
#
# Map file format (one line per interface, no quotes):
#   <iface> <keep|remove> <label> [start-command...]
#
#   easytier5 remove old-fa systemctl start easytier@old-fa
#   easytier6 keep   et-lan
#
# start-command is a plain shell fragment (simple tokens only); it is written
# into rollback.sh so a verify failure can restart the overlay that owned the
# removed interface. Without it, --apply refuses unless --allow-irreversible.
#
# Usage:
#   et-route-migrate.sh --map FILE [--expect-routes N] [--allow-irreversible]
#                        [--data-plane-check CMD] [--apply]
#
# Dry-run prints the routes, the plan, the expected remaining count and a data
# plane hint (ip route get 10.144.0.2). --apply needs root, --expect-routes and
# --data-plane-check; refuses live easytier owners/unmapped devices, backs up
# routes+map, deletes the "remove" interfaces, then re-checks the route count
# immediately and after 30s and runs the data plane check after 30s. Any failed
# check runs rollback.sh (best effort) and exits non-zero; under
# --allow-irreversible it reports "needs manual intervention" and never prints
# "apply complete".
set -Eeuo pipefail
umask 022

PROG=${0##*/}
MAP=
EXPECT=
APPLY=0
ALLOW_IRREVERSIBLE=0
DATA_PLANE_CHECK=''
declare -a DATA_PLANE_CMD=()
IP_BIN=$(command -v ip || true)
[[ -n $IP_BIN ]] || IP_BIN=/sbin/ip
[[ -x $IP_BIN ]] || IP_BIN=/usr/sbin/ip
PGREP_BIN=$(command -v pgrep || true)
FUSER_BIN=$(command -v fuser || true)

die() { printf '%s: %s\n' "$PROG" "$*" >&2; exit 1; }
usage() { sed -n '2,31p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

while [[ $# -gt 0 ]]; do
  case $1 in
    --map) MAP=${2:-}; shift 2 ;;
    --expect-routes) EXPECT=${2:-}; shift 2 ;;
    --apply) APPLY=1; shift ;;
    --allow-irreversible) ALLOW_IRREVERSIBLE=1; shift ;;
    --data-plane-check) DATA_PLANE_CHECK=${2:-}; shift 2 ;;
    -h|--help) usage ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n $MAP ]] || die "--map is required (see header for the format)"
[[ -r $MAP ]] || die "cannot read map: $MAP"
if (( APPLY )); then
  [[ $EUID -eq 0 ]] || die "--apply needs root"
  [[ ${EXPECT:-} =~ ^[0-9]+$ ]] || die "--apply needs --expect-routes N"
  [[ -n $DATA_PLANE_CHECK ]] || die "--apply needs --data-plane-check CMD"
fi
if [[ -n $DATA_PLANE_CHECK ]]; then
  [[ $DATA_PLANE_CHECK =~ ^[A-Za-z0-9_./:@%?=,-]+(\ [A-Za-z0-9_./:@%?=,-]+)*$ ]] ||
    die "data-plane-check must be plain tokens (no quotes/metacharacters)"
  read -r -a DATA_PLANE_CMD <<<"$DATA_PLANE_CHECK"
fi

declare -a REMOVE_IFACES=() REMOVE_LABELS=() REMOVE_STARTS=() KEEP_IFACES=() ALL_MAPPED=()
remove_count=0
keep_count=0
while IFS= read -r line; do
  line=${line%%#*}
  read -r iface action label cmd <<<"$line"
  [[ -n ${iface:-} ]] || continue
  [[ -n ${action:-} && -n ${label:-} ]] || die "bad map line: $line"
  [[ $iface =~ ^[A-Za-z0-9_.:-]+$ ]] || die "bad interface name: $iface"
  [[ -e /sys/class/net/$iface ]] || die "interface does not exist: $iface"
  [[ $(cat "/sys/class/net/$iface/type") == 65534 ]] || die "not a tun interface: $iface"
  case $action in
    remove)
      if [[ -n ${cmd:-} && ! $cmd =~ ^[A-Za-z0-9_./@:-]+(\ [A-Za-z0-9_./@:-]+)*$ ]]; then
        die "start-command must be plain tokens (no quotes/metacharacters): $cmd"
      fi
      REMOVE_IFACES+=("$iface"); REMOVE_LABELS+=("$label"); REMOVE_STARTS+=("${cmd:-}")
      remove_count=$((remove_count + 1))
      ;;
    keep)
      KEEP_IFACES+=("$iface")
      keep_count=$((keep_count + 1))
      ;;
    *) die "action must be keep or remove: $line" ;;
  esac
  ALL_MAPPED+=("$iface")
done <"$MAP"
(( remove_count + keep_count > 0 )) || die "map is empty"

route_lines=$("$IP_BIN" -o route show table all | awk '$1 == "10.144.0.0/24"')
route_count=$(printf '%s\n' "$route_lines" | grep -c . || true)
printf 'current 10.144.0.0/24 routes: %s\n' "$route_count"
printf '%s\n' "$route_lines" | sed 's/^/  /'
printf 'data plane hint: '; "$IP_BIN" route get 10.144.0.2 2>/dev/null || echo 'no route'

# Every device carrying the prefix must be mapped, otherwise the expected
# count would silently absorb an unowned route.
for dev in $(printf '%s\n' "$route_lines" | awk '{for (i = 1; i <= NF; i++) if ($i == "dev") print $(i + 1)}'); do
  case " ${ALL_MAPPED[*]} " in
    *" $dev "*) ;;
    *) die "route device $dev is not in the map; add it as keep or remove" ;;
  esac
done
for iface in "${REMOVE_IFACES[@]}"; do
  printf '%s\n' "$route_lines" | grep -q "dev $iface\b" ||
    die "no 10.144.0.0/24 route on $iface; re-check the mapping"
done
for iface in "${KEEP_IFACES[@]}"; do
  printf '%s\n' "$route_lines" | grep -q "dev $iface\b" ||
    die "kept interface $iface has no 10.144.0.0/24 route; re-check the mapping"
done

expected=$(( route_count - remove_count ))
(( expected >= 0 )) || die "mapping removes more routes than exist"
if (( remove_count > 0 )); then
  printf 'plan: remove %s\n' "${REMOVE_IFACES[*]}"
else
  printf 'plan: remove (none)\n'
fi
printf 'expected after apply: %s route(s)\n' "$expected"
if [[ -n ${EXPECT:-} && $EXPECT -ne $expected ]]; then
  die "expected $EXPECT route(s) but mapping implies $expected"
fi

if (( ! APPLY )); then
  if [[ -n $DATA_PLANE_CHECK ]]; then
    printf 'data plane check: %s\n' "$DATA_PLANE_CHECK"
  fi
  printf 'dry-run: no changes made\n'
  if [[ -n $PGREP_BIN ]] && "$PGREP_BIN" -x easytier >/dev/null; then
    printf 'warning: easytier process is running; stop it before --apply\n' >&2
  fi
  exit 0
fi

# Anything that would recreate the interface must be gone first.
if [[ -n $PGREP_BIN ]]; then
  live=$("$PGREP_BIN" -af 'easytier(-core)?' || true)
  [[ -z $live ]] || die "easytier process is running: $live"
fi
if command -v docker >/dev/null && docker ps --format '{{.Names}}' | grep -qi easytier; then
  die "an easytier container is running; stop the mapped overlay first"
fi
for iface in "${REMOVE_IFACES[@]}"; do
  if [[ -n $FUSER_BIN ]] && "$FUSER_BIN" "/sys/class/net/$iface" >/dev/null 2>&1; then
    die "$iface still has an owner; stop that process first"
  fi
done
printf 'baseline data plane check: %s\n' "$DATA_PLANE_CHECK"
"${DATA_PLANE_CMD[@]}" ||
  die "baseline data plane check failed; nothing deleted"
if (( ! ALLOW_IRREVERSIBLE )); then
  for i in "${!REMOVE_IFACES[@]}"; do
    [[ -n ${REMOVE_STARTS[$i]:-} ]] ||
      die "no start-command for ${REMOVE_IFACES[$i]} (${REMOVE_LABELS[$i]}); add one or pass --allow-irreversible"
  done
fi

backup_dir=$(mktemp -d /var/tmp/et-route-migrate.XXXXXX)
chmod 700 "$backup_dir"
"$IP_BIN" -o route show table all >"$backup_dir/routes.before"
cp "$MAP" "$backup_dir/map"
sha256sum "$backup_dir/routes.before" >"$backup_dir/routes.before.sha256"
{
  echo '#!/bin/bash'
  echo 'set -Eeuo pipefail'
  echo 'printf "%s\n" "Rollback restarts the overlays that owned the removed interfaces."'
  if (( remove_count > 0 )); then
    for i in "${!REMOVE_IFACES[@]}"; do
      if [[ -n ${REMOVE_STARTS[$i]:-} ]]; then
        printf '%s\n' "printf 'starting %s\\n'; ${REMOVE_STARTS[$i]}" "${REMOVE_LABELS[$i]}"
      else
        printf '%s\n' "printf 'no start command recorded for %s (%s)\\n'" "${REMOVE_IFACES[$i]}" "${REMOVE_LABELS[$i]}"
      fi
    done
  fi
} >"$backup_dir/rollback.sh"
chmod 700 "$backup_dir/rollback.sh"

attempt_rollback() {
  printf 'applying rollback: %s\n' "$backup_dir/rollback.sh" >&2
  bash "$backup_dir/rollback.sh" || printf 'rollback reported an error; inspect manually\n' >&2
  if (( ALLOW_IRREVERSIBLE )); then
    printf 'irreversible mode: rollback has no start command; manual intervention required\n' >&2
  fi
}
cleanup() { printf 'backup: %s\nrollback: %s\n' "$backup_dir" "$backup_dir/rollback.sh" >&2; }
trap cleanup EXIT

if (( remove_count > 0 )); then
  for iface in "${REMOVE_IFACES[@]}"; do
    printf 'deleting %s\n' "$iface"
    if ! "$IP_BIN" link del "$iface"; then
      attempt_rollback
      die "failed to delete $iface"
    fi
  done
fi

verify() {
  local stage=$1 now
  now=$("$IP_BIN" -o route show table all | awk '$1 == "10.144.0.0/24"' | grep -c . || true)
  printf '%s: 10.144.0.0/24 routes=%s (expected %s)\n' "$stage" "$now" "$expected"
  if [[ $now -ne $expected ]]; then
    attempt_rollback
    die "$stage route count mismatch"
  fi
}
check_data_plane() {
  local stage=$1
  printf '%s data plane check: %s\n' "$stage" "$DATA_PLANE_CHECK"
  if ! "${DATA_PLANE_CMD[@]}"; then
    attempt_rollback
    die "$stage data plane check failed"
  fi
}
verify immediately
sleep 30
verify after-30s
check_data_plane after-30s
printf 'apply complete\n'
