# syntax=docker/dockerfile:1
# 家侧网关镜像：编译本 fork 的内核（含 TUN 模式）+ 预装 iptables（NAT 自动化用）
#
# 构建上下文必须是 mihomo 仓库根目录——docker-compose.yml 已经写好（build.context: ../../..），
# 所以你只需要 `docker compose up -d`，不需要手动 build。
#
# ⚠️ 国内网络：Go 官方代理 proxy.golang.org 在国内会 i/o timeout，所以默认走 goproxy.cn。
#    换源（任何可用代理都行）：
#      docker compose build --build-arg GOPROXY=https://mirrors.aliyun.com/goproxy/,direct
#    完全不想在容器里编译（最快、也不依赖 Go 代理）：见 Dockerfile.prebuilt
FROM golang:1.25-alpine AS build

ARG GOPROXY=https://goproxy.cn,direct
ARG GOSUMDB=sum.golang.google.cn
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB} \
    CGO_ENABLED=0

WORKDIR /src
COPY go.mod go.sum ./
# 模块缓存挂进 BuildKit cache：第二次构建起不再重复下载
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -tags with_gvisor -trimpath -ldflags '-w -s' -o /out/mihomo .

FROM alpine:3.20
# 国内网络可换 Alpine 镜像源（默认已用中科大；置空则用官方源）
ARG APK_MIRROR=mirrors.ustc.edu.cn
RUN if [ -n "$APK_MIRROR" ]; then sed -i "s#dl-cdn.alpinelinux.org#$APK_MIRROR#g" /etc/apk/repositories; fi \
 && apk add --no-cache iptables iproute2
COPY --from=build /out/mihomo /usr/local/bin/mihomo
COPY docs/examples/easytier-home-gateway/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
