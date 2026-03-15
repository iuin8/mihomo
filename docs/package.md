# 打包

## macOS

```bash
cd ..
CGO_ENABLED=0 GOARCH=arm64 GOOS=darwin go build -tags with_gvisor -trimpath -ldflags '-X "github.com/metacubex/mihomo/constant.Version=tag_ssh_system_v1.19.21_12" -w -s -buildid=' -o verge-mihomo-alpha
```

## Windows

```bash
cd ..
CGO_ENABLED=0 GOARCH=amd64 GOOS=windows go build -tags with_gvisor -trimpath -ldflags '-X "github.com/metacubex/mihomo/constant.Version=tag_ssh_system_v1.19.21_12" -w -s -buildid=' -o verge-mihomo-alpha.exe
```

## 打标

```bash
# 触发 GitHub action 自动发布新包
git tag tag_ssh_system_v1.19.21_12 && git push origin tag_ssh_system_v1.19.21_12
```
