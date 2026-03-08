# Bingtang Xueli

English | [中文](README_CN.md)

An open-source community platform built on [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — providing free AI model access through a quota-based credential sharing system.

## What is Bingtang Xueli?

Bingtang Xueli transforms CLIProxyAPI from a personal CLI proxy into a **multi-user public welfare platform**. Users register, claim quota via redemption codes, and access AI models (Claude, Gemini, OpenAI, etc.) through a shared credential pool — all managed via a modern web panel.

## Features

### User System
- Account registration with email verification (SMTP)
- JWT authentication (Access Token + Refresh Token)
- Invite code & referral system with dual-side rewards
- Per-user API Key for proxy access

### Quota & Credential Management
- Model-level quota allocation (request count / token limit)
- Redemption code templates for self-service claim
- Shared credential pool with weighted load balancing
- Contributor / Public / Private pool modes

### Admin Panel
- Real-time dashboard with request trends & model distribution
- User management (ban/unban, role assignment)
- Credential pool health monitoring
- Redemption code batch generation & template management
- Quota configuration per model pattern
- Router engine tuning (strategy, weights, health checks)
- System settings (SMTP, OAuth, general config)

### Security
- IP whitelist/blacklist with CIDR support
- Global & per-IP rate limiting
- Anomaly detection (high-frequency, model scanning, error spikes)
- Risk marking with automatic expiry
- Full audit logging
- Constant-time verification code comparison
- Crypto-secure random generation throughout

### Frontend
- React 19 + Tailwind CSS + Vite SPA
- Responsive design with mobile sidebar drawer
- Bilingual UI (Chinese / English) via Zustand i18n store
- Served at `/panel` alongside the proxy API

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend | Go 1.26, Gin, modernc.org/sqlite (CGO-free) |
| Frontend | React 19, TypeScript, Tailwind CSS, Vite, Zustand, Recharts |
| Auth | JWT HS256 (32-byte minimum secret) |
| Database | SQLite (WAL mode) |
| Container | Docker multi-arch (amd64 + arm64) |
| CI/CD | GitHub Actions + GHCR |

## Quick Start

### Docker Compose (Recommended)

```bash
# 1. Clone the repository
git clone https://github.com/hajimilvdou/BTXL.git
cd BTXL

# 2. Copy and edit configuration
cp config.example.yaml config.yaml
# Edit config.yaml with your settings

# 3. Start the service
docker compose up -d
```

The service exposes:
- **Port 8317** — API proxy + management panel (`/panel`)

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLI_PROXY_IMAGE` | `ghcr.io/hajimilvdou/btxl:latest` | Docker image |
| `CLI_PROXY_CONFIG_PATH` | `./config.yaml` | Config file path |
| `CLI_PROXY_AUTH_PATH` | `./auths` | OAuth credentials directory |
| `CLI_PROXY_LOG_PATH` | `./logs` | Log directory |

### Build from Source

```bash
# Requires Go 1.26+
go build -o bingtang-xueli ./cmd/server/
./bingtang-xueli
```

## Project Structure

```
internal/
  community/           # Community platform extension layer
    user/              # Auth, JWT, email verification
    quota/             # Quota engine, risk control
    credential/        # Redemption, referral, templates
    security/          # IP control, rate limit, anomaly, audit
    stats/             # Request statistics
    community.go       # Unified initializer
  panel/web/           # React SPA frontend
    src/pages/         # Auth / User / Admin pages
    src/api/           # TypeScript API client
    src/stores/        # Zustand state management
    src/i18n/          # Bilingual translations
  db/                  # SQLite store + migrations
  translator/          # Upstream API format translation (core)
```

## Attribution

This project is a derivative work of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) by Router-For.ME.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
