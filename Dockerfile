FROM golang:1.26-alpine AS builder
RUN apk add --no-cache curl
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION
ARG BUILD_DATE
ARG COMMIT=auto

# 将版本号获取和 go build 合并到同一个 RUN 中
RUN set -e && \
    # 1. 版本号
    if [ -n "$VERSION" ]; then \
      FINAL_VERSION="$VERSION"; \
    else \
      FINAL_VERSION=$(curl -s https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'); \
    fi && \
    if [ -z "$FINAL_VERSION" ] || [ "$FINAL_VERSION" = "null" ]; then FINAL_VERSION="unknown"; fi && \
    echo "Building version: $FINAL_VERSION" && \
    # 2. 构建时间
    if [ -n "$BUILD_DATE" ]; then \
      FINAL_DATE="$BUILD_DATE"; \
    else \
      FINAL_DATE=$(date -u +'%Y-%m-%dT%H:%M:%SZ'); \
    fi && \
    echo "Build date: $FINAL_DATE" && \
    # 3. 构建
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w \
        -X 'main.Version=${FINAL_VERSION}' \
        -X 'main.Commit=${COMMIT}' \
        -X 'main.BuildDate=${FINAL_DATE}'" \
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

CMD sh -c '\
  if [ ! -f /CLIProxyAPI/data/entrypoint.sh ]; then \
    echo "Downloading entrypoint.sh..."; \
    curl -fsSL https://raw.githubusercontent.com/Yuxcoo/CLIProxyAPI/refs/heads/main/entrypoint.sh \
    -o /CLIProxyAPI/data/entrypoint.sh && \
    chmod +x /CLIProxyAPI/data/entrypoint.sh; \
  fi; \
  /CLIProxyAPI/data/entrypoint.sh'
