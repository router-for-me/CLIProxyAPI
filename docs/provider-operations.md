# Provider Operations Runbook

This runbook is for operators who care about provider uptime, quota health, and routing quality.

## Daily Checks

1. Health check:
   - `curl -sS http://localhost:8317/health`
2. Model inventory:
   - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq '.data | length'`
3. Provider metrics:
   - `curl -sS http://localhost:8317/v1/metrics/providers | jq`
4. Log scan:
   - Verify no sustained bursts of `401`, `403`, or `429`.
5. Spark eligibility check (Copilot/Codex):
   - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq -r '.data[].id' | rg 'gpt-5.3-codex|gpt-5.3-codex-spark'`

## Update Check Workflow

Use this before reporting “latest image/binary mismatch” incidents.

1. Check running binary/image:
   - `./cliproxyapi++ --version`
   - `docker images | head`
2. Check upstream release/tag:
   - `git fetch --tags --prune`
   - `git describe --tags --always`
3. Refresh deployment:
   - Docker: `docker pull KooshaPari/cliproxyapi-plusplus:latest && docker compose up -d`
   - Binary: download latest release asset and restart service supervisor
4. Validate image digest to catch stale cache:
   - `docker image inspect KooshaPari/cliproxyapi-plusplus:latest --format '{{index .RepoDigests 0}}'`
5. Re-validate:
   - `curl -sS http://localhost:8317/health`
   - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq '.data | length'`

## Quota Visibility (`#146` scope)

- Current operational source of truth is `v1/metrics/providers` plus provider auth/token files.
- There is no dedicated unified "Kiro quota dashboard" endpoint in this repo today.
- Treat repeated `429` + falling success ratio as quota pressure and rotate capacity accordingly.

## Onboard a New Provider

1. Add provider block in `config.yaml` (`openai-compatibility` preferred for OpenAI-style upstreams).
2. Add `prefix` for tenant/workload isolation.
3. Add `models` aliases for client-stable names.
4. Validate `/v1/models` output includes expected IDs.
5. Run canary request through the new prefix.
6. Monitor `v1/metrics/providers` for 10-15 minutes before production traffic.

## Rotation and Quota Strategy

- Configure multiple credentials per provider where supported.
- Use per-credential proxy override first (`auth.proxy-url`); keep global `proxy-url` as fallback only.
- Keep at least one alternate provider for each critical workload class.
- Use prefixes to separate high-priority traffic from best-effort traffic.
- If one provider is degraded, reroute by updating model prefix policy and aliases.

## Incident Playbooks

### Repeated `401/403`

- Recheck credential validity and token freshness.
- For OAuth providers (`kiro`, `cursor`, `minimax`, `roo`), verify token files and refresh path.
- Confirm client is hitting intended provider prefix.

### Repeated `429`

- Add capacity (extra keys/providers) or reduce concurrency.
- Shift traffic to fallback provider prefix.
- Tighten expensive-model exposure with `excluded-models`.

### Repeated `503` No Capacity (Antigravity)

- Confirm payload includes `MODEL_CAPACITY_EXHAUSTED` or `No capacity available`.
- Keep `antigravity-no-capacity-retry: true` to allow fallback retry across base URLs.
- Set `antigravity-no-capacity-retry: false` only for fail-fast incident handling.

### Repeated `406` for iFlow

- Run two probes with identical payloads (`stream:false` then `stream:true`).
- If non-stream succeeds but stream fails, temporarily force non-stream for that model/prefix.
- Keep a fallback alias ready (`iflow/glm-4.7` or `iflow/minimax-m2.5`) and switch via config reload.
- Treat sustained `406` as rollback criteria for newly enabled iFlow models.

### Wrong Provider Selected

- Inspect `force-model-prefix` and model naming in requests.
- Verify alias collisions across provider blocks.
- Prefer explicit `prefix/model` calls for sensitive workloads.

### Disable One Credential Quickly

- Use management status patch against a specific auth file (file name or full path both supported):
  - `curl -sS -X PATCH http://localhost:8317/v0/management/auth-files/status -H "Authorization: Bearer <mgmt-secret>" -H "Content-Type: application/json" -d '{"name":"gemini-auth.json","disabled":true}'`
- Re-enable when incident clears:
  - `curl -sS -X PATCH http://localhost:8317/v0/management/auth-files/status -H "Authorization: Bearer <mgmt-secret>" -H "Content-Type: application/json" -d '{"name":"gemini-auth.json","disabled":false}'`

### Credential Proxy Misconfiguration

- Confirm per-credential `auth.proxy-url` parses successfully (preferred path).
- Confirm fallback `config proxy-url` is valid and not shadowing intended credential proxy.
- Use supported schemes only: `http`, `https`, `socks5`.
- Example:
  - `proxy-url: "http://user:pass@proxy.example.com:8080"`

### Cache Usage Always `0` on Claude OAuth

