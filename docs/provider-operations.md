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

## Quota Visibility (`#146` scope)

- Current operational source of truth:
  - `v1/metrics/providers`
  - Management auth snapshots (`/v0/management/auth-files`)
  - Kiro quota snapshot endpoint: `/v0/management/kiro-quota` (includes `remaining_quota`, `usage_percentage`, `quota_exhausted`)
- Treat repeated `429` + falling success ratio as quota pressure and rotate capacity accordingly.

### Kiro Remaining Quota Probe

```bash
AUTH_KEY="replace-with-management-secret"
curl -sS http://localhost:8317/v0/management/kiro-quota \
  -H "Authorization: Bearer $AUTH_KEY" | jq
```

If multiple Kiro credentials exist, map and query by index:

```bash
curl -sS http://localhost:8317/v0/management/auth-files \
  -H "Authorization: Bearer $AUTH_KEY" \
  | jq -r '.[] | .auth_index // .index'

curl -sS "http://localhost:8317/v0/management/kiro-quota?auth_index=<auth-index>" \
  -H "Authorization: Bearer $AUTH_KEY" | jq
```

Suggested alert policy:

- Warn: any credential returns `quota_exhausted=true`.
- Warn: `429` ratio > 5% over 10 minutes.
- Critical: `429` ratio > 10% over 10 minutes OR steady `quota_exhausted=true` across top 2 providers.
- Action: enable fallback toggles and rotate to alternate credentials:
  - `quota-exceeded.switch-project=true`
  - `quota-exceeded.switch-preview-model=true`

## Onboard a New Provider

1. Add provider block in `config.yaml` (`openai-compatibility` preferred for OpenAI-style upstreams).
2. Add `prefix` for tenant/workload isolation.
3. Add `models` aliases for client-stable names.
4. Validate `/v1/models` output includes expected IDs.
5. Run canary request through the new prefix.
6. Monitor `v1/metrics/providers` for 10-15 minutes before production traffic.

## Rotation and Quota Strategy

- Configure multiple credentials per provider where supported.
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

### Wrong Provider Selected

- Inspect `force-model-prefix` and model naming in requests.
- Verify alias collisions across provider blocks.
- Prefer explicit `prefix/model` calls for sensitive workloads.

### Missing Models in `/v1/models`

- Confirm provider block is enabled and auth loaded.
- Check model filters (`models`, `excluded-models`) and prefix constraints.
- Verify upstream provider currently serves requested model.

### Tool-Result Image Translation Regressions

- Symptom pattern: tool responses containing image blocks fail after translation between OpenAI-compatible and Claude-style payloads.
- First checks:
  - Reproduce with a non-stream request and compare with stream behavior.
  - Inspect request/response logs for payload-shape mismatches around `tool_result` + image content blocks.
- Operational response:
  - Keep one canary scenario that includes image content in tool results.
  - Alert when canary success rate drops or `4xx` translation errors spike for that scenario.
  - Route impacted traffic to a known-good provider prefix while triaging translator output.

### Stream/Non-Stream Usage Parity Check

- Goal: confirm token usage fields are consistent between stream and non-stream responses for the same prompt.
- Commands:
  - Non-stream:
    - `curl -sS http://localhost:8317/v1/responses -H "Authorization: Bearer <api-key>" -H "Content-Type: application/json" -d '{"model":"gpt-5.1-codex","input":[{"role":"user","content":"ping"}],"stream":false}' | tee /tmp/nonstream.json | jq '{input_tokens: .usage.input_tokens, output_tokens: .usage.output_tokens, total_tokens: .usage.total_tokens}'`
  - Stream (extract terminal usage event):
    - `curl -sN http://localhost:8317/v1/responses -H "Authorization: Bearer <api-key>" -H "Content-Type: application/json" -d '{"model":"gpt-5.1-codex","input":[{"role":"user","content":"ping"}],"stream":true}' | rg '^data:' | sed 's/^data: //' | jq -c 'select(.usage? != null) | {input_tokens: (.usage.input_tokens // .usage.prompt_tokens), output_tokens: (.usage.output_tokens // .usage.completion_tokens), total_tokens: .usage.total_tokens}' | tail -n 1 | tee /tmp/stream-usage.json`
  - Compare:
    - `diff -u <(jq -S . /tmp/nonstream.json | jq '{input_tokens: .usage.input_tokens, output_tokens: .usage.output_tokens, total_tokens: .usage.total_tokens}') <(jq -S . /tmp/stream-usage.json)`
- Pass criteria:
  - `diff` is empty, or any difference is explainable by provider-side truncation/stream interruption.

### iFlow OAuth model visibility is narrower than expected

