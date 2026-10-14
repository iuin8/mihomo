# syntax=docker/dockerfile:1
# 镜像构建定义：编译本 fork 的内核（含 TUN 模式）+ 预装 iptables
#
# 三个角色共用这一个镜像（ghcr.io/iuin8/mihomo）—— 上游内核会静默忽略 tun: true ✗，
# 所以家侧/出口都**必须**用本 fork 构建的镜像/内核 ✓。
#
# 构建上下文 = 仓库根目录 ✓（CI 里就是 `context: .` + `file: docs/easytier/Dockerfile` ✓）
# 本地手动构建：
#   docker build -f docs/easytier/Dockerfile --build-arg MIHOMO_VERSION=dev -t mihomo-fork .
#
# ⚠️ 国内网络：Go 官方代理 proxy.golang.org 在国内会 i/o timeout，所以默认走 goproxy.cn。
#    换源（任何可用代理都行）：
#      docker compose build --build-arg GOPROXY=https://mirrors.aliyun.com/goproxy/,direct
#    完全不想在容器里编译（最快、也不依赖 Go 代理）：见 Dockerfile.prebuilt
FROM golang:1.26-alpine AS build

ARG GOPROXY=https://goproxy.cn,direct
ARG GOSUMDB=sum.golang.google.cn
# 版本号注入：不注入的话 `mihomo -v` 会显示源码里的占位值（1.10.0）
ARG MIHOMO_VERSION=dev
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB} \
    CGO_ENABLED=0

WORKDIR /src
COPY go.mod go.sum ./
# go.mod 里有 `replace github.com/easytier/easytier/easytier-go => ./third_party/easytier-go`，
# 所以 go mod download 之前该目录必须已就位，否则解析不到替换目标（CI 首次构建就栽在这里）。
COPY third_party/ ./third_party/
# 模块缓存挂进 BuildKit cache：第二次构建起不再重复下载
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -tags with_gvisor -trimpath \
      -ldflags "-w -s -X 'github.com/metacubex/mihomo/constant.Version=${MIHOMO_VERSION}'" \
      -o /out/mihomo .

FROM alpine:3.24
# iptables 不是运行必需 —— 实测 guest（EasyTier）在 TUN 模式下自己会 SNAT，见 SOP §6.13。保留它是为了：
#   ① 遇到特殊拓扑时按 §6.13 的逃生口自己加一条 SNAT；② 把本镜像当通用路由容器用时随手就有。
# 国内网络可换 Alpine 镜像源（默认已用中科大；置空则用官方源）
ARG APK_MIRROR=mirrors.ustc.edu.cn
RUN if [ -n "$APK_MIRROR" ]; then sed -i "s#dl-cdn.alpinelinux.org#$APK_MIRROR#g" /etc/apk/repositories; fi \
 && apk add --no-cache iptables iproute2
COPY --from=build /out/mihomo /usr/local/bin/mihomo
# 纯 mihomo：镜像里没有入口脚本，配置与行为全部来自挂载的 YAML
ENTRYPOINT ["/usr/local/bin/mihomo"]
CMD ["-d", "/root/.config/mihomo", "-f", "/root/.config/mihomo/config.yaml"]
