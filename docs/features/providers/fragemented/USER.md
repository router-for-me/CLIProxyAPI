# User Guide: Providers

This guide explains provider configuration using the current `cliproxyapi++` config schema.

## Core Model

- Client sends requests to OpenAI-compatible endpoints (`/v1/*`).
- `cliproxyapi++` resolves model -> provider/credential based on prefix + aliases.
- Provider blocks in `config.yaml` define auth, base URL, and model exposure.

## Current Provider Configuration Patterns

### Direct provider key

```yaml
claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "claude-prod"
```

### Aggregator provider

```yaml
openrouter:
  - api-key: "sk-or-v1-..."
    base-url: "https://openrouter.ai/api/v1"
    prefix: "or"
```

### OpenAI-compatible provider registry

```yaml
openai-compatibility:
  - name: "openrouter"
    prefix: "or"
    base-url: "https://openrouter.ai/api/v1"
    api-key-entries:
      - api-key: "sk-or-v1-..."
```

### OAuth/session provider

```yaml
kiro:
  - token-file: "~/.aws/sso/cache/kiro-auth-token.json"
```

## Operational Best Practices

- Use `force-model-prefix: true` to enforce explicit routing boundaries.
- Keep at least one fallback provider for each critical workload.
- Use `models` + `alias` to keep client model names stable.
- Use `excluded-models` to hide risky/high-cost models from consumers.

## Validation Commands

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <api-key>" | jq '.data[:10]'

curl -sS http://localhost:8317/v1/metrics/providers | jq
```

## Deep Dives

- [Provider Usage](/provider-usage)
- [Provider Catalog](/provider-catalog)
- [Provider Operations](/provider-operations)
- [Routing and Models Reference](/routing-reference)
