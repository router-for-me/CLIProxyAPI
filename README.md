# CLIProxyAPI++ (KooshaPari Fork)

**Forked and enhanced from [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)**

Multi-provider LLM proxy with unified OpenAI-compatible API, third-party auth, SDK generation, and enterprise features.

## Overview

CLIProxyAPI++ provides a unified API gateway for multiple LLM providers with:
- OpenAI-compatible endpoints
- Third-party provider support (Kiro, GitHub Copilot, Ollama)
- OAuth authentication flows
- Built-in rate limiting and metrics
- SDK auto-generation

## Architecture

```
┌──────────────┐     ┌─────────────────┐     ┌────────────┐
│   Clients    │────▶│   CLIProxy++     │────▶│  Providers │
│ (thegent,   │     │  (this repo)    │     │ (OpenAI,   │
│  agentapi)   │     │                 │     │  Anthropic,│
└──────────────┘     └─────────────────┘     │  AWS, etc) │
                         │                   └────────────┘
                         ▼
                  ┌─────────────────┐
                  │   SDK Gen      │
                  │ (Python, Go)   │
                  └─────────────────┘
```

## Quick Start

### Docker

```bash
mkdir -p ~/cli-proxy && cd ~/cli-proxy

cat > docker-compose.yml << 'EOF'
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api-plus:latest
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
    restart: unless-stopped
EOF

curl -o config.yaml https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.example.yaml
docker compose up -d
```

### From Source

```bash
# Build
go build -o cliproxy ./cmd/cliproxy

# Run
./cliproxy --config config.yaml
```

## Configuration

```yaml
server:
  port: 8317

providers:
  openai:
    api_key: ${OPENAI_API_KEY}
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
  kiro:
    enabled: true
  github_copilot:
    enabled: true

rate_limit:
  requests_per_minute: 60
  tokens_per_minute: 100000
```

## Features

### Provider Support

| Provider | Auth | Status |
|----------|------|--------|
| OpenAI | API Key | ✅ |
| Anthropic | API Key | ✅ |
| Azure OpenAI | API Key/OAuth | ✅ |
| Google Gemini | API Key | ✅ |
| AWS Bedrock | IAM | ✅ |
| Kiro (CodeWhisperer) | OAuth | ✅ |
| GitHub Copilot | OAuth | ✅ |
| Ollama | Local | ✅ |

### Authentication

- **API Key** - Standard OpenAI-style
- **OAuth** - Kiro, GitHub Copilot via web flow
- **AWS IAM** - Bedrock credentials

### Rate Limiting

- Token bucket algorithm
- Per-provider limits
- Cooldown management
- Usage quotas

### Observability

- Request/response logging
- Cost tracking
- Latency metrics
- Error rate monitoring

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `POST /v1/chat/completions` | Chat completions |
| `POST /v1/completions` | Text completions |
| `GET /v1/models` | List models |
| `GET /health` | Health check |
| `GET /metrics` | Prometheus metrics |

## SDKs

Auto-generated SDKs for:

- **Python** - `pip install cliproxy-sdk`
- **Go** - `go get github.com/KooshaPari/cliproxy-sdk-go`

## Integration

### With thegent

```yaml
# thegent config
llm:
  provider: cliproxy
  base_url: http://localhost:8317/v1
  api_key: ${CLIPROXY_API_KEY}
```

### With agentapi

```bash
agentapi --cliproxy http://localhost:8317
```

## Development

```bash
# Lint
go fmt ./...
go vet ./...

# Test
go test ./...

# Generate SDKs
./scripts/generate_sdks.sh
```

## Fork Differences

This fork includes:

- ✅ SDK auto-generation workflow
- ✅ Enhanced OpenAPI spec
- ✅ Python client SDK (`pkg/sdk/python`)
- ✅ Go client SDK (`pkg/sdk/go`)
- ✅ Integration with tokenledger for cost tracking

## License

MIT License - see LICENSE file
