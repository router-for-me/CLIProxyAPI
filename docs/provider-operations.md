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

## Quota Visibility (`#146` scope)

- Current operational source of truth is `v1/metrics/providers` plus provider auth/token files.
- There is no dedicated unified "Kiro quota dashboard" endpoint in this repo today.
- Treat repeated `429` + falling success ratio as quota pressure and rotate capacity accordingly.

## Kiro IAM Operational Runbook

Minimum checks after every Kiro IAM login/import:

1. Confirm auth file is unique per account (avoid shared filename reuse).
2. Confirm `/v1/models` exposes expected `kiro/*` aliases for the active credential.
3. Run one canary request and verify no immediate `401/403/429` burst.

Suggested alert thresholds:

- `kiro` provider success ratio < 95% for 10m.
- `kiro` provider `401/403` ratio > 2% for 5m.
- `kiro` provider `429` ratio > 5% for 5m.

Immediate response:

1. Verify token metadata freshness in auth file.
2. Re-login with IAM flow.
3. Shift traffic to fallback prefix until success ratio recovers.

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

### iFlow Model List Drift / Update Failures

- Validate the iFlow credential first (`401/403` indicates auth drift, not model drift).
- Recheck provider filters (`models`, `excluded-models`) before concluding upstream regression.
- Use a safe fallback alias set (known-good `glm`/`minimax` entries) while refreshing model mappings.
- Re-run `/v1/models` and compare before/after counts to confirm recovery.

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
