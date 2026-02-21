# Agent Operator Docset

For teams routing autonomous or semi-autonomous agent workloads through `cliproxyapi++`.

## Audience and Goals

- Agent platform owners who need stable latency and high success rates.
- Operators balancing cost, provider quotas, and failover behavior.

## Read This First

1. [Operating Model](./operating-model.md)
2. [Routing and Models Reference](/routing-reference)
3. [Operations API](/api/operations)
4. [Troubleshooting](/troubleshooting)

## Recommended Baseline

- Use explicit model prefixes per agent class (for example `planner/*`, `coder/*`).
- Keep separate API keys for distinct traffic classes.
- Monitor provider metrics and alert on rising error ratio.
- Validate fallback behavior before production rollout.

## Quick Smoke Test

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <agent-client-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "planner/claude-3-5-sonnet",
    "messages": [{"role":"user","content":"Return JSON: {status:ok}"}],
    "temperature": 0,
    "stream": false
  }'
```
