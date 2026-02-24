# CLIProxyAPI Plus

<<<<<<< HEAD
This repository works with Claude and other AI agents as autonomous software engineers.
=======
English | [Chinese](README_CN.md)

This is the Plus version of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI), adding support for third-party providers on top of the mainline project.
>>>>>>> fix/circular-import-config

All third-party provider support is maintained by community contributors; CLIProxyAPI does not provide technical support. Please contact the corresponding community maintainer if you need assistance.

<<<<<<< HEAD
```bash
# Docker
docker run -p 8317:8317 eceasy/cli-proxy-api-plus:latest

# Or build from source
go build -o cliproxy ./cmd/cliproxy
./cliproxy --config config.yaml

# Health check
curl http://localhost:8317/health
```

## Multi-Provider Routing

Route OpenAI-compatible requests to any provider:

```bash
# List models
curl http://localhost:8317/v1/models

# Chat completion (OpenAI)
curl -X POST http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello"}]}'

# Chat completion (Anthropic)
curl -X POST http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-3-5-sonnet", "messages": [{"role": "user", "content": "Hello"}]}'
```

### Provider Configuration

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

## Supported Providers

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
| LM Studio | Local | ✅ |

## Documentation

- `docs/start-here.md` - Getting started guide
- `docs/provider-usage.md` - Provider configuration
- `docs/provider-quickstarts.md` - Per-provider guides
- `docs/api/` - API reference
- `docs/sdk-usage.md` - SDK guides

## Environment

```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-..."
export CLIPROXY_PORT=8317
=======
The Plus release stays in lockstep with the mainline features.

## Differences from the Mainline

- Added GitHub Copilot support (OAuth login), provided by [em4go](https://github.com/em4go/CLIProxyAPI/tree/feature/github-copilot-auth)
- Added Kiro (AWS CodeWhisperer) support (OAuth login), provided by [fuko2935](https://github.com/fuko2935/CLIProxyAPI/tree/feature/kiro-integration), [Ravens2121](https://github.com/Ravens2121/CLIProxyAPIPlus/)

## New Features (Plus Enhanced)

- **OAuth Web Authentication**: Browser-based OAuth login for Kiro with beautiful web UI
- **Rate Limiter**: Built-in request rate limiting to prevent API abuse
- **Background Token Refresh**: Automatic token refresh 10 minutes before expiration
- **Metrics & Monitoring**: Request metrics collection for monitoring and debugging
- **Device Fingerprint**: Device fingerprint generation for enhanced security
- **Cooldown Management**: Smart cooldown mechanism for API rate limits
- **Usage Checker**: Real-time usage monitoring and quota management
- **Model Converter**: Unified model name conversion across providers
- **UTF-8 Stream Processing**: Improved streaming response handling

## Kiro Authentication

### Web-based OAuth Login

Access the Kiro OAuth web interface at:

```
http://your-server:8080/v0/oauth/kiro
```

This provides a browser-based OAuth flow for Kiro (AWS CodeWhisperer) authentication with:
- AWS Builder ID login
- AWS Identity Center (IDC) login
- Token import from Kiro IDE

## Quick Deployment with Docker

### One-Command Deployment

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

# Download example config
curl -o config.yaml https://raw.githubusercontent.com/router-for-me/CLIProxyAPIPlus/main/config.example.yaml

# Pull and start
docker compose pull && docker compose up -d
>>>>>>> fix/circular-import-config
```

### Configuration

<<<<<<< HEAD
## Development Philosophy

### Extend, Never Duplicate

- NEVER create a v2 file. Refactor the original.
- NEVER create a new class if an existing one can be made generic.
- NEVER create custom implementations when an OSS library exists.
- Before writing ANY new code: search the codebase for existing patterns.

### Primitives First

- Build generic building blocks before application logic.
- A provider interface + registry is better than N isolated classes.
- Template strings > hardcoded messages. Config-driven > code-driven.

### Research Before Implementing

- Check pkg.go.dev for existing libraries.
- Search GitHub for 80%+ implementations to fork/adapt.

---

## Library Preferences (DO NOT REINVENT)

| Need | Use | NOT |
|------|-----|-----|
| HTTP router | chi | custom router |
| Logging | zerolog | fmt.Print |
| Config | viper | manual env parsing |
| Validation | go-playground/validator | manual if/else |
| Rate limiting | golang.org/x/time/rate | custom limiter |

---

## Code Quality Non-Negotiables

- Zero new lint suppressions without inline justification
- All new code must pass: go fmt, go vet, golint
- Max function: 40 lines
- No placeholder TODOs in committed code

### Go-Specific Rules

- Use `go fmt` for formatting
- Use `go vet` for linting
- Use `golangci-lint` for comprehensive linting
- All public APIs must have godoc comments

---

## Verifiable Constraints

| Metric | Threshold | Enforcement |
|--------|-----------|-------------|
| Tests | 80% coverage | CI gate |
| Lint | 0 errors | golangci-lint |
| Security | 0 critical | trivy scan |

---

## Domain-Specific Patterns

### What CLIProxyAPI++ Is

CLIProxyAPI++ is an **OpenAI-compatible API gateway** that translates client requests to multiple upstream LLM providers. The core domain is: provide a single API surface that routes to heterogeneous providers with auth, rate limiting, and metrics.

### Key Interfaces

| Interface | Responsibility | Location |
|-----------|---------------|----------|
| **Router** | Request routing to providers | `pkg/llmproxy/router/` |
| **Provider** | Provider abstraction | `pkg/llmproxy/providers/` |
| **Auth** | Credential management | `pkg/llmproxy/auth/` |
| **Rate Limiter** | Throttling | `pkg/llmproxy/ratelimit/` |

### Request Flow

```
1. Client Request → Router
2. Router → Auth Validation
3. Auth → Provider Selection
4. Provider → Upstream API
5. Response ← Provider
6. Metrics → Response
```

### Common Anti-Patterns to Avoid

- **Hardcoded provider URLs** -- Use configuration
- **Blocking on upstream** -- Use timeouts and circuit breakers
- **No fallbacks** -- Implement provider failover
- **Missing metrics** -- Always track latency/cost

---

## Kush Ecosystem

This project is part of the Kush multi-repo system:

```
kush/
├── thegent/         # Agent orchestration
├── agentapi++/      # HTTP API for coding agents
├── cliproxy++/     # LLM proxy (this repo)
├── tokenledger/     # Token and cost tracking
├── 4sgm/           # Python tooling workspace
├── civ/             # Deterministic simulation
├── parpour/         # Spec-first planning
└── pheno-sdk/       # Python SDK
=======
Edit `config.yaml` before starting:

```yaml
# Basic configuration example
server:
  port: 8317

# Add your provider configurations here
```

### Update to Latest Version

```bash
cd ~/cli-proxy
docker compose pull && docker compose up -d
>>>>>>> fix/circular-import-config
```

## Contributing

<<<<<<< HEAD
=======
This project only accepts pull requests that relate to third-party provider support. Any pull requests unrelated to third-party provider support will be rejected.

If you need to submit any non-third-party provider changes, please open them against the [mainline](https://github.com/router-for-me/CLIProxyAPI) repository.

>>>>>>> fix/circular-import-config
## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
