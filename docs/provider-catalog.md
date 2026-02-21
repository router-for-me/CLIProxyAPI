# Provider Catalog

This page is the provider-first reference for `cliproxyapi++`: what each provider block is for, how to configure it, and when to use it.

## Provider Groups

| Group | Primary Use | Config Blocks |
| --- | --- | --- |
| Direct APIs | Lowest translation overhead, direct vendor features | `claude-api-key`, `gemini-api-key`, `codex-api-key`, `deepseek`, `groq`, `mistral` |
| Aggregators | Broad model inventory under one account | `openrouter`, `together`, `fireworks`, `novita`, `siliconflow`, `openai-compatibility` |
| OAuth / Session Flows | IDE-style account login and managed refresh | `kiro`, `cursor`, `minimax`, `roo`, `kilo`, `ampcode` |
| Compatibility Endpoints | OpenAI-shaped upstream endpoints | `openai-compatibility`, `vertex-api-key` |

## Minimal Provider Patterns

### 1) Direct vendor key

```yaml
claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "claude-prod"
```

### 2) Aggregator provider

```yaml
openrouter:
  - api-key: "sk-or-v1-..."
    base-url: "https://openrouter.ai/api/v1"
    prefix: "or"
```

### 3) OpenAI-compatible provider registry

```yaml
openai-compatibility:
  - name: "openrouter"
    prefix: "or"
    base-url: "https://openrouter.ai/api/v1"
    api-key-entries:
      - api-key: "sk-or-v1-..."
```

### 4) OAuth/session provider

```yaml
kiro:
  - token-file: "~/.aws/sso/cache/kiro-auth-token.json"
```

## Prefixing and Model Scope

- `prefix` isolates traffic per credential/provider (for example `prod/claude-3-5-sonnet`).
- `force-model-prefix: true` enforces explicit provider routing.
- `models` with `alias` gives client-stable names while preserving upstream model IDs.
- `excluded-models` prevents unsafe or expensive models from appearing in `/v1/models`.

## Provider Selection Guide

| Goal | Recommended Pattern |
| --- | --- |
| Predictable latency | Prefer direct providers (`claude-api-key`, `gemini-api-key`, `codex-api-key`) |
| Broad fallback options | Add one aggregator (`openrouter` or `openai-compatibility`) |
| Team/workload isolation | Use provider `prefix` and `force-model-prefix: true` |
| Zero-downtime auth | Use OAuth/session providers with token file refresh (`kiro`, `cursor`, `minimax`) |
| Lowest ops friction | Standardize all non-direct integrations under `openai-compatibility` |

## Validation Checklist

1. `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq '.data[].id'`
2. Ensure required prefixes are visible in returned model IDs.
3. Issue one request per critical model path.
4. Check metrics: `curl -sS http://localhost:8317/v1/metrics/providers | jq`.
5. Confirm no sustained `429` or `401/403` on target providers.

## Related Docs

- [Provider Usage](/provider-usage)
- [Provider Operations](/provider-operations)
- [Routing and Models Reference](/routing-reference)
- [OpenAI-Compatible API](/api/openai-compatible)
