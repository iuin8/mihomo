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

# replace 生效时 `go list -m` 拿不到版本，改从 require 行取（升级版本时改的是它）
version=$(awk -v m="$module" '$1 == m { print $2; exit }' go.mod)
if [ -z "$version" ]; then
  echo "无法在 go.mod 里找到 $module 的版本" >&2
  exit 1
fi

src=$(go env GOMODCACHE)/$module@$version
if [ ! -d "$src" ]; then
  echo "模块缓存里没有 $module@$version" >&2
  echo "先执行：go mod download $module@$version" >&2
  exit 1
fi

rm -rf "$dest"
mkdir -p third_party
cp -R "$src" "$dest"
chmod -R u+w "$dest"
rm -rf "$dest/testdata"   # 仅该依赖自身的测试用，体积 9MB

# fork 对 easytier-go 的全部改动都放在这些补丁里；
# 任何一处忘了写进补丁，下次重建依赖就会被静默冲掉。
shopt -s nullglob
patch_files=(third_party/easytier-go-*.patch)
shopt -u nullglob
if [ ${#patch_files[@]} -eq 0 ]; then
  echo "third_party/easytier-go-*.patch 一个都没有 —— 拒绝重建（会把 fork 改动冲掉）" >&2
  exit 1
fi
for patch_file in "${patch_files[@]}"; do
  echo "套用 $(basename "$patch_file")"
  # --no-backup-if-mismatch: 多个补丁可能碰同一文件，别在依赖树里留 .orig
  patch -p1 --no-backup-if-mismatch -d "$dest" < "$patch_file"
done

# 断言补丁真的落地了，避免静默退化成 JIT
grep -q 'NewRuntimeConfigInterpreter' "$dest/internal/engine/host.go" || {
  echo "补丁未生效：$dest/internal/engine/host.go 里没有解释器运行时" >&2
  exit 1
}
grep -q 'wazero.NewRuntime(' "$dest/internal/engine/host.go" && {
  echo "仍有 wazero.NewRuntime( 直调（JIT），补丁不完整" >&2
  exit 1
}

echo "✅ $module@$version 已换为解释器运行时 → $dest"

# FORK(easytier-tun): 用我们按官方脚本重编的 guest wasm 覆盖模块里自带的产物 ✓。
# 原因:官方 wasm 未声明 smoltcp_available ⇒ 子网代理退回 kernel-listener ⇒ WASI 无 TUN ⇒ SYN 只能过期 ✗
VENDORED_WASM="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/easytier-go-wasi/easytier_core.wasm"
if [[ -f "$VENDORED_WASM" ]]; then
    cp -p "$VENDORED_WASM" third_party/easytier-go/internal/artifact/easytier_core.wasm
    echo "✅ 已用 fork 自带的 guest wasm 覆盖产物（见 third_party/easytier-go-wasi/SHA256SUMS）"
fi
