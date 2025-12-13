FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN apk add --no-cache gcc musl-dev sqlite-dev
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /app-bin ./cmd/server/

FROM alpine:3.22.0
RUN apk add --no-cache ca-certificates tzdata sqlite-libs
WORKDIR /app
COPY --from=builder /app-bin /app/cliproxy
COPY config.yaml /app/config.yaml
RUN chmod +x /app/cliproxy
EXPOSE 8317 8081

# Просто запускаем без флагов, настройки берутся из config.yaml
CMD ["/app/cliproxy", "--config", "/app/config.yaml"]