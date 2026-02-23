# CLIProxyAPI++ (KooshaPari Fork)

This repository works with Claude and other AI agents as autonomous software engineers.

## Quick Start

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
```

---

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
```

---

## License

MIT License - see LICENSE file