- Compare `usage` objects between `/v1/chat/completions` and `/v1/responses` for the same prompt.
- Confirm the selected model is cache-capable in your entitlement tier.
- Use total token counters as billing baseline when cache counters are absent.
- Do not block rollout on cache counters alone unless token totals are also inconsistent.

### Missing Models in `/v1/models`

- Confirm provider block is enabled and auth loaded.
- Check model filters (`models`, `excluded-models`) and prefix constraints.
- Verify upstream provider currently serves requested model.
- For Antigravity specifically, treat `0` returned models as an incident signal and trigger token refresh + auth file verification before retrying production traffic.

### Antigravity Returns Zero Models

- Symptom: `/v1/models` responds successfully but Antigravity inventory is empty.
- Immediate checks:
  - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq '.data | length'`
  - Verify auth file freshness and last token refresh timestamp.
- Remediation:
  - Refresh Antigravity auth, reload config, and rerun model inventory.
  - Keep fallback provider aliases active until model count is stable.

### Banana Pro 4K Empty Output (Thinking Only)

- Symptom: response contains thought/trace fields but no assistant content.
- Immediate checks:
  - Re-run same payload with `stream:false`.
  - Inspect logs for missing `candidates[].content`/`finish_reason`.
- Remediation:
  - Route to fallback alias for image generation workloads.
  - Keep alert active until content-bearing responses return consistently.

### Port `8317` Becomes Unreachable

- Verify listener state: `lsof -iTCP:8317 -sTCP:LISTEN`.
- Run local and remote `/health` checks to isolate host-level reachability vs process crash.
- Ensure service is supervised (`systemd`/container restart policy) with restart-on-failure.
- Add synthetic health checks so idle/network policy regressions are detected before user traffic impact.

### Copilot Spark Mismatch (`gpt-5.3-codex-spark`)

- Symptom: plus/team users get `400/404 model_not_found` for `gpt-5.3-codex-spark`.
- Immediate action:
  - Confirm presence in `GET /v1/models` for the exact client API key.
  - If absent, route workloads to `gpt-5.3-codex` and keep Spark disabled for that segment.
- Suggested alert thresholds:
  - Warn: Spark error ratio > 2% over 10 minutes.
  - Critical: Spark error ratio > 5% over 10 minutes.
  - Auto-mitigation: fallback alias to `gpt-5.3-codex` when critical threshold is crossed.

### iFlow Cookie Failure After Google Login (`iflow uses cookie`)

- Symptom: `iflow` requests fail after entering Google-style login cookies.
- Immediate checks:
  - Capture raw response for `401`/`403` plus redirect/callback traces.
  - Re-open with the same cookie in a fresh browser context; stale `SameSite`/domain cookies often fail silently.
- Remediation:
  - Clear local `iflow` cookie store and re-run `thegent cliproxy login iflow`.
  - Compare `/v1/models` under `iflow/` alias before and after reauth.
  - Keep an alternate iFlow model alias (`iflow/minimax-m2.5`) as immediate fallback.

### Invalid Thinking Block Contracts (`max_tokens` / `thinking.budget_tokens`)

- Symptom: `invalid_request_error` rejecting requests with explicit `thinking.budget_tokens`.
- Immediate checks:
  - Confirm `thinking.budget_tokens` < `max_tokens` for each `iflow`/`antigravity` payload.
  - Validate `generationConfig` contains only one of `thinkingBudget` / `thinking` style fields per adapter path.
- Remediation:
  - Roll out request-shape standardization before capacity tests.
  - Enable canary non-stream request first, then stream.
  - Use explicit `max_tokens` in model-specific sanity harness until telemetry confirms stable.

### Antigravity CLI Compatibility and Reachability (`--iflow-login` and auth channels)

- Symptom: Operators cannot determine which CLIs provide stable Antigravity login flows.
- Immediate checks:
  - Record successful login command matrix daily (`claude`, `codex`, `cursor`, `iflow`).
  - Verify management endpoint login mode is enabled (`/v0/management/*`) and log redaction is active.
- Remediation:
  - Update runbook matrix with command-specific caveats (browser callbacks, SSO, proxy dependencies).
  - Normalize onboarding docs to a canonical command-order for login and validation (`login` → `v1/models` → canary request).

### Antigravity Non-Standard Response / Tooling Path (`/v1/responses`)

- Symptom: `antigravity` request goes through but returns unusual response fields or websocket naming mismatch.
- Immediate checks:
  - Compare `GET /v1/models` and `/v1/metrics/providers` for provider ID drift.
  - Replay failing request in `/v1/responses` and inspect raw `websocket` upgrade traces.
- Remediation:
  - Verify client path is hitting `/v1/responses` with expected streaming handshake.
  - Prefer the websocket endpoint documented for the selected proxy client and keep `/v1/chat/completions` as fallback probe.

### Antigravity Fallback and Timeout Hardening (`not working`)

- Symptom: Antigravity intermittently returns empty responses or 503 despite healthy health checks.
- Immediate checks:
  - Compare success/failure ratio for `/v1/chat/completions` and `/v1/responses`.
  - Inspect `ANTIGRAVITY` provider logs for auth, quota, and schema mismatch warnings.
- Remediation:
  - Route critical traffic to fallback providers on sustained failure windows.
  - Capture and keep a golden response sample for each upstream model during incident.

### Proxy/WebSocket Deployment and Parser Surface (`Gemini/Zeabur compatibility`)

- Symptom: Zeabur-style deployments expose parser drift or request shape issues for Gemini/Gemini-CLI interoperability.
- Immediate checks:
  - Confirm proxy/websocket headers pass auth and content-type consistently.
  - Compare non-stream and stream payload traces against local reference on same model.
- Remediation:
  - Rebuild image with minimal config drift and pinned upstream URLs.
  - Keep `gemini` deployment health with a dedicated deploy verification request matrix:
    - `/v1/chat/completions`
    - `/v1/models`
    - `/v1/responses`

### Gemini `3-pro-preview` and gmini CLI Compatibility (`Could I use gemini-3-pro-preview by gmini cli?`)

- Symptom: `gemini/3-pro-preview` works from some clients but fails from `gmini`/CLI or with stream mismatch.
- Immediate checks:
  - Validate both non-stream and stream parity with identical payloads before rollout.
  - Compare upstream request/response shape against `/v1/chat/completions` golden response from proxy for the same alias.
- Remediation:
  - Confirm `alias: "gemini-3-pro-preview"` exists under `gemini-api-key` and is exposed in `/v1/models`.
  - Re-route to `stream:false` for one full validation cycle, then re-enable `stream:true`.
  - Keep a fallback alias (`gemini/2.5-pro`, `iflow/gemini-2.5-flash`) ready for canary failures.
- Suggested parity probe:

```bash
for mode in false true; do
  echo "== stream=$mode =="
  curl -sS -X POST http://localhost:8317/v1/chat/completions \
    -H "Authorization: Bearer <api-key>" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"gemini/gemini-3-pro-preview\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"stream\":$mode}" \
    | jq 'del(.choices)'
done
```

### Windows Hyper-V Reserved Port Validation

- Symptom: service intermittently fails to start on Windows hosts with errors like bind failed / address already in use.
- Immediate checks:
  - `netsh interface ipv4 show excludedportrange protocol=tcp | rg 8317`
  - `netstat -ano | rg :8317`
- Remediation:
  - Switch to a free port in `config.yaml` (both `port` and any reverse-proxy backend port mappings).
  - Prefer explicit `auth-dir` and static config path under writable directories when running under Hyper-V.
  - Restart process host after port reservation changes and validate `/health` before traffic resume.

### Gemini Image Preview Capability and Observability

- Symptom: image-capable endpoints return confusion (`image` request denied or disabled model behavior).
- Immediate checks:
  - Verify model appears in `/v1/models` as an image-enabled alias.
  - Run a timeout-limited image generation probe and collect error object fields.
- Remediation:
  - Route image jobs to a known-good image-capable alias when model-specific capability is uncertain.
  - Add alert on repeated image generation errors by model prefix:
    - Warn: image error ratio > 2% over 5 minutes.
    - Critical: image error ratio > 5% over 5 minutes or `status_code` > 399 in `gemini` image provider metrics.
  - Refresh capability matrix docs when new image-support changes land from upstream.

### Local Dev Reload for File Upload / Gemini Native API Changes

- Symptom: config edits for upload-capable native API path are not reflected without manual restart.
- Immediate checks:
  - Ensure `examples/process-compose.dev.yaml` is started from the repo root with a writable `config.yaml` mount.
  - Check that `cliproxy` process reports readiness and `/health` remains green after config save.
- Remediation:
  - Use process-compose restart-on-failure and explicit file-backed config for deterministic HMR reload behavior.
  - After upload-related config edits, re-run upload-path parity probe and `/health` check before reopening clients.

Example command sequence after config edits:

```bash
process-compose -f examples/process-compose.dev.yaml restart cliproxy
curl -sS http://localhost:8317/health
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/image-preview","messages":[{"role":"user","content":"upload-path sanity"}],"stream":false}' \
  | jq '{id,error,model} '
```

## Recommended Production Pattern

1. One direct primary provider for latency-critical traffic.
2. One aggregator fallback provider for model breadth.
3. Prefix-based routing policy per workload class.
4. Metrics and alerting tied to error ratio, latency, and provider availability.

## Related Docs

- [Provider Catalog](/provider-catalog)
- [Provider Usage](/provider-usage)
- [Routing and Models Reference](/routing-reference)
- [Troubleshooting](/troubleshooting)
