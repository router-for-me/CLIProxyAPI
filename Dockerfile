FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}-++' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./cliproxyapi++ ./cmd/server/

FROM alpine:3.22.0

# Install required packages for entrypoint script
RUN apk add --no-cache tzdata sed

RUN mkdir /CLIProxyAPI

COPY --from=builder ./app/cliproxyapi++ /CLIProxyAPI/cliproxyapi++
COPY --from=builder ./app/docker-init.sh /CLIProxyAPI/docker-init.sh
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

# Make entrypoint executable
RUN chmod +x /CLIProxyAPI/docker-init.sh

WORKDIR /CLIProxyAPI

# Expose default ports
EXPOSE 8317 8085 1455 54545 51121 11451

# Environment variable defaults
ENV TZ=Asia/Shanghai \
    CONFIG_FILE=/CLIProxyAPI/config.yaml \
    CONFIG_EXAMPLE=/CLIProxyAPI/config.example.yaml \
    AUTH_DIR=/root/.cli-proxy-api \
    LOGS_DIR=/CLIProxyAPI/logs

# Runtime configuration via environment variables (override config.yaml):
# - CLIPROXY_HOST: Server host (default: "" for all interfaces)
# - CLIPROXY_PORT: Server port (default: 8317)
# - CLIPROXY_SECRET_KEY: Management API secret key
# - CLIPROXY_ALLOW_REMOTE: Allow remote management access (true/false)
# - CLIPROXY_DEBUG: Enable debug logging (true/false)
# - CLIPROXY_ROUTING_STRATEGY: Routing strategy (round-robin/fill-first)

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

# Use entrypoint script for out-of-the-box deployment
ENTRYPOINT ["/CLIProxyAPI/docker-init.sh"]
CMD []