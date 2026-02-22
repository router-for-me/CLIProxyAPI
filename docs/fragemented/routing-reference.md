# Routing and Models Reference

This page explains how `cliproxyapi++` selects credentials/providers and resolves model names.

## Audience Guidance

- Platform operators tuning reliability and quota usage.
- Developers debugging model resolution and fallback behavior.

## Request Flow

1. Client sends an OpenAI-compatible request to `/v1/*`.
2. API key auth is checked (`Authorization: Bearer <client-key>`).
3. Model name is resolved against configured providers, prefixes, and aliases.
4. Credential/provider is chosen by routing strategy.
5. Upstream request is translated and executed.
6. Response is normalized back to OpenAI-compatible JSON/SSE.

## Routing Controls in `config.yaml`

```yaml
routing:
  strategy: "round-robin" # round-robin | fill-first

force-model-prefix: false
request-retry: 3
max-retry-interval: 30
quota-exceeded:
  switch-project: true
  switch-preview-model: true
```

## Model Prefix and Alias Behavior

- A credential/provider prefix (for example `team-a`) can require requests like `team-a/model-name`.
- With `force-model-prefix: true`, unprefixed model calls are restricted.
- Per-provider alias mappings can translate client-stable names to upstream names.

Example alias configuration:

```yaml
codex-api-key:
  - api-key: "sk-xxxx"
    models:
      - name: "gpt-5-codex"
        alias: "codex-latest"
```

Client request:

```json
{ "model": "codex-latest", "messages": [{"role":"user","content":"hi"}] }
```

## Metrics and Routing Diagnosis

```bash
# Per-provider rolling stats
curl -sS http://localhost:8317/v1/metrics/providers | jq

# Runtime health
curl -sS http://localhost:8317/health
```

Use these signals with logs to confirm if retries, throttling, or auth issues are driving fallback.

## Common Routing Failure Modes

- `model_not_found`: model alias/prefix not exposed by configured credentials.
- Wrong provider selected: prefix overlap or non-explicit model name.
- High latency spikes: provider degraded; add retries or alternate providers.
- Repeated `429`: insufficient credential pool for traffic profile.

## Related Docs

- [Provider Usage](/provider-usage)
- [Operations API](/api/operations)
- [Troubleshooting](/troubleshooting)
