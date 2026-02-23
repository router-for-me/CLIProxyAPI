# CPB-0786 — Nano Banana Quickstart

## Setup

1. Add Nano Banana credentials in your provider block.
2. Restart or reload config after key updates.
3. Validate discovery:

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <api-key>" | jq '.data[] | select(.id|contains("nano-banana"))'
```

## Copy-paste request

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"nano-banana","messages":[{"role":"user","content":"Quick health-check request"}]}'
```

## Troubleshooting

- If responses show only partial tokens, check model mapping in config and alias collisions.
- If requests fail with structured tool errors, simplify payload to a plain text request and re-test.
- If metadata drifts after deployment, restart process-compose and re-query `/v1/models`.
