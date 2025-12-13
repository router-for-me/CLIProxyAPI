FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# Важно: кладём бинарник в корень образа builder-а
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /CLIProxyAPI ./cmd/server/

# ========================
FROM alpine:3.22.0

RUN apk add --no-cache tzdata ca-certificates

WORKDIR /CLIProxyAPI

# Правильно берём из корня builder-образа
COPY --from=builder /CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

# ЭТО ГЛАВНОЕ — создаём именно тот файл, который ищет приложение
COPY config.example.yaml /CLIProxyAPI/config.yaml

# Оставляем и пример (необязательно, но удобно)
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

ENV TZ=Asia/Shanghai
RUN cp /usr/share/zoneinfo/$TZ /etc/localtime && echo "$TZ" > /etc/timezone

EXPOSE 8317

CMD ["./CLIProxyAPI"]