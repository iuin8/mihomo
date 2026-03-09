# SSH System Release Guide

This document describes how to build and release Mihomo with SSH system adapter enhancements.

## Platforms
The following 4 architectures are targeted:
- macOS (Darwin) amd64 (v3)
- macOS (Darwin) arm64
- Windows amd64 (v3)
- Windows arm64

## Automated Release (Recommended)
The project is configured with a GitHub Action that triggers on tags matching `tag_ssh_system_*`.

### Steps:
1. Ensure your local branch is up to date.
2. Create and push a tag:
   ```bash
   git tag tag_ssh_system_v1.19.20_11 && git push origin tag_ssh_system_v1.19.20_11
   ```
   > [!NOTE]
   > Use full ref `refs/tags/...` if there is a branch with the same name as the tag.

3. The workflow will automatically build the binaries and create a GitHub Release.

## Manual Release
If you need to build manually on a local machine (macOS with Go installed):

### 1. Build commands
```bash
VERSION="tag_ssh_system_v1.19.20_11"
BUILDTIME="$(date -u)"
CGO_FLAGS="CGO_ENABLED=0"
TAGS="-tags with_gvisor"
LDFLAGS="-X 'github.com/metacubex/mihomo/constant.Version=${VERSION}' -X 'github.com/metacubex/mihomo/constant.BuildTime=${BUILDTIME}' -w -s -buildid="

# Darwin AMD64 v3
GOARCH=amd64 GOOS=darwin GOAMD64=v3 $CGO_FLAGS go build $TAGS -trimpath -ldflags "$LDFLAGS" -o bin/mihomo-darwin-amd64-v3

# Darwin ARM64
GOARCH=arm64 GOOS=darwin $CGO_FLAGS go build $TAGS -trimpath -ldflags "$LDFLAGS" -o bin/mihomo-darwin-arm64

# Windows AMD64 v3
GOARCH=amd64 GOOS=windows GOAMD64=v3 $CGO_FLAGS go build $TAGS -trimpath -ldflags "$LDFLAGS" -o bin/mihomo-windows-amd64-v3.exe

# Windows ARM64
GOARCH=arm64 GOOS=windows $CGO_FLAGS go build $TAGS -trimpath -ldflags "$LDFLAGS" -o bin/mihomo-windows-arm64.exe
```

### 2. Packaging
```bash
# MacOS (preserves permissions with tar)
chmod +x bin/mihomo-darwin-amd64-v3
tar -czvf bin/mihomo-darwin-amd64-v3-${VERSION}.tar.gz -C bin mihomo-darwin-amd64-v3

chmod +x bin/mihomo-darwin-arm64
tar -czvf bin/mihomo-darwin-arm64-${VERSION}.tar.gz -C bin mihomo-darwin-arm64

# Windows
zip -j bin/mihomo-windows-amd64-v3-${VERSION}.zip bin/mihomo-windows-amd64-v3.exe
zip -j bin/mihomo-windows-arm64-${VERSION}.zip bin/mihomo-windows-arm64.exe
```

### 3. Release
Use `gh` CLI or GitHub Web UI to create the release and upload the `.tar.gz` and `.zip` files.
