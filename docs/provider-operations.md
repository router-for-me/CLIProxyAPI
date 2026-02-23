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

### Copilot Spark Mismatch (`gpt-5.3-codex-spark`)

- Symptom: plus/team users get `400/404 model_not_found` for `gpt-5.3-codex-spark`.
- Immediate action:
  - Confirm presence in `GET /v1/models` for the exact client API key.
  - If absent, route workloads to `gpt-5.3-codex` and keep Spark disabled for that segment.
- Suggested alert thresholds:
  - Warn: Spark error ratio > 2% over 10 minutes.
  - Critical: Spark error ratio > 5% over 10 minutes.
  - Auto-mitigation: fallback alias to `gpt-5.3-codex` when critical threshold is crossed.

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
