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
| Antigravity returns `503` + `MODEL_CAPACITY_EXHAUSTED` | Upstream pool has no available model capacity | Check error payload for `No capacity available` and `MODEL_CAPACITY_EXHAUSTED` | Keep `antigravity-no-capacity-retry: true` for failover; set `false` only when you want fail-fast behavior |
| `Invalid JSON payload ... tool_result has no content field` | Upstream/client emitted sparse `tool_result` content block shape | Reproduce with one minimal payload and inspect translated request in logs | Upgrade to a build with sparse `tool_result` normalization; as a temporary workaround, send `tool_result.content` as `[]` |
| `Docker Image Error` on startup/health | Image tag mismatch, stale config mount, or incompatible env defaults | `docker images | head`, `docker logs CONTAINER_NAME --tail 200`, `/health` check | Pull/pin a known-good tag, verify mounted `config.yaml`, then compare `stream: true/false` behavior for parity |
| `Model not found` / `bad model` | Alias/prefix/model map mismatch | `curl .../v1/models` and compare requested ID | Update alias map, prefix rules, and `excluded-models` |
| `gpt-5.3-codex-spark` fails for plus/team | Account tier does not expose Spark model even if config lists it | `GET /v1/models` and look for `gpt-5.3-codex-spark` | Route to `gpt-5.3-codex` fallback and alert on repeated Spark 400/404 responses |
| `iflow` requests repeatedly return `406` | Upstream model-level rejection, stale auth/session metadata, or stream-shape mismatch | Retry same payload with `stream:false` and compare status/body | Gate rollout with non-stream canary first, then stream; keep fallback model alias ready (`iflow/glm-4.7` or `iflow/minimax-m2.5`) |
| `Reasoning Error` / invalid effort-level failures | Request carries unsupported `reasoning_effort`/`reasoning.effort` for selected model | Retry with `reasoning_effort: "low"` and inspect error payload + model ID | Use model-supported levels only; for unknown compatibility set `reasoning_effort` to `auto` or omit field |
| Claude OAuth cache usage always `0` | Upstream does not emit cache counters for selected model or request path | Compare `usage` fields for same prompt on `/v1/chat/completions` and `/v1/responses` | Treat missing cache counters as observational gap; verify routing and usage totals before assuming billing drift |
| Antigravity models count is `0` in `/v1/models` | Expired auth/session metadata or upstream entitlement outage | `curl .../v1/models | jq '.data | length'` and inspect Antigravity auth freshness | Refresh Antigravity auth, reload config, keep fallback alias active until inventory returns |
| `"Requested entity was not found"` on Antigravity | Upstream model ID no longer valid or mapping drift | Retry with known-good model alias and compare upstream error payload | Update alias map, rerun model inventory, and keep canary tests for stream/non-stream parity |
| Banana Pro 4K returns only thinking/trace output | Upstream response missing assistant content blocks | Re-run with `stream:false`, inspect `finish_reason` and candidate content fields | Route image workload to fallback model, alert on repeated empty-content responses |
| Port `8317` becomes unreachable until SSH/login activity | Host/network idle policy, process supervision gap, or stale listener | `lsof -iTCP:8317 -sTCP:LISTEN` and `/health` from local + remote shell | Add external health check + autorestart, and pin service under process manager with restart-on-failure |
| Runtime config write errors | Read-only mount or immutable filesystem | `find /CLIProxyAPI -maxdepth 1 -name config.yaml -print` | Use writable mount, re-run with read-only warning, confirm management persistence status |
| Per-credential proxy fails unexpectedly | Invalid `auth.proxy-url`, unsupported scheme, or fallback confusion with global proxy | Check logs for `proxy config invalid` / `unsupported proxy scheme` and inspect proxy source | Prefer `auth.proxy-url` for credential-specific routing, keep `config proxy-url` as fallback, and use `http`/`https`/`socks5` only |
| Kiro/OAuth auth loops | Expired or missing token refresh fields | Re-run `cliproxyapi++ auth`/reimport token path | Refresh credentials, run with fresh token file, avoid duplicate token imports |
| Streaming hangs or truncation | Reverse proxy buffering / payload compatibility issue | Reproduce with `stream: false`, then compare SSE response | Verify reverse-proxy config, compare tool schema compatibility and payload shape |
| `Cannot use Claude Models in Codex CLI` | Missing oauth alias bridge for Claude model IDs | `curl -sS .../v1/models | jq '.data[].id' | rg 'claude-opus|claude-sonnet|claude-haiku'` | Add/restore `oauth-model-alias` entries (or keep default injection enabled), then reload and re-check `/v1/models` |
| Codex OAuth fails with `Failed to exchange authorization code for tokens` | Proxy/network path blocks callback token exchange | Retry `--codex-login` with/without configured proxy and inspect login logs | Verify `proxy-url` routing, disable proxy temporarily for login, then re-enable once token exchange succeeds |
| `/v1/responses/compact` fails or hangs | Wrong endpoint/mode expectations (streaming not supported for compact) | Retry with non-stream `POST /v1/responses/compact` and inspect JSON `object` field | Use compact only in non-stream mode; for streaming flows keep `/v1/responses` or `/v1/chat/completions` |
| iFlow cookie auth accepted but requests still fail | Cookie/session scope mismatch after interactive login | Re-run token rebind with fresh browser context and compare minimal probe payload | Clear cookie cache, re-auth via `thegent cliproxy login iflow`, then validate with `/v1/models` |
| `invalid_request_error` (`max_tokens` must be greater than `thinking.budget_tokens`) | thinking budget set too high for selected generation cap | Replay with matching `max_tokens`/`thinking.budget_tokens` pair and inspect request payload | Enforce `thinking.budget_tokens < max_tokens`; add canary validation before stream traffic |
| Antigravity CLI compatibility is unclear | Undocumented CLI-specific auth support matrix | Run help/status matrix for `antigravity`, `iflow`, `kiro`, `copilot` | Use only approved CLI paths from operations runbook, update docs on any new channel |
| iFlow custom parameter routing (`/tab`, dynamic aliasing) | Parameter injection mismatch in routing mapping | Replay with explicit alias and inspect resolved model in logs | Keep slash/mapping aliases explicitly pinned in `config.yaml` |
| Gemini responses contain non-standard OpenAI fields | Translator/parser shape differences on Gemini/OpenAI compatibility path | Capture raw payload and response from minimal failing call | Normalize field shape for parser-friendly request/response and test non-stream before stream |
| Token unobtainable after Google account auth success | Proxy/token handoff mismatch on login completion | Compare token source and proxy route in auth refresh logs | Re-run auth without proxy to confirm baseline, then re-enable proxy with corrected `proxy-url` |
| Antigravity websocket path naming drift | Websocket event/name mismatch in responses stream path | Replay through same session via `/v1/responses` with websocket capture | Validate event ordering and field names against expected protocol contract |
| Antigravity authentication still fails (`antigravity认证难以成功`) | Credential exists but routing path rejects despite successful login | Revalidate auth freshness and model availability in `/v1/models` and metrics | Refresh credential file, reload config, and fail open to fallback provider while investigating |
| Antigravity appears down but returns empty model inventory | Provider auth or entitlement race during startup | Confirm `/v1/models` model length and auth file timestamp | Rotate through credential reload and keep fallback model aliases during recovery |
| `gemini-3-pro-preview` works on some clients but not `gmini` CLI | Preview model alias or websocket stream contract mismatch | Verify `/v1/models` contains `gemini/3-pro-preview`, then run identical non-stream and stream probes | Gate rollout on non-stream; keep fallback alias and retest stream after upstream/client refresh |
| Port not bound on Windows hosts (`Hyper-V` reserved port) | Port is in Windows reserved range or blocked by another process | `netsh interface ipv4 show excludedportrange protocol=tcp` and `netstat -ano | rg :8317` | Choose a free port, update `config.yaml`, and restart/reload process-compose workflow |
| Gemini `image-preview` unexpectedly unsupported | Image model capability drift or capability matrix stale | Compare `/v1/models`, then run `/v1/images/generations` with explicit timeout | Route traffic to fallback image-capable alias and annotate model capability matrix in runbook |
| Gemini native upload path not reflected after config edit | Config/runtime reload gap or stale watcher event | `process-compose -f examples/process-compose.dev.yaml restart cliproxy`, then run `/v1/models`, upload probe, `/health` | Validate watcher/config reload deterministically before client resume and keep non-destructive rollout mode during edits |

Use this matrix as an issue-entry checklist:

```bash
for endpoint in health models v1/metrics/providers v0/management/logs; do
  curl -sS "http://localhost:8317/$endpoint" -H "Authorization: Bearer YOUR_API_KEY" | head -n 3
done
```

## Invalid Auth File Cleanup Checks

Use this when auth files are stale/corrupt and you need a safe cleanup pass before rollout:

```bash
AUTH_DIR="${AUTH_DIR:-./auths}"
find "$AUTH_DIR" -maxdepth 1 -type f -name '*.json' -print
```

```bash
# Validate JSON files and list broken ones
find "$AUTH_DIR" -maxdepth 1 -type f -name '*.json' -print0 \
  | xargs -0 -I{} sh -c 'jq empty "{}" >/dev/null 2>&1 || echo "invalid-json: {}"'
```

```bash
# Stream/non-stream parity probe after cleanup
for mode in false true; do
  curl -sS -X POST http://localhost:8317/v1/chat/completions \
    -H "Authorization: Bearer YOUR_CLIENT_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"iflow/minimax-m2.5\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"stream\":$mode}" \
    | head -n 5
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
