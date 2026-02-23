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
| `iflow` `glm-4.7` returns `406` | Request format/headers do not match IFlow acceptance rules for that model | Retry once with non-stream mode and capture response body + headers | Pin to known-working alias for `glm-4.7`, normalize request format, and keep fallback model route available |
| iFlow OAuth login succeeded but most iFlow models unavailable | Account currently exposes only a non-CLI subset (model inventory mismatch) | `GET /v1/models` and filter `^iflow/` | Route only to listed `iflow/*` IDs; avoid requesting non-listed upstream aliases; keep one known-good canary model |
| Usage statistics remain `0` after many requests | Upstream omitted usage metadata in stream/non-stream responses | Compare one `stream:false` and one `stream:true` canary and inspect `usage` fields/logs | Keep request counting enabled via server-side usage fallback; validate parity with both request modes before rollout |
| Kiro remaining quota unknown or near exhaustion | Wrong auth credential exhausted or stale quota signal | `curl -sS http://localhost:8317/v0/management/kiro-quota -H "Authorization: Bearer <management-key>" | jq` | Find `auth_index`, confirm `quota_exhausted` and `remaining_quota`, then enable quota-fallback switches and rotate to alternate credentials |
| Gemini via OpenAI-compatible client cannot control thinking length | Thinking controls were dropped/normalized unexpectedly before provider dispatch | Compare request payload vs debug logs for `thinking: original config` and `thinking: processed config` | Use explicit thinking suffix/level supported by exposed model, enforce canary checks, and alert when processed thinking mode mismatches request intent |
| `Gemini CLI OAuth 认证失败: failed to start callback server` | Default callback port `8085` is already bound on localhost | `lsof -iTCP:8085` or `ss -tnlp | grep 8085` | Stop the conflicting server, or re-run `cliproxyctl login --oauth-callback-port <free-port>`; the CLI now also falls back to an ephemeral port and prints the final callback URL/SSH tunnel instructions. |
| `codex5.3` availability unclear across environments | Integration path mismatch (in-process SDK vs remote HTTP fallback) | Probe `/health` then `/v1/models`, verify `gpt-5.3-codex` exposure | Use in-process `sdk/cliproxy` when local runtime is controlled; fall back to `/v1/*` only when process boundaries require HTTP |
| Amp requests bypass CLIProxyAPI | Amp process missing `OPENAI_API_BASE`/`OPENAI_API_KEY` or stale shell env | Run direct canary to `http://127.0.0.1:8317/v1/chat/completions` with same credentials | Export env in the same process/shell that launches Amp, then verify proxy logs show Amp traffic |
| `auth-dir` mode is too permissive (`0755`/`0777`) | OAuth/API key login writes fail fast due insecure permissions | `ls -ld ~/.cli-proxy-api` or `ls -ld <configured auth-dir>` | Run `chmod 700` on the configured auth directory, then retry the login/refresh command |
| Runtime config write errors | Read-only mount or immutable filesystem | `find /CLIProxyAPI -maxdepth 1 -name config.yaml -print` | Use writable mount, re-run with read-only warning, confirm management persistence status |
| Kiro/OAuth auth loops | Expired or missing token refresh fields | Re-run `cliproxyapi++ auth`/reimport token path | Refresh credentials, run with fresh token file, avoid duplicate token imports |
| Streaming hangs or truncation | Reverse proxy buffering / payload compatibility issue | Reproduce with `stream: false`, then compare SSE response | Verify reverse-proxy config, compare tool schema compatibility and payload shape |
| `Cherry Studio can't find the model even though CLI runs succeed` (CPB-0373) | Workspace-specific model filters (Cherry Studio) do not include the alias/prefix that the CLI is routing, so the UI never lists the model. | `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <client-key>" | jq '.data[].id' | rg '<workspace-prefix>'` and compare with the workspace filter used in Cherry Studio. | Add the missing alias/prefix to the workspace's allowed set or align the workspace selection with the alias returned by `/v1/models`, then reload Cherry Studio so it sees the same inventory as CLI. |
| `Antigravity 2 API Opus model returns Error searching files` (CPB-0375) | The search tool block is missing or does not match the upstream tool schema, so translator rejects `tool_call` payloads when the Opus model tries to access files. | Replay the search payload against `/v1/chat/completions` and tail the translator logs for `tool_call`/`SearchFiles` entries to see why the tool request was pruned or reformatted. | Register the `searchFiles` alias for the Opus provider (or the tool name Cherry Studio sends), adjust the `tools` block to match upstream requirements, and rerun the flow so the translator forwards the tool call instead of aborting. |
| `Streaming response never emits [DONE] even though upstream closes` (CPB-0376) | SSE translator drops the `[DONE]` marker or misses the final `model_status: "succeeded"` transition, so downstream clients never see completion. | Compare the SSE stream emitted by `/v1/chat/completions` to the upstream stream and watch translator logs for `[DONE]` / `model_status` transitions; tail `cliproxy` logs around the final chunks. | Ensure the translation layer forwards `[DONE]` immediately after the upstream `model_status` indicates completion (or emits a synthetic `[DONE]`), and log a warning if the stream closes without sending the final marker so future incidents can be traced. |
| `Cannot use Claude Models in Codex CLI` | Missing oauth alias bridge for Claude model IDs | `curl -sS .../v1/models | jq '.data[].id' | rg 'claude-opus|claude-sonnet|claude-haiku'` | Add/restore `oauth-model-alias` entries (or keep default injection enabled), then reload and re-check `/v1/models` |
| `claude-opus-4-6` missing or returns `bad model` | Alias/prefix mapping is stale after Claude model refresh | `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer YOUR_CLIENT_KEY" | jq -r '.data[].id' | rg 'claude-opus-4-6|claude-sonnet-4-6'` | Update `claude-api-key` model alias mappings, reload config, then re-run non-stream Opus 4.6 request before stream rollout |
| `/v1/responses/compact` fails or hangs | Wrong endpoint/mode expectations (streaming not supported for compact) | Retry with non-stream `POST /v1/responses/compact` and inspect JSON `object` field | Use compact only in non-stream mode; for streaming flows keep `/v1/responses` or `/v1/chat/completions` |
| MCP memory tools fail (`tool not found`, invalid params, or empty result) | MCP server missing memory tool registration or request schema mismatch | Run `tools/list` then one minimal `tools/call` against the same MCP endpoint | Enable/register memory tools, align `tools/call` arguments to server schema, then repeat `tools/list` and `tools/call` smoke tests |

