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

## Claude Compatibility Notes (`#145` scope)

- Use canonical OpenAI chat payload shape: `messages[].role` + `messages[].content`.
- Avoid mixing `/v1/responses` payload fields into `/v1/chat/completions` requests in the same call.
- If you use model aliases for Claude, verify the alias resolves in `GET /v1/models` before testing chat.
- For conversion debugging, run one non-stream request first, then enable streaming once format parity is confirmed.

## Common Failure Modes

- `401`: missing/invalid client API key.
- `404`: wrong path (use `/v1/...` exactly).
- `429`: upstream provider throttling; add backoff and provider capacity.
- `400 model_not_found`: alias/prefix/config mismatch.
- `400` with schema/field errors: payload shape mismatch between OpenAI chat format and provider-specific fields.

## Related Docs

- [Provider Usage](/provider-usage)
- [Routing and Models Reference](/routing-reference)
- [Troubleshooting](/troubleshooting)
