# Merged Fragmented Markdown

## Source: docsets/developer/external/integration-quickstart.md

# Integration Quickstart

This quickstart gets an external service talking to `cliproxyapi++` with minimal changes.

## 1. Configure Client Base URL and Key

Set your OpenAI SDK/client to:

- Base URL: `http://<cliproxy-host>:8317/v1`
- API key: one entry from `config.yaml -> api-keys`

## 2. Run a Compatibility Check

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <client-key>" | jq '.data[:5]'
```

If this fails, fix auth/config before testing completions.

## 3. Send a Chat Request

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet",
    "messages": [{"role":"user","content":"Generate a short status update."}]
  }'
```

## 4. Add Resilience in Client Code

- Retry idempotent calls with jittered backoff.
- Handle `429` with provider-aware cooldown windows.
- Log response `id` and status for incident correlation.

## 5. Add Runtime Observability

```bash
curl -sS http://localhost:8317/health
curl -sS http://localhost:8317/v1/metrics/providers | jq
```

## Common Integration Pitfalls

- Missing `Authorization` header on `/v1/*` calls.
- Assuming all upstreams support identical model names.
- Hard-coding one provider model without fallback.


---
