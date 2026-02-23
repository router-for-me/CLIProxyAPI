# CPB-0783 — Gemini 3 Pro Preview HMR Refresh Workflow

Problem context:
`gemini-3-pro-preview` tool failures can leave stale runtime state in long-lived process-compose sessions.

## Deterministic Remediation Steps

1. Rebuild config and clear runtime cache:

```bash
process-compose down
rm -rf .cache/cliproxy
process-compose up -d
```

2. Reload local services after translation rule changes (no full stack restart):

```bash
process-compose restart cliproxy-api
process-compose reload
```

3. Validate with a provider-level sanity check:

```bash
curl -sS -f http://localhost:8317/health
curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq '.data | map(select(.id|contains("gemini-3-pro-preview")))'
```

4. If the failure path persists, capture request/response evidence:

```bash
curl -sS -H "Authorization: Bearer <api-key>" "http://localhost:8317/v0/operations/runtime" | jq
```

## Expected outcome

- `process-compose restart cliproxy-api` applies updated translator/runtime configuration.
- `/v1/models` shows `gemini-3-pro-preview` availability after config reload.

## Escalation

If failures continue, open a follow-up runbook entry with payload + provider ID and attach the output from `/v1/operations/runtime`.
