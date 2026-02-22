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
6. Antigravity alias continuity check:
   - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq -r '.data[].id' | rg 'gemini-claude-opus-4-6-thinking|claude-opus-4-6-thinking'`

## Quota Visibility (`#146` scope)

- Current operational source of truth is `v1/metrics/providers` plus provider auth/token files.
- Kiro quota can be queried directly via management API:
  - List auth records and capture a Kiro `auth_index`:
    - `curl -sS http://localhost:8317/v0/management/auth-files -H "Authorization: Bearer <management-key>" | jq`
  - Query Kiro quota:
    - `curl -sS "http://localhost:8317/v0/management/kiro-quota?auth_index=<AUTH_INDEX>" -H "Authorization: Bearer <management-key>" | jq`
  - If `auth_index` is omitted, API falls back to the first available Kiro credential.
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
- For `Qwen Free allocated quota exceeded`, switch credential/project immediately and reduce burst concurrency.

### Wrong Provider Selected

- Inspect `force-model-prefix` and model naming in requests.
- Verify alias collisions across provider blocks.
- Prefer explicit `prefix/model` calls for sensitive workloads.

### Missing Models in `/v1/models`

- Confirm provider block is enabled and auth loaded.
- Check model filters (`models`, `excluded-models`) and prefix constraints.
- Verify upstream provider currently serves requested model.

### `502 unknown provider for model ...`

- Primary check:
  - Ensure requested model appears in `/v1/models` for the same client key.
  - Verify `oauth-model-alias` entries are still present after config edits/reloads.
- Common fix path:
  - Re-add missing alias bridge entries (for example Antigravity Claude thinking aliases).
  - Reload config and re-run model inventory before retrying requests.

### iFlow `glm-4.7` returns `406`

- Immediate checks:
  - Reproduce with `stream: false` and capture full upstream body.
  - Verify exact model ID and alias route in `/v1/models`.
- Mitigation:
  - Route temporarily to a known-good fallback alias while normalizing request shape.

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
