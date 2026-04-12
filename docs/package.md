# 打包

## 会话记录

```bash
claude --resume 0e21b145-621d-4262-9de7-d559ece2c8d3 --dangerously-skip-permissions
```

## macOS

```bash
cd ..
CGO_ENABLED=0 GOARCH=arm64 GOOS=darwin go build -tags with_gvisor -trimpath -ldflags '-X "github.com/metacubex/mihomo/constant.Version=tag_ssh_system_v1.19.21_13" -w -s -buildid=' -o verge-mihomo-alpha
```

## Windows

```bash
cd ..
CGO_ENABLED=0 GOARCH=amd64 GOOS=windows go build -tags with_gvisor -trimpath -ldflags '-X "github.com/metacubex/mihomo/constant.Version=tag_ssh_system_v1.19.21_13" -w -s -buildid=' -o verge-mihomo-alpha.exe
```

## 打标

```bash
# 触发 GitHub action 自动发布新包
TAG=v1.19.23-fa.1015 && git tag $TAG && git push origin $TAG
```
