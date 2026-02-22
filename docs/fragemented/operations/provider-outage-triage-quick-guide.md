# Provider Outage Triage Quick Guide

Use this quick guide when a provider starts failing or latency spikes.

## 5-Minute Flow

1. Confirm process health:
   - `curl -sS -f http://localhost:8317/health`
2. Confirm exposed models still look normal:
   - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq '.data | length'`
3. Inspect provider metrics for the failing provider:
   - `curl -sS http://localhost:8317/v1/metrics/providers | jq`
4. Check logs for repeated status codes (`401`, `403`, `429`, `5xx`).
5. Reroute critical traffic to fallback prefix/provider.

## Decision Hints

| Symptom | Likely Cause | Immediate Action |
| --- | --- | --- |
| One provider has high error ratio, others healthy | Upstream outage/degradation | Shift traffic to fallback provider prefix |
| Mostly `401/403` | Expired/invalid provider auth | Run auth refresh checks and manual refresh |
| Mostly `429` | Upstream throttling | Lower concurrency and shift non-critical traffic |
| `/v1/models` missing expected models | Provider config/auth problem | Recheck provider block, auth file, and filters |

## Escalation Trigger

Escalate after 10 minutes if any one is true:

- No successful requests for a critical workload.
- Error ratio remains above on-call threshold after reroute.
- Two independent providers are simultaneously degraded.

## Related

- [Critical Endpoints Curl Pack](./critical-endpoints-curl-pack.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `Platform On-Call`  
Pattern: `YYYY-MM-DD`
