#!/usr/bin/env bash
# FORK: 从模块缓存重新生成 third_party/easytier-go 并套用解释器补丁。
#
# 背景：easytier-go 内部用 wazero 的编译器运行时（JIT）创建 WASI runtime，
# macOS 服务模式给内核重签名（hardened runtime，无 allow-jit）后，JIT 页会被判非法页 → 内核被 SIGKILL。
# 本脚本把那一行换成解释器运行时。详见 third_party/README.md。
set -euo pipefail

cd "$(dirname "$0")/.."

module=github.com/easytier/easytier/easytier-go
dest=third_party/easytier-go
artifact_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/easytier-go-artifact"
proto_names=(annotations.proto common.proto error.proto acl.proto peer_rpc.proto api_instance.proto api_config.proto api_manage.proto web.proto)

fail() {
  echo "$*" >&2
  exit 1
}

# 1) 先做所有只读前置检查：失败时绝不碰 $dest。
version=$(awk -v m="$module" '$1 == m { print $2; exit }' go.mod)
[[ -n "$version" ]] || fail "无法在 go.mod 里找到 $module 的版本"

src="$(go env GOMODCACHE)/$module@$version"
[[ -d "$src" ]] || fail "模块缓存里没有 $module@$version（先执行：go mod download $module@$version）"

[[ -d "$artifact_dir" ]] || fail "缺少 fork artifact 目录：${artifact_dir}（不能用上游未固定产物静默代替）"
for required in easytier_core.wasm SHA256SUMS provenance.patch; do
  [[ -f "$artifact_dir/$required" ]] || fail "缺少 fork artifact 文件：$artifact_dir/$required"
done
for name in "${proto_names[@]}"; do
  [[ -f "$artifact_dir/proto/$name" ]] || fail "缺少 proto 快照：$artifact_dir/proto/$name"
done

# 2) 在临时目录构建；所有检查通过后才替换 $dest，失败不影响工作树。
mkdir -p third_party
build="$(mktemp -d "${dest}.tmp.XXXXXX")"
old="${dest}.old.$$"
swap_started=0
cleanup() {
  if [[ "$swap_started" -eq 1 && -e "$old" && ! -e "$dest" ]]; then
    mv "$old" "$dest"
  fi
  rm -rf "$build" "$old"
}
trap cleanup EXIT

cp -R "$src/." "$build/"
chmod -R u+w "$build"
rm -rf "$build/testdata"   # 仅该依赖自身的测试用，体积 9MB

# fork 对 easytier-go 的全部改动都放在这些补丁里；
# 任何一处忘了写进补丁，下次重建依赖就会被静默冲掉。
shopt -s nullglob
patch_files=(third_party/easytier-go-*.patch)
shopt -u nullglob
[[ ${#patch_files[@]} -gt 0 ]] || fail "third_party/easytier-go-*.patch 一个都没有 —— 拒绝重建（会把 fork 改动冲掉）"
for patch_file in "${patch_files[@]}"; do
  echo "套用 $(basename "$patch_file")"
  # --no-backup-if-mismatch: 多个补丁可能碰同一文件，别在依赖树里留 .orig
  patch -p1 --no-backup-if-mismatch -d "$build" < "$patch_file"
done

# 断言补丁真的落地了，避免静默退化成 JIT
grep -q 'NewRuntimeConfigInterpreter' "$build/internal/engine/host.go" || fail "补丁未生效：$build/internal/engine/host.go 里没有解释器运行时"
grep -q 'wazero.NewRuntime(' "$build/internal/engine/host.go" && fail "仍有 wazero.NewRuntime( 直调（JIT），补丁不完整"

# 3) 安装 fork-owned artifact，并做完整门禁。
cp -p "$artifact_dir/easytier_core.wasm" "$build/internal/artifact/easytier_core.wasm"
patch -p1 -d "$build" < "$artifact_dir/provenance.patch"
expected_sha="$(awk 'NR==1{print $1}' "$artifact_dir/SHA256SUMS")"
expected_commit="$(sed -n 's/^commit=//p' "$artifact_dir/SHA256SUMS")"
expected_schema="$(sed -n 's/^schema=//p' "$artifact_dir/SHA256SUMS")"
actual_sha="$(shasum -a 256 "$build/internal/artifact/easytier_core.wasm" | awk '{print $1}')"
declared_sha="$(grep -oE 'SHA256 = "[0-9a-f]{64}"' "$build/internal/artifact/provenance.go" | grep -oE '[0-9a-f]{64}')"
art_commit="$(grep -oE 'Commit = "[0-9a-f]{40}"' "$build/internal/artifact/provenance.go" | grep -oE '[0-9a-f]{40}')"
proto_commit="$(grep -oE 'EasyTierCommit = "[0-9a-f]{40}"' "$build/proto/provenance.go" | grep -oE '[0-9a-f]{40}')"
schema="$(grep -oE 'SchemaSHA256   = "[0-9a-f]{64}"' "$build/proto/provenance.go" | grep -oE '[0-9a-f]{64}')"
pin_commit="${version##*-}"
proto_actual="$(for name in "${proto_names[@]}"; do
  printf '%s\0' "$name"
  cat "$artifact_dir/proto/$name"
  printf '\0'
done | shasum -a 256 | awk '{print $1}')"

[[ -n "$expected_sha" && "$actual_sha" == "$expected_sha" && "$declared_sha" == "$expected_sha" ]] || \
  fail "artifact sha 不一致：wasm=$actual_sha provenance=$declared_sha expected=$expected_sha"
[[ -n "$expected_commit" && "$art_commit" == "$expected_commit" && "$proto_commit" == "$expected_commit" ]] || \
  fail "artifact commit 不一致：art=$art_commit proto=$proto_commit expected=$expected_commit"
[[ "$expected_commit" == "$pin_commit"* ]] || \
  fail "artifact commit 与 pin 脱钩：pin=$pin_commit artifact=$expected_commit"
[[ -n "$expected_schema" && "$schema" == "$expected_schema" && "$proto_actual" == "$expected_schema" ]] || \
  fail "proto schema 不一致：proto=$schema computed=$proto_actual expected=$expected_schema"
grep -aq 'ring_core_' "$build/internal/artifact/easytier_core.wasm" && \
  fail "artifact 含 ring 导入（WASI 宿主无法提供）"
for symbol in easytier_data_plane_tcp_connect_submit easytier_data_plane_tcp_accept_submit \
              easytier_data_plane_tcp_shutdown_write_submit easytier_data_plane_udp_bind_submit; do
  grep -aq "$symbol" "$build/internal/artifact/easytier_core.wasm" || \
    fail "artifact 缺少关键 ABI：$symbol"
done

# 4) 全部通过后才替换；失败 trap 恢复旧树。
swap_started=1
rm -rf "$old"
if [[ -e "$dest" ]]; then
  mv "$dest" "$old"
fi
if ! mv "$build" "$dest"; then
  [[ -e "$old" ]] && mv "$old" "$dest"
  fail "无法替换 $dest（旧树已恢复）"
fi
rm -rf "$old"
trap - EXIT

echo "✅ artifact 已固定：commit=$expected_commit sha=$expected_sha schema=$expected_schema"
echo "✅ $module@$version 已换为解释器运行时 → $dest"
