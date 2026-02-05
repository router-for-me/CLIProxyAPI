FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o codex-lite ./cmd/server

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/codex-lite .
COPY config.example.yaml config.yaml
COPY web/static ./web/static

EXPOSE 8080
CMD ["./codex-lite"]
