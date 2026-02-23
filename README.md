# CLIProxyAPI++ (KooshaPari Fork)

**Forked and enhanced from [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)**

Multi-provider LLM proxy with unified OpenAI-compatible API, third-party auth, SDK generation, and enterprise features.

---

## What is CLIProxyAPI++?

CLIProxyAPI++ provides a unified API gateway that translates OpenAI-compatible requests to any LLM provider:

```
Client (OpenAI format) → CLIProxyAPI++ → OpenAI, Anthropic, Google, AWS, Ollama, etc.
```

### Key Capabilities

| Capability | Description |
|------------|-------------|
| **Multi-Provider Routing** | Single endpoint for OpenAI, Anthropic, Google, AWS Bedrock, Ollama, Kiro, GitHub Copilot |
| **OAuth Authentication** | Built-in OAuth flows for Kiro (AWS CodeWhisperer) and GitHub Copilot |
| **Rate Limiting** | Token bucket algorithm with per-provider limits |
| **Metrics & Monitoring** | Prometheus metrics, cost tracking, latency monitoring |
| **SDK Generation** | Auto-generated Python and Go SDKs |

---

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
EOF

curl -o config.yaml https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.example.yaml
docker compose up -d
```

### From Source

```bash
go build -o cliproxy ./cmd/cliproxy
./cliproxy --config config.yaml
```

---

## Configuration

### Provider Setup

```yaml
providers:
  openai:
    api_key: ${OPENAI_API_KEY}
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
  kiro:
    enabled: true
  github_copilot:
    enabled: true
  ollama:
    enabled: true
    base_url: http://localhost:11434
```

### Rate Limiting

```yaml
rate_limit:
  requests_per_minute: 60
  tokens_per_minute: 100000
  cooldown_seconds: 30
```

---

## Supported Providers

| Provider | Auth Type | Status |
|----------|-----------|--------|
| OpenAI | API Key | ✅ |
| Anthropic | API Key | ✅ |
| Azure OpenAI | API Key/OAuth | ✅ |
| Google Gemini | API Key | ✅ |
| AWS Bedrock | IAM | ✅ |
| Kiro (CodeWhisperer) | OAuth | ✅ |
| GitHub Copilot | OAuth | ✅ |
| Ollama | Local | ✅ |
| LM Studio | Local | ✅ |

---

## API Endpoints

### OpenAI-Compatible

| Endpoint | Description |
|----------|-------------|
| `POST /v1/chat/completions` | Chat completions |
| `POST /v1/completions` | Text completions |
| `GET /v1/models` | List available models |

### Management

| Endpoint | Description |
|----------|-------------|
| `GET /health` | Health check |
| `GET /metrics` | Prometheus metrics |
| `GET /v1/providers` | Provider status |
| `POST /v1/providers/{provider}/refresh` | Refresh credentials |

---

## SDKs

### Python

```python
from cliproxy import CliproxyClient

client = CliproxyClient(
    base_url="http://localhost:8317",
    api_key="your-api-key"
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### Go

```go
package main

import (
    "github.com/KooshaPari/cliproxy-sdk-go/client"
)

func main() {
    c := client.New("http://localhost:8317", "your-api-key")
    resp, _ := c.Chat.Completions(&.ChatCompletionRequest{
        Model: "gpt-4o",
        Messages: []map[string]string{{"role": "user", "content": "Hello!"}},
    })
}
```

---

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
                  │   SDK Gen       │
                  │ (Python, Go)     │
                  └─────────────────┘
```

---

## Documentation

- [Start Here](docs/start-here.md) - Getting started guide
- [Provider Usage](docs/provider-usage.md) - Provider configuration
- [Provider Quickstarts](docs/provider-quickstarts.md) - 5-minute setup per provider
- [API Reference](docs/api/) - Full API documentation
- [SDK Usage](docs/sdk-usage.md) - SDK guides

---

## Development Philosophy

### Extend, Never Duplicate
- NEVER create a v2 file. Refactor the original.
- NEVER create a new class if an existing one can be made generic.
- NEVER create custom implementations when an OSS library exists.

### Primitives First
- Build generic building blocks before application logic.
- A provider interface + registry is better than N isolated classes.

### Research Before Implementing
- Check pkg.go.dev for existing libraries.
- Search GitHub for 80%+ implementations to fork/adapt.

---

## License

MIT License - see LICENSE file