## `gemini-3-pro-preview` Tool-Use Triage

- Use this flow when tool calls to `gemini-3-pro-preview` return unexpected `400/500` patterns and non-stream canaries work:
  - `touch config.yaml`
  - `process-compose -f examples/process-compose.dev.yaml down`
  - `process-compose -f examples/process-compose.dev.yaml up`
  - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <client-key>" | jq '.data[].id' | rg 'gemini-3-pro-preview'`
  - `curl -sS -X POST http://localhost:8317/v1/chat/completions -H "Authorization: Bearer <client-key>" -H "Content-Type: application/json" -d '{"model":"gemini-3-pro-preview","messages":[{"role":"user","content":"ping"}],"stream":false}'`

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

## OAuth Callback Server Start Failure (Gemini/Antigravity)

Symptom:

- Login fails with `failed to start callback server` or `bind: address already in use`.

Checks:

```bash
lsof -iTCP:51121 -sTCP:LISTEN
lsof -iTCP:51122 -sTCP:LISTEN
```

Remediation:

```bash
# Pick an unused callback port explicitly
./cliproxyapi++ auth --provider antigravity --oauth-callback-port 51221

# Server mode
./cliproxyapi++ --oauth-callback-port 51221
```

If callback setup keeps failing, run with `--no-browser`, copy the printed URL manually, and paste the callback URL back into the CLI prompt.

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
