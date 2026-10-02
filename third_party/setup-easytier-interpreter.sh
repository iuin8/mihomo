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
patch_file=third_party/easytier-go-interpreter.patch

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

patch -p1 -d "$dest" < "$patch_file"

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
