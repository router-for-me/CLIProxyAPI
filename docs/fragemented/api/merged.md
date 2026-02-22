# Merged Fragmented Markdown

## Source: api/management.md

# Management API

Management endpoints provide runtime inspection and administrative controls.

## Access Model

- Surface path: `/v0/management/*`
- Protected by management key.
- Disabled entirely when `remote-management.secret-key` is empty.

### Enable and Protect Management Access

```yaml
remote-management:
  allow-remote: false
  secret-key: "replace-with-strong-secret"
```

Use either header style:

- `Authorization: Bearer <management-key>`
- `X-Management-Key: <management-key>`

## Common Endpoints

- `GET /v0/management/config`
- `GET /v0/management/config.yaml`
- `GET /v0/management/auth-files`
- `GET /v0/management/logs`
- `POST /v0/management/api-call`

Note: some management routes are provider/tool-specific and may vary by enabled features.

## Practical Examples

Read effective config:

```bash
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq
```

Inspect auth file summary:

```bash
curl -sS http://localhost:8317/v0/management/auth-files \
  -H "X-Management-Key: <management-key>" | jq
```

Tail logs stream/snapshot:

```bash
curl -sS "http://localhost:8317/v0/management/logs?lines=200" \
  -H "Authorization: Bearer <management-key>"
```

## Failure Modes

- `404` on all management routes: management disabled (empty secret key).
- `401`: invalid or missing management key.
- `403`: remote request blocked when `allow-remote: false`.
- `500`: malformed config/auth state causing handler errors.

## Operational Guidance

- Keep `allow-remote: false` unless absolutely required.
- Place management API behind private network or VPN.
- Rotate management key and avoid storing it in shell history.

## Related Docs

- [Operations API](./operations.md)
- [Troubleshooting](/troubleshooting)


---

## Source: api/openai-compatible.md

# OpenAI-Compatible API

These endpoints are designed for OpenAI-style client compatibility while routing through `cliproxyapi++` provider logic.

## Base URL

```text
http://<host>:8317
```

## Authentication

`/v1/*` routes require a configured client API key:

```http
Authorization: Bearer <api-key-from-config.yaml-api-keys>
```

## Endpoints

### `POST /v1/chat/completions`

Use for chat-style generation.

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer dev-local-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet",
    "messages": [{"role": "user", "content": "Give me 3 release notes bullets"}],
    "temperature": 0.2,
    "stream": false
  }'
```

Example response shape:

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "created": 1730000000,
  "model": "claude-3-5-sonnet",
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "..."},
      "finish_reason": "stop"
    }
  ],
  "usage": {"prompt_tokens": 10, "completion_tokens": 42, "total_tokens": 52}
}
```

### `POST /v1/completions`

Legacy completion-style flow for clients that still use text completion payloads.

### `POST /v1/responses`

Responses-style payload support for compatible clients/workloads.

### `GET /v1/models`

Lists models visible under current configuration and auth context.

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer dev-local-key" | jq '.data[:10]'
```

## Streaming Guidance

- For SSE, set `"stream": true` on `chat/completions`.
- Ensure reverse proxies do not buffer event streams.
- If clients hang, verify ingress/edge idle timeouts.

## Common Failure Modes

- `401`: missing/invalid client API key.
- `404`: wrong path (use `/v1/...` exactly).
- `429`: upstream provider throttling; add backoff and provider capacity.
- `400 model_not_found`: alias/prefix/config mismatch.

## Related Docs

- [Provider Usage](/provider-usage)
- [Routing and Models Reference](/routing-reference)
- [Troubleshooting](/troubleshooting)


---

## Source: api/operations.md

# Operations API

Operations endpoints are used for liveness checks, routing visibility, and incident triage.

## Audience Guidance

- SRE/ops: integrate these routes into health checks and dashboards.
- Developers: use them when debugging routing/performance behavior.

## Core Endpoints

- `GET /health` for liveness/readiness style checks.
- `GET /v1/metrics/providers` for rolling provider-level performance/usage stats.

## Monitoring Examples

Basic liveness check:

```bash
curl -sS -f http://localhost:8317/health
```

Provider metrics snapshot:

```bash
curl -sS http://localhost:8317/v1/metrics/providers | jq
```

Prometheus-friendly probe command:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://localhost:8317/health
```

## Suggested Operational Playbook

1. Check `/health` first.
2. Inspect `/v1/metrics/providers` for latency/error concentration.
3. Correlate with request logs and model-level failures.
4. Shift traffic (prefix/model/provider) when a provider degrades.

## Failure Modes

- Health endpoint flaps: resource saturation or startup race.
- Provider metrics stale/empty: no recent traffic or exporter initialization issues.
- High error ratio on one provider: auth expiry, upstream outage, or rate-limit pressure.

## Related Docs

- [Routing and Models Reference](/routing-reference)
- [Troubleshooting](/troubleshooting)
- [Management API](./management.md)


---
