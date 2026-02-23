# API Index

`cliproxyapi++` exposes three practical API surfaces: client-compatible runtime APIs, management APIs, and operational APIs.

## Audience Guidance

- Application teams: start with [OpenAI-Compatible API](./openai-compatible.md).
- Platform ops/SRE: add [Operations API](./operations.md) checks.
- Admin tooling: use [Management API](./management.md) with strict access control.

## 1) OpenAI-Compatible API (`/v1/*`)

Common endpoints:

- `POST /v1/chat/completions`
- `POST /v1/completions`
- `GET /v1/models`
- `POST /v1/responses`
- `GET /v1/responses` (websocket bootstrap path)

Use when integrating existing OpenAI-style clients with minimal client changes.

## 2) Management API (`/v0/management/*`)

Use for runtime administration, config/auth inspection, and service controls.

Important: if `remote-management.secret-key` is empty, this surface is disabled.

## 3) Operations API

Operational endpoints include health and metrics surfaces used for monitoring and triage.

- `GET /health`
- `GET /v1/metrics/providers`

## Quick Curl Starter

```bash
# OpenAI-compatible request
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hello"}]}'
```

## Next

- [OpenAI-Compatible API](./openai-compatible.md)
- [Management API](./management.md)
- [Operations API](./operations.md)
