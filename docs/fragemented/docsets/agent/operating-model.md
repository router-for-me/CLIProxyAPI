# Agent Operating Model

This model describes how to run agent traffic safely through `cliproxyapi++`.

## Control Loop

1. Accept agent request on `/v1/*` with API key auth.
2. Resolve model prefix/alias and eligible providers.
3. Select credential by routing strategy and runtime health.
4. Execute upstream call with retries and provider translation.
5. Return normalized response and emit metrics/log events.

## Deployment Pattern

- One shared proxy per environment (`dev`, `staging`, `prod`).
- API keys segmented by agent type or team.
- Prefix-based model policy to prevent accidental cross-traffic.

Example config fragment:

```yaml
api-keys:
  - "agent-planner-key"
  - "agent-coder-key"

routing:
  strategy: "round-robin"

force-model-prefix: true
```

## Operational Guardrails

- Alert on 401/429/5xx trends per provider.
- Keep at least one fallback provider for critical agent classes.
- Test with synthetic prompts on each deploy.
- Keep management access on localhost/private network only.

## Failure Drills

- Simulate provider throttling and verify fallback.
- Rotate one credential and confirm zero-downtime behavior.
- Force model prefix mismatch and validate explicit error handling.

## Useful Commands

```bash
curl -sS http://localhost:8317/health
curl -sS http://localhost:8317/v1/metrics/providers | jq
curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <agent-key>" | jq '.data[].id' | head
```
