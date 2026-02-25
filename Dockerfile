# CLIProxyAPI Docker镜像构建文件
# 多平台构建：使用 tonistiigi/xx + 交叉编译，避免 QEMU 模拟
# syntax=docker/dockerfile:1.4

# 构建阶段 - 使用 BUILDPLATFORM 在原生架构执行
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

# 版本号参数
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# 安装交叉编译工具链
# tonistiigi/xx 提供跨架构编译辅助工具
COPY --from=tonistiigi/xx:1.6.1 / /
RUN apk add --no-cache git ca-certificates tzdata clang lld upx

WORKDIR /app

# 配置目标平台的交叉编译工具链
ARG TARGETPLATFORM
RUN xx-apk add musl-dev gcc

# 设置Go模块代理
ENV GOPROXY=https://proxy.golang.org,direct

# 复制go mod文件
COPY go.mod go.sum ./

# 下载依赖（在原生平台执行，速度快）
RUN --mount=type=cache,target=/root/.cache/go-mod \
    go mod download

# 复制源代码
COPY . .

# 静态编译（CGO_ENABLED=0）
# -trimpath 移除构建路径信息，增强安全性和可复现性
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/.cache/go-mod \
    xx-go build \
    -buildvcs=false \
    -trimpath \
    -ldflags="-s -w \
      -X 'main.Version=${VERSION}' \
      -X 'main.Commit=${COMMIT}' \
      -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./CLIProxyAPI ./cmd/server/ && \
    xx-verify CLIProxyAPI && \
    upx --best --lzma ./CLIProxyAPI

# 运行阶段
FROM alpine:3.22.0

RUN apk add --no-cache tzdata ca-certificates

RUN mkdir /CLIProxyAPI

COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