- Symptom: login/auth succeeds, but only a subset of `iflow/*` models appear or work.
- Immediate checks:
  - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq -r '.data[].id' | rg '^iflow/'`
  - Validate request model is exactly one of the exposed IDs.
- Mitigation:
  - Do not assume upstream catalog parity after OAuth login.
  - Keep a known-good iFlow canary model and gate rollout on successful canary responses.

### iFlow account errors shown in terminal

- Symptom: terminal output shows account-level iFlow errors but requests keep retrying noisily.
- Immediate checks:
  - `rg -n "iflow|account|retry|cooldown|429|403" logs/*.log`
  - `curl -sS http://localhost:8317/v1/metrics/providers | jq '.iflow // .providers.iflow'`
- Mitigation:
  - Alert on sustained iFlow error-rate spikes (>5% over 10m).
  - Keep one known-good iFlow canary request in non-stream mode.
  - Rotate traffic away from iFlow prefix when account-level failures persist beyond cooldown windows.

### Usage dashboard shows zeros under load

- Symptom: traffic volume rises but usage counters remain `0`.
- Immediate checks:
  - Run one non-stream and one stream request against the same model and compare emitted usage fields/log lines.
  - Verify provider metrics endpoint still records request/error activity.
- Mitigation:
  - Treat missing upstream usage as a provider payload gap, not a transport success signal.
  - Keep stream/non-stream parity probes in pre-release checks.

### Antigravity / CLA CLI support matrix (`CPB-0743`)

- Symptom: `antigravity` clients intermittently produce empty payloads or different behavior between `antigravity-cli` and CLIProxyAPI Plus front-end calls.
- Immediate checks:
  - Confirm model coverage:
    - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq -r '.data[].id' | rg '^antigravity/'`
  - Confirm supported CLI client class:
    - `curl -sS http://localhost:8317/v0/management/config -H "Authorization: Bearer <management-secret>" | jq '.providers[] | select(.name==\"antigravity\") | .supported_clients'`
  - Confirm request translation path in logs:
    - `rg -n "antigravity|claude|tool_use|custom_model|request.*model" logs/*.log`
- Suggested matrix checks:
  - `antigravity-cli` should map to supported auth-backed model IDs.
  - Provider alias mode should keep aliases explicit in `/v1/models`.
  - Tool/callback-heavy workloads should pass through without dropping `tool_use` boundaries.
- Mitigation:
  - If parity is missing, align source request to provider-native model IDs and re-check with a non-stream request first.
  - Route unsupported workloads through mapped aliases using `ampcode.model-mappings` and document temporary exclusion.
  - Keep a canary for each supported `antigravity/*` model with 10-minute trend windows.

### Copilot Spark Mismatch (`gpt-5.3-codex-spark`)

- Symptom: plus/team users get `400/404 model_not_found` for `gpt-5.3-codex-spark`.
- Immediate action:
  - Confirm presence in `GET /v1/models` for the exact client API key.
  - If absent, route workloads to `gpt-5.3-codex` and keep Spark disabled for that segment.
- Suggested alert thresholds:
  - Warn: Spark error ratio > 2% over 10 minutes.
  - Critical: Spark error ratio > 5% over 10 minutes.
  - Auto-mitigation: fallback alias to `gpt-5.3-codex` when critical threshold is crossed.

### Codex 5.3 integration path (non-subprocess first)

- Preferred path:
  - Embed via `sdk/cliproxy` when the caller owns the runtime process.
- HTTP fallback path:
  - Use `/v1/*` only when crossing process boundaries.
- Negotiation checks:
  - Probe `/health` and `/v1/models` before enabling codex5.3-specific flows.
  - Gate advanced behavior on observed model exposure (`gpt-5.3-codex`, `gpt-5.3-codex-spark`).

### Amp traffic does not route through CLIProxyAPI

- Symptom: Amp appears to call upstream directly and proxy logs remain idle.
- Immediate checks:
  - Ensure Amp process has `OPENAI_API_BASE=http://127.0.0.1:8317/v1`.
  - Ensure Amp process has `OPENAI_API_KEY=<client-key>`.
  - Run one direct canary request with identical env and confirm it appears in proxy logs.
- Mitigation:
  - Standardize Amp launch wrappers to export proxy env explicitly.
  - Add startup validation that fails early when base URL does not target CLIProxyAPI.

### Windows duplicate auth-file display safeguards

- Symptom: auth records appear duplicated in management/UI surfaces on Windows.
- Immediate checks:
  - Confirm auth filename normalization output is stable across refresh/reload cycles.
  - `curl -sS http://localhost:8317/v0/management/auth-files -H "X-Management-Secret: <secret>" | jq '.[].filename' | sort | uniq -c`
- Rollout safety:
  - Gate deployments with one Windows canary that performs add -> refresh -> list -> restart -> list.
  - Block promotion when duplicate filename count changes after restart.

### Metadata naming conventions for provider quota/refresh commands

Use consistent names across docs, APIs, and operator runbooks:
- `provider_key`
- `model_id`
- `quota_remaining`
- `quota_reset_seconds`
- `refresh_state`

Avoid per-tool aliases for these fields in ops docs to keep telemetry queries deterministic.

### TrueNAS Apprise notification DX checks

- Validate target endpoint formatting before enabling alerts:
  - `apprise -vv --dry-run "<apprise-url>"`
- Send one canary alert for routing incidents:
  - `apprise "<apprise-url>" -t "cliproxy canary" -b "provider routing notification check"`
- Keep this notification path non-blocking for request handling; alerts should not gate proxy response paths.

### Gemini thinking-length control drift (OpenAI-compatible clients)

- Symptom: client requests a specific thinking level/budget but observed behavior looks unbounded or unchanged.
- Immediate checks:
  - Inspect request/response pair and compare with runtime debug lines:
    - `thinking: original config from request`
    - `thinking: processed config to apply`
  - Confirm requested model and its thinking-capable alias are exposed in `/v1/models`.
- Suggested alert thresholds:
  - Warn: processed thinking mode mismatch ratio > 2% over 10 minutes.
  - Critical: processed thinking mode mismatch ratio > 5% over 10 minutes.
  - Warn: reasoning token growth > 25% above baseline for fixed-thinking workloads over 10 minutes.
- Mitigation:
  - Force explicit thinking-capable model alias for affected workloads.
  - Reduce rollout blast radius by pinning the model suffix/level per workload class.
  - Keep one non-stream and one stream canary for each affected client integration.

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
