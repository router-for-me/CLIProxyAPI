> **Work state:** ACTIVE · **Progress:** `███████░░░ 70%`
> CLIProxyAPI++ — multi-provider LLM proxy fork. Builds + CI workflows repaired (25 files). Residual: pre-existing Go test failures surfaced now that CI runs. · updated 2026-06-02

# cliproxyapi-plusplus
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2FKooshaPari%2Fcliproxyapi-plusplus.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2FKooshaPari%2Fcliproxyapi-plusplus?ref=badge_shield)
[![AI Slop Inside](https://sladge.net/badge.svg)](https://sladge.net)


Agent-native, multi-provider OpenAI-compatible proxy for production and local model routing.

This is the Plus version of [cliproxyapi-plusplus](https://github.com/kooshapari/cliproxyapi-plusplus), adding support for third-party providers on top of the mainline project.

All third-party provider support is maintained by community contributors; cliproxyapi-plusplus does not provide technical support. Please contact the corresponding community maintainer if you need assistance.

## Key Features

- OpenAI-compatible request surface across heterogeneous providers.
- Unified auth and token handling for OpenAI, Anthropic, Gemini, Kiro, Copilot, and more.
- Provider-aware routing and model conversion.
- Built-in operational tooling for management APIs and diagnostics.

## Architecture

- `cmd/server`: primary API server entrypoint.
- `cmd/cliproxyctl`: operational CLI.
- `internal/`: runtime/auth/translator internals.
- `pkg/llmproxy/`: reusable proxy modules.
- `sdk/`: SDK-facing interfaces.

## Getting Started

### Prerequisites

- Go 1.24+
- Docker (optional)
- Provider credentials for target upstreams

### Quick Start (local Go)

The fastest path to a running proxy on localhost. Uses the bundled minimal config.

```bash
# Clone and enter the repo
git clone https://github.com/KooshaPari/cliproxyapi-plusplus.git
cd cliproxyapi-plusplus

# Copy the minimal first-run config and edit api-keys / claude-api-key
cp config.minimal.yaml config.yaml
$EDITOR config.yaml

# Run the server (binds 0.0.0.0:8317)
go run ./cmd/server --config config.yaml

# Smoke test from another shell
curl -H "Authorization: Bearer <your api-keys entry>" \
     http://127.0.0.1:8317/v1/models
```

For the full schema (TLS, OAuth providers, routing, quotas, management API), see
[`config.example.yaml`](./config.example.yaml). Drop fields into `config.yaml` as you need them.

### Quick Start (Docker)

```bash
# Create deployment directory
mkdir -p ~/cli-proxy && cd ~/cli-proxy

# Create docker-compose.yml
cat > docker-compose.yml << 'EOF'
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api-plus:latest
    container_name: cli-proxy-api-plus
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
      - ./logs:/CLIProxyAPI/logs
    restart: unless-stopped
EOF

# Download minimal config (or grab config.example.yaml for the full schema)
curl -o config.yaml https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.minimal.yaml

# Pull and start
docker compose pull && docker compose up -d
```

```bash
docker run -p 8317:8317 eceasy/cli-proxy-api-plus:latest
```

### Configuration

- [`config.minimal.yaml`](./config.minimal.yaml) — ~25 lines, everything you need to boot the proxy with a static Claude API key. Start here.
- [`config.example.yaml`](./config.example.yaml) — full annotated schema (~430 lines): every provider, OAuth, routing, TLS, management API, quotas. Reference only — copy fields into your `config.yaml` as needed.

## Operations and Security

- Rate limiting and quota/cooldown controls.
- Auth flows for provider-specific OAuth/API keys.
- CI policy checks and path guards.
- Governance and security docs under `docs/operations/` and `docs/reference/`.

## Testing and Quality

```bash
go test ./...
```

Quality gates are enforced via repo CI workflows (build/lint/path guards).

## Documentation

- `docs/start-here.md` - Getting started guide
- `docs/provider-usage.md` - Provider configuration
- `docs/provider-quickstarts.md` - Per-provider guides
- `docs/api/` - API reference
- `docs/sdk-usage.md` - SDK guides

## Environment

```bash
cd docs
npm install
npm run docs:dev
npm run docs:build
```

---

This project only accepts pull requests that relate to third-party provider support. Any pull requests unrelated to third-party provider support will be rejected.

If you need to submit any non-third-party provider changes, please open them against the [mainline](https://github.com/kooshapari/cliproxyapi-plusplus) repository.

## License

MIT License. See `LICENSE`.


[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2FKooshaPari%2Fcliproxyapi-plusplus.svg?type=large)](https://app.fossa.com/projects/git%2Bgithub.com%2FKooshaPari%2Fcliproxyapi-plusplus?ref=badge_large)
