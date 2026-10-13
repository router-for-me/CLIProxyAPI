FROM golang:1.26-bookworm AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates git && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# Technical Debt Note: -checklinkname=0 inherited from Commit 2 (pion/ice -> wlynxg/anet)
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -buildvcs=false \
    -tags "netgo osusergo" \
    -ldflags="-s -w -checklinkname=0 -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./CLIProxyAPI ./cmd/server/

# Keep glibc for dynamic plugins and /bin/sh for the cluster Compose command.
FROM debian:bookworm-slim

ENV TZ=Asia/Shanghai

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tzdata ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /root/.cli-proxy-api /CLIProxyAPI/logs /CLIProxyAPI/plugins \
    && ln -snf /usr/share/zoneinfo/${TZ} /etc/localtime \
    && echo "${TZ}" > /etc/timezone

WORKDIR /CLIProxyAPI

COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

EXPOSE 8317

# Preserve access to root-owned 0600 credentials in existing bind mounts.
USER 0:0

CMD ["/CLIProxyAPI/CLIProxyAPI"]
