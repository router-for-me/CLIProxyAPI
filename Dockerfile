# CLIProxyAPI Docker image build file
# Multi-platform build using tonistiigi/xx cross-compilation to avoid QEMU emulation
# syntax=docker/dockerfile:1.4

# Build stage running on BUILDPLATFORM for native-speed compilation
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

# Version metadata
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG STRIP_BINARY=true

# Install the cross-compilation toolchain.
# tonistiigi/xx provides the cross-platform build helpers.
COPY --from=tonistiigi/xx:1.6.1 / /
RUN apk add --no-cache git ca-certificates tzdata clang lld

WORKDIR /app

# Configure the cross-compilation toolchain for TARGETPLATFORM
ARG TARGETPLATFORM
RUN xx-apk add musl-dev gcc

# Configure the Go module proxy
ENV GOPROXY=https://proxy.golang.org,direct

# Copy the Go module files
COPY go.mod go.sum ./

# Download dependencies on the native platform for faster builds
RUN --mount=type=cache,target=/root/.cache/go-mod \
    go mod download

# Copy the source tree
COPY . .

# Static build with CGO disabled.
# -trimpath removes build-path details for better reproducibility.
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/.cache/go-mod \
    set -eu; \
    ldflags="-X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}"; \
    if [ "${STRIP_BINARY}" = "true" ]; then \
      ldflags="-s -w ${ldflags}"; \
    fi; \
    xx-go build \
    -buildvcs=false \
    -trimpath \
    -ldflags="${ldflags}" \
    -o ./CLIProxyAPI ./cmd/server/ && \
    xx-verify CLIProxyAPI

# Runtime stage
FROM alpine:3.23

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG REPOSITORY_URL=unknown

LABEL org.opencontainers.image.source="${REPOSITORY_URL}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

RUN apk add --no-cache tzdata ca-certificates

RUN mkdir /CLIProxyAPI

COPY --from=builder /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
