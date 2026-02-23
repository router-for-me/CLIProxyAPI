# Troubleshooting

Use this page to quickly isolate common runtime and integration failures.

## Audience Guidance

- Operators: start with health + logs + model inventory checks.
- Integrators: verify request auth, model name, and endpoint shape first.

## Fast Triage Checklist

```bash
# 1) Is process healthy?
curl -sS http://localhost:8317/health

# 2) Is API key auth configured correctly?
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_KEY"

# 3) What models are actually exposed?
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_KEY" | jq '.data[].id' | head

# 4) Any provider-side stress?
curl -sS http://localhost:8317/v1/metrics/providers | jq
```

## Troubleshooting Matrix

| Symptom | Likely Cause | Immediate Check | Remediation |
| --- | --- | --- | --- |
| `Error 401` on request | Missing or rotated client API key | `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer API_KEY"` | Update key in `api-keys`, restart, verify no whitespace in config |
| `403` from provider upstream | License/subscription or permission mismatch | Search logs for `status_code":403` in provider module | Align account entitlement, retry with fallback-capable model, inspect provider docs |
| `Invalid JSON payload ... tool_result has no content field` | Upstream/client emitted sparse `tool_result` content block shape | Reproduce with one minimal payload and inspect translated request in logs | Upgrade to a build with sparse `tool_result` normalization; as a temporary workaround, send `tool_result.content` as `[]` |
| `Docker Image Error` on startup/health | Image tag mismatch, stale config mount, or incompatible env defaults | `docker images | head`, `docker logs CONTAINER_NAME --tail 200`, `/health` check | Pull/pin a known-good tag, verify mounted `config.yaml`, then compare `stream: true/false` behavior for parity |
| `Model not found` / `bad model` | Alias/prefix/model map mismatch | `curl .../v1/models` and compare requested ID | Update alias map, prefix rules, and `excluded-models` |
| Gemini 3 Flash `includeThoughts` appears ignored | Mixed `includeThoughts`/`include_thoughts` or mode mismatch | Inspect incoming `generationConfig.thinkingConfig` and verify reasoning mode | Send one explicit variant (`includeThoughts` preferred); proxy normalizes snake_case to camelCase before upstream |
| Gemini `400` with `defer_loading` in `ToolSearch` | Unsupported `google_search.defer_loading` propagated from client payload | Re-run request with same `tools` block and inspect translated request path | Upgrade to build with ToolSearch sanitization; `defer_loading`/`deferLoading` are stripped for Gemini/Gemini-CLI/Antigravity |
| `gpt-5.3-codex-spark` fails for plus/team | Account tier does not expose Spark model even if config lists it | `GET /v1/models` and look for `gpt-5.3-codex-spark` | Route to `gpt-5.3-codex` fallback and alert on repeated Spark 400/404 responses |
| Gemini CLI/Gemini 3 returns `404 Requested entity was not found` | Model alias maps to non-exposed upstream model or wrong provider prefix | `GET /v1/models` for same client key, then run one non-stream request for the same model | Correct alias/prefix mapping, validate non-stream first, then confirm stream parity |
| NVIDIA OpenAI-compatible upstream connect failures | Stream/non-stream request shape mismatch or provider-side path/header expectations | Run back-to-back non-stream and stream `POST /v1/chat/completions` with identical model/message payload | Keep payload/model constant; if non-stream passes and stream fails, focus on SSE/proxy path; if both fail, verify provider base URL/model exposure/auth first |
| Runtime config write errors | Read-only mount or immutable filesystem | `find /CLIProxyAPI -maxdepth 1 -name config.yaml -print` | Use writable mount, re-run with read-only warning, confirm management persistence status |
| Kiro/OAuth auth loops | Expired or missing token refresh fields | Re-run `cliproxyapi++ auth`/reimport token path | Refresh credentials, run with fresh token file, avoid duplicate token imports |
| Streaming hangs or truncation | Reverse proxy buffering / payload compatibility issue | Reproduce with `stream: false`, then compare SSE response | Verify reverse-proxy config, compare tool schema compatibility and payload shape |
| `Cannot use Claude Models in Codex CLI` | Missing oauth alias bridge for Claude model IDs | `curl -sS .../v1/models | jq '.data[].id' | rg 'claude-opus|claude-sonnet|claude-haiku'` | Add/restore `oauth-model-alias` entries (or keep default injection enabled), then reload and re-check `/v1/models` |
| `claude-opus-4-6` missing or returns `bad model` | Alias/prefix mapping is stale after Claude model refresh | `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer YOUR_CLIENT_KEY" | jq -r '.data[].id' | rg 'claude-opus-4-6|claude-sonnet-4-6'` | Update `claude-api-key` model alias mappings, reload config, then re-run non-stream Opus 4.6 request before stream rollout |
| `/v1/responses/compact` fails or hangs | Wrong endpoint/mode expectations (streaming not supported for compact) | Retry with non-stream `POST /v1/responses/compact` and inspect JSON `object` field | Use compact only in non-stream mode; for streaming flows keep `/v1/responses` or `/v1/chat/completions` |
| MCP memory tools fail (`tool not found`, invalid params, or empty result) | MCP server missing memory tool registration or request schema mismatch | Run `tools/list` then one minimal `tools/call` against the same MCP endpoint | Enable/register memory tools, align `tools/call` arguments to server schema, then repeat `tools/list` and `tools/call` smoke tests |

