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
| `request body too large` / payload near `280KB` fails | Upstream body limit reached for selected route/model | Replay with same request using smaller prompt (`<200KB`) and inspect response code/body | Split long context into chunks, move large artifacts to files/URLs, and retry with reduced inline payload size |
| `502 unknown provider for model gemini-claude-opus-4-6-thinking` | Missing `oauth-model-alias` bridge for Antigravity thinking model IDs | `curl -sS .../v1/models | jq -r '.data[].id' | rg 'gemini-claude-opus-4-6-thinking|claude-opus-4-6-thinking'` | Add/restore `oauth-model-alias.antigravity` mappings, reload config, and verify `/v1/models` before retry |
| `gpt-5.3-codex-spark` fails for plus/team | Account tier does not expose Spark model even if config lists it | `GET /v1/models` and look for `gpt-5.3-codex-spark` | Route to `gpt-5.3-codex` fallback and alert on repeated Spark 400/404 responses |
| `iflow` `glm-4.7` returns `406` | Request format/headers do not match IFlow acceptance rules for that model | Retry once with non-stream mode and capture response body + headers | Pin to known-working alias for `glm-4.7`, normalize request format, and keep fallback model route available |
| iFlow OAuth login succeeded but most iFlow models unavailable | Account currently exposes only a non-CLI subset (model inventory mismatch) | `GET /v1/models` and filter `^iflow/` | Route only to listed `iflow/*` IDs; avoid requesting non-listed upstream aliases; keep one known-good canary model |
| Usage statistics remain `0` after many requests | Upstream omitted usage metadata in stream/non-stream responses | Compare one `stream:false` and one `stream:true` canary and inspect `usage` fields/logs | Keep request counting enabled via server-side usage fallback; validate parity with both request modes before rollout |
| Gemini via OpenAI-compatible client cannot control thinking length | Thinking controls were dropped/normalized unexpectedly before provider dispatch | Compare request payload vs debug logs for `thinking: original config` and `thinking: processed config` | Use explicit thinking suffix/level supported by exposed model, enforce canary checks, and alert when processed thinking mode mismatches request intent |
| `codex5.3` availability unclear across environments | Integration path mismatch (in-process SDK vs remote HTTP fallback) | Probe `/health` then `/v1/models`, verify `gpt-5.3-codex` exposure | Use in-process `sdk/cliproxy` when local runtime is controlled; fall back to `/v1/*` only when process boundaries require HTTP |
| Amp requests bypass CLIProxyAPI | Amp process missing `OPENAI_API_BASE`/`OPENAI_API_KEY` or stale shell env | Run direct canary to `http://127.0.0.1:8317/v1/chat/completions` with same credentials | Export env in the same process/shell that launches Amp, then verify proxy logs show Amp traffic |
| Runtime config write errors | Read-only mount or immutable filesystem | `find /CLIProxyAPI -maxdepth 1 -name config.yaml -print` | Use writable mount, re-run with read-only warning, confirm management persistence status |
| Kiro/OAuth auth loops | Expired or missing token refresh fields | Re-run `cliproxyapi++ auth`/reimport token path | Refresh credentials, run with fresh token file, avoid duplicate token imports |
| Streaming hangs or truncation | Reverse proxy buffering / payload compatibility issue | Reproduce with `stream: false`, then compare SSE response | Verify reverse-proxy config, compare tool schema compatibility and payload shape |
| `Cannot use Claude Models in Codex CLI` | Missing oauth alias bridge for Claude model IDs | `curl -sS .../v1/models | jq '.data[].id' | rg 'claude-opus|claude-sonnet|claude-haiku'` | Add/restore `oauth-model-alias` entries (or keep default injection enabled), then reload and re-check `/v1/models` |
| `Qwen Free allocated quota exceeded` | Current Qwen credential/project exhausted | Check current quota behavior and provider error ratio in logs/metrics | Switch to alternate credential/project, enable quota-exceeded fallback toggles, and retry with reduced concurrency |
| `/v1/responses/compact` fails or hangs | Wrong endpoint/mode expectations (streaming not supported for compact) | Retry with non-stream `POST /v1/responses/compact` and inspect JSON `object` field | Use compact only in non-stream mode; for streaming flows keep `/v1/responses` or `/v1/chat/completions` |

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
