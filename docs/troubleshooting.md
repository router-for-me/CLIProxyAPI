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
| Repeated upstream `403` with ambiguous provider labels | Non-canonical provider key/alias naming across config/docs | Compare request model prefix + `oauth-model-alias` channel against canonical keys (`github-copilot`, `antigravity`, `iflow`, `kimi`) | Normalize names to canonical provider keys, then reload and re-check `/v1/models` inventory |
| `Invalid JSON payload ... tool_result has no content field` | Upstream/client emitted sparse `tool_result` content block shape | Reproduce with one minimal payload and inspect translated request in logs | Upgrade to a build with sparse `tool_result` normalization; as a temporary workaround, send `tool_result.content` as `[]` |
| iFlow `GLM-5` occasionally returns empty answer | Stream/non-stream payload parity drift or stale reasoning context in history | Retry same prompt with `stream:false` and compare emitted chunks/choices | Validate parity in both modes, keep assistant history intact, and avoid mixed legacy/new reasoning fields in one request |
| `Docker Image Error` on startup/health | Image tag mismatch, stale config mount, or incompatible env defaults | `docker images | head`, `docker logs CONTAINER_NAME --tail 200`, `/health` check | Pull/pin a known-good tag, verify mounted `config.yaml`, then compare `stream: true/false` behavior for parity |
| `Model not found` / `bad model` | Alias/prefix/model map mismatch | `curl .../v1/models` and compare requested ID | Update alias map, prefix rules, and `excluded-models` |
| `gpt-5.3-codex-spark` fails for plus/team | Account tier does not expose Spark model even if config lists it | `GET /v1/models` and look for `gpt-5.3-codex-spark` | Route to `gpt-5.3-codex` fallback and alert on repeated Spark 400/404 responses |
| `iflow` requests repeatedly return `406` | Upstream model-level rejection, stale auth/session metadata, or stream-shape mismatch | Retry same payload with `stream:false` and compare status/body | Gate rollout with non-stream canary first, then stream; keep fallback model alias ready (`iflow/glm-4.7` or `iflow/minimax-m2.5`) |
| `Reasoning Error` / invalid effort-level failures | Request carries unsupported `reasoning_effort`/`reasoning.effort` for selected model | Retry with `reasoning_effort: "low"` and inspect error payload + model ID | Use model-supported levels only; for unknown compatibility set `reasoning_effort` to `auto` or omit field |
| Claude OAuth cache usage always `0` | Upstream does not emit cache counters for selected model or request path | Compare `usage` fields for same prompt on `/v1/chat/completions` and `/v1/responses` | Treat missing cache counters as observational gap; verify routing and usage totals before assuming billing drift |
| Port `8317` becomes unreachable until SSH/login activity | Host/network idle policy, process supervision gap, or stale listener | `lsof -iTCP:8317 -sTCP:LISTEN` and `/health` from local + remote shell | Add external health check + autorestart, and pin service under process manager with restart-on-failure |
| Runtime config write errors | Read-only mount or immutable filesystem | `find /CLIProxyAPI -maxdepth 1 -name config.yaml -print` | Use writable mount, re-run with read-only warning, confirm management persistence status |
| Kiro/OAuth auth loops | Expired or missing token refresh fields | Re-run `cliproxyapi++ auth`/reimport token path | Refresh credentials, run with fresh token file, avoid duplicate token imports |
| `iflow executor: token refresh failed` | Refresh token expired/revoked or refresh metadata incomplete | Inspect auth metadata (`refresh_token`, `expired`, `last_refresh`) and trigger management refresh endpoint | Re-auth affected provider, then run one management refresh + one canary chat request before restoring traffic |
| Streaming hangs or truncation | Reverse proxy buffering / payload compatibility issue | Reproduce with `stream: false`, then compare SSE response | Verify reverse-proxy config, compare tool schema compatibility and payload shape |
| Local `gemini3` runs keep failing after config edits | Runtime not reloaded deterministically in local dev | Check whether `process-compose` profile is running and watcher sees file changes | Use `process-compose -f examples/process-compose.dev.yaml up`, then force a config timestamp bump and re-run `/health` + canary request |
| `Cannot use Claude Models in Codex CLI` | Missing oauth alias bridge for Claude model IDs | `curl -sS .../v1/models | jq '.data[].id' | rg 'claude-opus|claude-sonnet|claude-haiku'` | Add/restore `oauth-model-alias` entries (or keep default injection enabled), then reload and re-check `/v1/models` |
| `/v1/responses/compact` fails or hangs | Wrong endpoint/mode expectations (streaming not supported for compact) | Retry with non-stream `POST /v1/responses/compact` and inspect JSON `object` field | Use compact only in non-stream mode; for streaming flows keep `/v1/responses` or `/v1/chat/completions` |

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
