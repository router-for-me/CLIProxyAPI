# Provider Usage

`cliproxyapi++` routes OpenAI-style requests to many provider backends through a unified auth and translation layer.

This page covers provider strategy and high-signal setup patterns. For full block-by-block coverage, use [Provider Catalog](/provider-catalog).

## Audience Guidance

- Use this page if you manage provider credentials and model routing.
- Use [Routing and Models Reference](/routing-reference) for selection behavior details.
- Use [Troubleshooting](/troubleshooting) for runtime failure triage.

## Provider Categories

- Direct APIs: Claude, Gemini, OpenAI, Mistral, Groq, DeepSeek.
- Aggregators: OpenRouter, Together AI, Fireworks AI, Novita AI, SiliconFlow.
- Proprietary/OAuth flows: Kiro, GitHub Copilot, Roo Code, Kilo AI, MiniMax.

## Provider-First Architecture

`cliproxyapi++` keeps one client-facing API (`/v1/*`) and pushes provider complexity into configuration:

1. Inbound auth is validated from top-level `api-keys`.
2. Model names are resolved by prefix + alias.
3. Routing selects provider/credential based on eligibility.
4. Upstream call is translated and normalized back to OpenAI-compatible output.

This lets clients stay stable while provider strategy evolves independently.

## Common Configuration Pattern

Use provider-specific blocks in `config.yaml`:

```yaml
# Client API auth for /v1/*
api-keys:
  - "prod-client-key"

# One direct provider
claude-api-key:
  - api-key: "sk-ant-xxxx"
    prefix: "claude-prod"

# One OpenAI-compatible aggregator
openai-compatibility:
  - name: "openrouter"
    prefix: "or"
    base-url: "https://openrouter.ai/api/v1"
    api-key-entries:
      - api-key: "sk-or-v1-xxxx"
```

## MLX and vLLM-MLX Pattern

For MLX servers that expose OpenAI-compatible APIs (for example `mlx-openai-server` and `vllm-mlx`), configure them under `openai-compatibility`:

```yaml
openai-compatibility:
  - name: "mlx-local"
    prefix: "mlx"
    base-url: "http://127.0.0.1:8000/v1"
    api-key-entries:
      - api-key: "dummy-or-local-key"
```

Then request models through the configured prefix (for example `mlx/<model-id>`), same as other OpenAI-compatible providers.

## Requesting Models

Call standard OpenAI-compatible endpoints:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer prod-client-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-prod/claude-3-5-sonnet",
    "messages": [{"role":"user","content":"Summarize this repository"}],
    "stream": false
  }'
```

Prefix behavior depends on your `prefix` + `force-model-prefix` settings.

## Production Routing Pattern

Use this default design in production:

- Primary direct provider for predictable latency.
- Secondary aggregator provider for breadth/failover.
- Prefix isolation by workload (for example `agent-core/*`, `batch/*`).
- Explicit alias map for client-stable model names.

Example:

```yaml
force-model-prefix: true

claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "agent-core"
    models:
      - name: "claude-3-5-sonnet-20241022"
        alias: "core-sonnet"

openrouter:
  - api-key: "sk-or-v1-..."
    prefix: "batch"
```

## Verify Active Model Inventory

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer prod-client-key" | jq '.data[].id' | head
```

If a model is missing, verify provider block, credential validity, and prefix constraints.

## Rotation and Multi-Credential Guidance

- Add multiple keys per provider to improve resilience.
- Use prefixes to isolate traffic by team or workload.
- Monitor `429` patterns and redistribute traffic before hard outage.
- Keep at least one fallback provider for every critical workload path.

## Failure Modes and Fixes

- Upstream `401/403`: provider key invalid or expired.
- Frequent `429`: provider quota/rate limit pressure; add keys/providers.
- Unexpected provider choice: model prefix mismatch or alias overlap.
- Provider appears unhealthy: inspect operations endpoints and logs.

## Related Docs

- [Provider Catalog](/provider-catalog)
- [Provider Operations](/provider-operations)
- [Routing and Models Reference](/routing-reference)
- [OpenAI-Compatible API](/api/openai-compatible)
- [Features: Providers](/features/providers/USER)
