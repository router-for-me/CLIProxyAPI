FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./cli-proxy-api ./cmd/server/

FROM alpine:3.22

RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    curl \
    su-exec \
    && rm -rf /var/cache/apk/*

# Create non-root user
RUN addgroup -g 1000 cliproxy && \
    adduser -u 1000 -G cliproxy -s /bin/sh -D cliproxy

WORKDIR /app

COPY --from=builder /app/cli-proxy-api /app/cli-proxy-api
COPY config.example.yaml /app/config.example.yaml
COPY deploy/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

RUN mkdir -p /app/data /app/logs /app/auths && \
    chown -R cliproxy:cliproxy /app

EXPOSE 8317

ENV TZ=Asia/Shanghai
RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget -q -T 5 -O /dev/null http://localhost:8317/ || exit 1

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/cli-proxy-api"]
