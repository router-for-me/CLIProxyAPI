# syntax=docker/dockerfile:1

# ── Stage 1: Go build ──────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG ALL_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG all_proxy
ARG no_proxy
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org

ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    ALL_PROXY=${ALL_PROXY} \
    NO_PROXY=${NO_PROXY} \
    http_proxy=${http_proxy} \
    https_proxy=${https_proxy} \
    all_proxy=${all_proxy} \
    no_proxy=${no_proxy} \
    GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/server/ ./cmd/server/
COPY internal/ ./internal/
COPY sdk/ ./sdk/

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

# ── Stage 2: Runtime ───────────────────────────────────────────────
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

# 安装基础依赖
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    python3 \
    python3-pip \
    python3-venv \
    wget \
    gnupg2 \
    libnss3 \
    libatk-bridge2.0-0 \
    libdrm2 \
    libxcomposite1 \
    libxdamage1 \
    libxrandr2 \
    libgbm1 \
    libpango-1.0-0 \
    libcairo2 \
    libasound2 \
    libxshmfence1 \
    libx11-xcb1 \
    libxcb1 \
    libxfixes3 \
    libxkbcommon0 \
    fonts-liberation \
    xdg-utils \
    ping \
    telnet \
    && rm -rf /var/lib/apt/lists/*

# 安装 Playwright
RUN python3 -m venv /opt/playwright-venv \
    && /opt/playwright-venv/bin/pip install --no-cache-dir playwright \
    && /opt/playwright-venv/bin/python -m playwright install chromium \
    && /opt/playwright-venv/bin/python -m playwright install-deps chromium

ENV PATH="/opt/playwright-venv/bin:${PATH}"

# 时区（很少变动）
ENV TZ=Asia/Shanghai
RUN ln -sf /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

# ── 最后 COPY Go 二进制（变动最频繁，放在最后最大化缓存命中） ───────
RUN mkdir /CLIProxyAPI
COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

VOLUME ["/root/.cli-proxy-api"]

EXPOSE 8317

CMD ["./CLIProxyAPI"]
