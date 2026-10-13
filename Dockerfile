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
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -tags "netgo osusergo" \
    -ldflags="-s -w -checklinkname=0 -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./CLIProxyAPI ./cmd/server/

# Prepare directories with permissive access for nonroot compatibility
RUN mkdir -p /root/.cli-proxy-api /CLIProxyAPI/logs /CLIProxyAPI/plugins /home/nonroot \
    && ln -s /root/.cli-proxy-api /home/nonroot/.cli-proxy-api \
    && chmod -R 777 /root /CLIProxyAPI /home/nonroot

FROM gcr.io/distroless/static-debian12:nonroot

# Copy timezone data from builder
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy directories and compatibility symlinks
COPY --from=builder --chown=nonroot:nonroot /root /root
COPY --from=builder --chown=nonroot:nonroot /home/nonroot /home/nonroot
COPY --from=builder --chown=nonroot:nonroot /CLIProxyAPI /CLIProxyAPI

WORKDIR /CLIProxyAPI

COPY --from=builder --chown=nonroot:nonroot /app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --chown=nonroot:nonroot config.example.yaml /CLIProxyAPI/config.example.yaml

EXPOSE 8317

ENV TZ=Asia/Shanghai

USER nonroot:nonroot

CMD ["/CLIProxyAPI/CLIProxyAPI"]
