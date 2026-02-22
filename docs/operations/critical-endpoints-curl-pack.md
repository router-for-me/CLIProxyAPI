# Critical Endpoints Curl Pack

Copy/paste pack for first-response checks.

## Runtime Canonical Probes

```bash
# Health probe
curl -sS -f http://localhost:8317/health | jq

# Operations provider status
curl -sS -f http://localhost:8317/v0/operations/providers/status | jq

# Operations load-balancing status
curl -sS -f http://localhost:8317/v0/operations/load_balancing/status | jq

# Runtime metrics surface (canonical unauth probe)
curl -sS -f http://localhost:8317/v1/metrics/providers | jq

# Exposed models (requires API key)
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <api-key>" | jq '.data[:10]'
```

## Management Safety Checks

```bash
# Effective runtime config
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq

# Auth files snapshot
curl -sS http://localhost:8317/v0/management/auth-files \
  -H "Authorization: Bearer <management-key>" | jq

# Recent logs
curl -sS "http://localhost:8317/v0/management/logs?lines=200" \
  -H "Authorization: Bearer <management-key>"
```

## Auth Refresh Action

```bash
curl -sS -X POST \
  http://localhost:8317/v0/management/auths/<provider>/refresh \
  -H "Authorization: Bearer <management-key>" | jq
```

## Deprecated Probes (Not Implemented In Runtime Yet)

```bash
# Deprecated: cooldown endpoints are not currently registered
curl -sS http://localhost:8317/v0/operations/cooldown/status
```

## Use With

- [Provider Outage Triage Quick Guide](./provider-outage-triage-quick-guide.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `SRE`  
Pattern: `YYYY-MM-DD`
