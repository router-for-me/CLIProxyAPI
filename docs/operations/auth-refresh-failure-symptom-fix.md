# Auth Refresh Failure Symptom/Fix Table

Use this table when token refresh is failing for OAuth/session-based providers.

| Symptom | How to Confirm | Fix |
| --- | --- | --- |
| Requests return repeated `401` after prior success | Check logs + provider metrics for auth errors | Trigger manual refresh: `POST /v0/management/auths/{provider}/refresh` |
| Manual refresh returns `401` | Verify management key header | Use `Authorization: Bearer <management-key>` or `X-Management-Key` |
| Manual refresh returns `404` | Check if management routes are enabled | Set `remote-management.secret-key`, restart service |
| Refresh appears to run but token stays expired | Inspect auth files + provider-specific auth state | Re-login provider flow to regenerate refresh token |
| Refresh failures spike after config change | Compare active config and recent deploy diff | Roll back auth/provider block changes, then re-apply safely |

## Fast Commands

```bash
# Check management API is reachable
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq

# Trigger a refresh for one provider
curl -sS -X POST http://localhost:8317/v0/management/auths/<provider>/refresh \
  -H "Authorization: Bearer <management-key>" | jq

# Inspect auth file summary
curl -sS http://localhost:8317/v0/management/auth-files \
  -H "Authorization: Bearer <management-key>" | jq
```

## Related

- [Provider Outage Triage Quick Guide](./provider-outage-triage-quick-guide.md)
- [Critical Endpoints Curl Pack](./critical-endpoints-curl-pack.md)

---
Last reviewed: `2026-02-21`  
Owner: `Auth Runtime On-Call`  
Pattern: `YYYY-MM-DD`
