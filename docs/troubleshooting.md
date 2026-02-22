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
  -H "Authorization: Bearer <your-client-key>"

# 3) What models are actually exposed?
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <your-client-key>" | jq '.data[].id' | head

# 4) Any provider-side stress?
curl -sS http://localhost:8317/v1/metrics/providers | jq
```

## Troubleshooting Matrix

| Symptom | Likely Cause | Immediate Check | Remediation |
| --- | --- | --- | --- |
| `Error 401` on request | Missing or rotated client API key | `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <key>"` | Update key in `api-keys`, restart, verify no whitespace in config |
| `403` from provider upstream | License/subscription or permission mismatch | Search logs for `status_code":403` in provider module | Align account entitlement, retry with fallback-capable model, inspect provider docs |
| `Model not found` / `bad model` | Alias/prefix/model map mismatch | `curl .../v1/models` and compare requested ID | Update alias map, prefix rules, and `excluded-models` |
| Runtime config write errors | Read-only mount or immutable filesystem | `find /CLIProxyAPI -maxdepth 1 -name config.yaml -print` | Use writable mount, re-run with read-only warning, confirm management persistence status |
| Kiro/OAuth auth loops | Expired or missing token refresh fields | Re-run `cliproxyapi++ auth`/reimport token path | Refresh credentials, run with fresh token file, avoid duplicate token imports |
| Streaming hangs or truncation | Reverse proxy buffering / payload compatibility issue | Reproduce with `stream: false`, then compare SSE response | Verify reverse-proxy config, compare tool schema compatibility and payload shape |

Use this matrix as an issue-entry checklist:

```bash
for endpoint in health models v1/metrics/providers v0/management/logs; do
  curl -sS "http://localhost:8317/$endpoint" -H "Authorization: Bearer <your-api-key>" | head -n 3
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

- Send `Authorization: Bearer <api-key>`.
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
