# Checks-to-Owner Responder Map

Route each failing check to the fastest owner path.

| Check | Primary Owner | Secondary Owner | First Response |
| --- | --- | --- | --- |
| `GET /health` fails | Runtime On-Call | Platform On-Call | Verify process/pod status, restart if needed |
| `GET /v1/models` fails/auth errors | Auth Runtime On-Call | Platform On-Call | Validate API key, provider auth files, refresh path |
| `GET /v1/metrics/providers` shows one provider degraded | Platform On-Call | Provider Integrations | Shift traffic to fallback prefix/provider |
| `GET /v0/management/config` returns `404` | Platform On-Call | Runtime On-Call | Enable `remote-management.secret-key`, restart |
| `POST /v0/management/auths/{provider}/refresh` fails | Auth Runtime On-Call | Provider Integrations | Validate management key, rerun provider auth login |
| Logs show sustained `429` | Platform On-Call | Capacity Owner | Reduce concurrency, add credentials/capacity |

## Paging Guidelines

1. Page primary owner immediately when critical user traffic is impacted.
2. Add secondary owner if no mitigation within 10 minutes.
3. Escalate incident lead when two or more critical checks fail together.

## Related

- [Provider Outage Triage Quick Guide](./provider-outage-triage-quick-guide.md)
- [Auth Refresh Failure Symptom/Fix Table](./auth-refresh-failure-symptom-fix.md)

---
Last reviewed: `2026-02-21`  
Owner: `Incident Commander Rotation`  
Pattern: `YYYY-MM-DD`
