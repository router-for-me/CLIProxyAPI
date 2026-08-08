FROM golang:1.26-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=1 GOOS=linux go build \
    -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /out/CLIProxyAPI \
    ./cmd/server/

RUN plugin_arch="$(go env GOARCH)" \
    && plugin_dir="/out/plugins/linux/${plugin_arch}" \
    && mkdir -p "${plugin_dir}" \
    && cd examples/plugin/kiro/go \
    && CGO_ENABLED=1 GOOS=linux go build \
        -buildmode=c-shared \
        -o "${plugin_dir}/kiro-go.so" \
        . \
    && rm -f "${plugin_dir}/kiro-go.h"

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /CLIProxyAPI

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --from=builder /out/plugins /CLIProxyAPI/plugins
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=UTC

CMD ["./CLIProxyAPI"]