Use this matrix as an issue-entry checklist:

```bash
for endpoint in health models v1/metrics/providers v0/management/logs; do
  curl -sS "http://localhost:8317/$endpoint" -H "Authorization: Bearer YOUR_API_KEY" | head -n 3
done
```

## Service Does Not Start

Checks:

- Config path in container/host is correct and readable.
- `config.yaml` syntax is valid.
- Port is not already used by another process.

Commands:

```bash
docker logs cliproxyapi-plusplus --tail 200
lsof -iTCP:8317 -sTCP:LISTEN
```

## `401 Unauthorized` on `/v1/*`

Checks:

- Send `Authorization: Bearer API_KEY`.
- Confirm key exists in `api-keys` list in `config.yaml`.
- Remove leading/trailing spaces in key value.

## Management API Returns `404`

Likely cause:

- `remote-management.secret-key` is empty, so `/v0/management/*` routes are disabled.

Fix:

```yaml
remote-management:
  secret-key: "set-a-strong-key"
```

Then restart the service.

## `429` and Rate-Limit Cascades

Checks:

- Inspect provider metrics and logs for sustained throttling.
- Add additional credentials/provider capacity.
- Reduce concurrency or enable stronger client backoff.

## Provider `403` Fast Path

Use this for repeated `403` on Kiro/Copilot/Antigravity-like channels:

```bash
# 1) Verify model is exposed to the current key
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <your-client-key>" | jq '.data[].id' | head -n 20

# 2) Run a minimal non-stream request for the same model
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <your-client-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"<model-id>","messages":[{"role":"user","content":"ping"}],"stream":false}'

# 3) Inspect provider metrics + recent logs for status bursts
curl -sS http://localhost:8317/v1/metrics/providers \
  -H "Authorization: Bearer <your-client-key>" | jq
```

If step (2) fails with `403` while model listing works, treat it as upstream entitlement/channel policy mismatch first, not model registry corruption.

## Model Not Found / Unsupported Model

Checks:

- Confirm model appears in `/v1/models` for current API key.
- Verify prefix requirements (for example `team-a/model`).
- Check alias and excluded-model rules in provider config.

## Streaming Issues (SSE/WebSocket)

Checks:

- Confirm reverse proxies do not buffer SSE.
- For `/v1/responses` websocket scenarios, verify auth headers are forwarded.
- Increase upstream/request timeout where ingress is aggressive.

### Claude Code Appears Non-Streaming (Chunks arrive all at once)

Use this quick isolate flow:

```bash
# Compare non-stream vs stream behavior against same model
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"ping"}],"stream":false}' | jq '.choices[0].message.content'

curl -N -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"ping"}],"stream":true}'
```

If non-stream succeeds but stream chunks are delayed/batched:
- check reverse proxy buffering settings first,
- verify client reads SSE incrementally,
- confirm no middleware rewrites the event stream response.

## Useful Endpoints

- `GET /health`
- `GET /v1/models`
- `GET /v1/metrics/providers`
- `GET /v0/management/config` (if management enabled)
- `GET /v0/management/logs` (if management enabled)

## Related Docs

- [Getting Started](/getting-started)
- [Provider Usage](/provider-usage)
- [Routing and Models Reference](/routing-reference)
- [API Index](/api/)
