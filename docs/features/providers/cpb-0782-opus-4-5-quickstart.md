# CPB-0782 — Opus 4.5 Provider Quickstart

## Setup

1. Add the provider credential block to `config.yaml`:

```yaml
claude:
  - api-key: "sk-ant-..."
    prefix: opus
    model: "claude-opus-4.5"
```

2. Reload config:

```bash
curl -sS -X POST http://localhost:8317/v0/management/config/reload
```

## Sanity check

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <api-key>" | jq '.data[] | select(.id|contains("claude-opus-4.5"))'
```

## Test request

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"opus-4.5","messages":[{"role":"user","content":"status check"}]}' | jq
```

## Troubleshooting

- `model not found`: verify alias in config and that `/v1/models` includes `claude-opus-4.5`.
- `auth failed`: confirm active auth key and `prefix` mapping.
- `tooling error`: capture `model` and returned body and re-run config reload.
