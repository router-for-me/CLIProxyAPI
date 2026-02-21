# Technical User Quickstart

A practical runbook to move from fresh install to reliable day-1 operation.

## 1. Start the Service

```bash
docker compose up -d
curl -sS http://localhost:8317/health
```

## 2. Validate Auth and Model Inventory

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <client-key>" | jq '.data[:10]'
```

## 3. Send a Known-Good Request

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet",
    "messages": [{"role":"user","content":"Reply with: operational"}],
    "temperature": 0,
    "stream": false
  }'
```

## 4. Check Runtime Signals

```bash
curl -sS http://localhost:8317/v1/metrics/providers | jq
```

## 5. Management Access (Optional, if enabled)

```bash
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq
```

## Common Day-1 Failures

- `401`: wrong client key.
- Empty model list: provider credential not active or prefix mismatch.
- `429` burst: provider throttled; lower concurrency or add capacity.
- Management `404`: `remote-management.secret-key` not set.

## Next Docs

- [Troubleshooting](/troubleshooting)
- [Routing and Models Reference](/routing-reference)
- [API Index](/api/)
