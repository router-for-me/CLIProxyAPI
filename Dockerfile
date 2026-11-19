# Bundled Antigravity guard plugin, installed into the plugins directory at startup.
FROM golang:1.26-bookworm AS guard-plugin

ARG GUARD_PLUGIN_REPO=https://github.com/zhyx111999/cpa-plugin-antigravity-coding-filter.git
ARG GUARD_PLUGIN_REF=0a5791203b03c970618a355ab3f5eac2e2d8a293

WORKDIR /plugin

RUN git init -q && git fetch -q --depth 1 "${GUARD_PLUGIN_REPO}" "${GUARD_PLUGIN_REF}" && git checkout -q FETCH_HEAD

RUN CGO_ENABLED=1 go build -buildmode=c-shared -o /out/antigravity-coding-filter.so . && rm -f /out/*.h

FROM golang:1.26-bookworm AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM debian:bookworm

RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*

RUN mkdir /CLIProxyAPI

COPY --from=builder ./app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY --from=guard-plugin /out/ /CLIProxyAPI/bundled-plugins/${TARGETOS}/${TARGETARCH}/

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
