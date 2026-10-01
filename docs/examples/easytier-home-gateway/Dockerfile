# 家侧网关镜像：编译本 fork 的内核（含 TUN 模式）+ 预装 iptables（NAT 自动化用）
#
# 构建上下文必须是 mihomo 仓库根目录——docker-compose.yml 已经写好（build.context: ../../..），
# 所以你只需要 `docker compose up -d`，不需要手动 build。
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags with_gvisor -trimpath -ldflags '-w -s' -o /out/mihomo .

FROM alpine:3.20
RUN apk add --no-cache iptables iproute2
COPY --from=build /out/mihomo /usr/local/bin/mihomo
COPY docs/examples/easytier-home-gateway/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
