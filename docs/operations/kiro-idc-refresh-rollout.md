# Kiro IDC Refresh Rollout Checklist

Scope: CP2K-0039 (`#136` follow-up).

This guide is for safe rollout of Kiro IDC refresh behavior and compatibility checks.

## Rollout Flags and Switches

- `debug: true` during canary only; disable after verification.
- `request-retry`: keep bounded retry count to avoid repeated refresh storms.
- `max-retry-interval`: keep retry backoff capped for faster recovery visibility.
- `remote-management.secret-key`: must be set so refresh/status routes are callable.

## Migration Sequence

1. Canary one environment with `debug: true`.
1. Trigger provider refresh:
   `POST /v0/management/auths/kiro/refresh`.
1. Confirm token file fields:
   `auth_method`, `client_id`, `client_secret`, `region`, `refresh_token`, `expires_at`.
1. Run one non-stream `/v1/chat/completions` canary request.
1. Run one stream canary request and compare response lifecycle.
1. Disable extra debug logging and proceed to broader rollout.

## Backward-Compatibility Expectations

- Refresh payload keeps both camelCase and snake_case token fields for IDC compatibility.
- Refresh result preserves prior `refresh_token` when upstream omits token rotation.
- Refresh failures include HTTP status and trimmed response body for diagnostics.

## Verification Commands

```bash
curl -sS -X POST http://localhost:8317/v0/management/auths/kiro/refresh \
  -H "Authorization: Bearer <management-key>" | jq
```

```bash
jq '{auth_method, region, expires_at, has_refresh_token:(.refresh_token != "")}' auths/kiro-*.json
```

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"health ping"}],"stream":false}' | jq
```
