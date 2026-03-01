FROM golang:1.26-alpine AS builder
RUN apk add --no-cache curl
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# ========== 1. 版本号 ==========
ARG VERSION
RUN export VERSION && VERSION=$( \
    if [ -n "$VERSION" ]; then echo "$VERSION"; \
    else curl -s https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'; \
    fi \
) && if [ -z "$VERSION" ] || [ "$VERSION" = "null" ]; then VERSION="unknown"; fi && \
    echo "Building version: $VERSION"
# ========== 2. 构建时间 ==========
ARG BUILD_DATE
RUN export BUILD_DATE && BUILD_DATE=$( \
    if [ -n "$BUILD_DATE" ]; then echo "$BUILD_DATE"; \
    else date -u +'%Y-%m-%dT%H:%M:%SZ'; \
    fi \
) && if [ -z "$BUILD_DATE" ]; then BUILD_DATE="unknown"; fi && \
    echo "Build date: $BUILD_DATE"
# ========== Commit ID ==========
ARG COMMIT=auto
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w \
        -X 'main.Version=${VERSION}' \
        -X 'main.Commit=${COMMIT}' \
        -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./CLIProxyAPI ./cmd/server/
FROM alpine:3.22.0

RUN apk add --no-cache tzdata ca-certificates curl

RUN mkdir -p /CLIProxyAPI/config /CLIProxyAPI/data

COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY config.example.yaml /tmp/config_init.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai
RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

# 复制启动脚本
# COPY entrypoint.sh /CLIProxyAPI/entrypoint.sh
# RUN chmod +x /CLIProxyAPI/entrypoint.sh
# CMD ["/CLIProxyAPI/entrypoint.sh"]

CMD sh -c '\
  if [ ! -f /CLIProxyAPI/data/entrypoint.sh ]; then \
    echo "Downloading entrypoint.sh..."; \
    curl -fsSL https://raw.githubusercontent.com/Yuxcoo/CLIProxyAPI/refs/heads/main/entrypoint.sh \
    -o /CLIProxyAPI/data/entrypoint.sh && \
    chmod +x /CLIProxyAPI/data/entrypoint.sh; \
  fi; \
  /CLIProxyAPI/data/entrypoint.sh'
