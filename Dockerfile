# =============================================================================
# RemoteBash — 多阶段 Docker 构建
# =============================================================================
# 阶段 1: 编译 Go 静态二进制
# =============================================================================
FROM golang:1.25-alpine AS builder

WORKDIR /build

# 先复制依赖描述文件，利用 Docker 层缓存加速重复构建。
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并编译静态二进制。
COPY cmd/      ./cmd/
COPY internal/ ./internal/
COPY web/      ./web/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/

# =============================================================================
# 阶段 2: 最小运行镜像
# =============================================================================
FROM alpine:3.21

# ca-certificates — 备用（未来可能对接 HTTPS 外部服务）。
# wget — 用于 Docker HEALTHCHECK。
RUN apk add --no-cache ca-certificates wget

COPY --from=builder /build/remotebash /usr/local/bin/remotebash

# 持久化数据目录（SQLite 数据库）。
RUN mkdir -p /data

EXPOSE 24587

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:24587/health || exit 1

# 默认绑定所有接口，数据库写入持久化卷。
ENTRYPOINT ["/usr/local/bin/remotebash"]
CMD ["--host", "0.0.0.0", "--db", "/data/remotebash.db"]
