# 打包

## macOS

```bash
cd ..
CGO_ENABLED=0 GOARCH=arm64 GOOS=darwin go build -tags with_gvisor -trimpath -ldflags '-X "github.com/metacubex/mihomo/constant.Version=v1.19.17-ssh" -w -s -buildid=' -o verge-mihomo-alpha
```
