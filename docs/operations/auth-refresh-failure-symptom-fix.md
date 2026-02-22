# Auth Refresh Failure Symptom/Fix Table

Use this table when token refresh is failing for OAuth/session-based providers.

| Symptom | How to Confirm | Fix |
| --- | --- | --- |
| Requests return repeated `401` after prior success | Check logs + provider metrics for auth errors | Trigger manual refresh: `POST /v0/management/auths/{provider}/refresh` |
| Manual refresh returns `401` | Verify management key header | Use `Authorization: Bearer <management-key>` or `X-Management-Key` |
| Manual refresh returns `404` | Check if management routes are enabled | Set `remote-management.secret-key`, restart service |
| Refresh appears to run but token stays expired | Inspect auth files + provider-specific auth state | Re-login provider flow to regenerate refresh token |
| Refresh failures spike after config change | Compare active config and recent deploy diff | Roll back auth/provider block changes, then re-apply safely |
| `iflow executor: token refresh failed` (or similar OAuth refresh errors) | Check auth record has non-empty `refresh_token` and recent `expired` timestamp | Follow provider-agnostic sequence: re-login -> management refresh -> one canary `/v1/chat/completions` before reopening traffic |
| Kiro IDC refresh fails with `400/401` repeatedly (`#149` scope) | Confirm `auth_method=idc` token has `client_id`, `client_secret`, `region`, and `refresh_token` | Re-login with `--kiro-aws-authcode` or `--kiro-aws-login`; verify refreshed token file fields before re-enabling traffic |
| Kiro login account selection seems ignored (`#102` scope) | Check logs for `kiro: using normal browser mode (--no-incognito)` | Remove `--no-incognito` unless reusing an existing session is intended; default incognito mode is required for clean multi-account selection |
| Manual status appears stale after refresh (`#136` scope) | Compare token file `expires_at` and management refresh response | Trigger refresh endpoint, then reload config/watcher if needed and confirm `expires_at` moved forward |

## Fast Commands

```bash
# Check management API is reachable
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq

# Trigger a refresh for one provider
curl -sS -X POST http://localhost:8317/v0/management/auths/<provider>/refresh \
  -H "Authorization: Bearer <management-key>" | jq

# Kiro specific refresh check (replace file name with your auth file)
jq '{auth_method, region, expires_at, has_refresh_token:(.refresh_token != "")}' \
  auths/kiro-*.json

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
