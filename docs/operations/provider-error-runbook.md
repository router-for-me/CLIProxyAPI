# Provider Error Runbook Snippets

These are the smallest actionable runbook entries for CPB-0803 and CPB-0804 so the on-call can exercise the correct validation commands before changing code or configuration.

## CPB-0803 – Huggingface CLIProxyAPI errors

**Symptom**: Huggingface calls fail silently in production logs, but observability lacks provider tags and the usage dashboard shows untracked traffic. Alerts are noisy because the log sink cannot route the error rate to the right channel.

**Validation commands**

- `curl -sS http://localhost:8317/v0/management/logs | jq '.logs[] | select(.provider == "huggingface" and .level == "error")'`
- `curl -sS http://localhost:8317/v1/metrics/providers | jq '.data[] | select(.provider == "huggingface") | {error_rate, requests, last_seen}'`
- `curl -sS http://localhost:8317/usage | jq '.providers.huggingface'`

**Runbook steps**

1. Make sure `cliproxyctl` has the `provider_filter` tags set for `huggingface` so the management log output includes `provider: "huggingface"`. If the logs lack tags, reapply the filter via `cliproxyctl config view` + `cliproxyctl config edit` (or update the `config.yaml` block) and restart the agent.
2. Verify the `v1/metrics/providers` entry for `huggingface` shows a stable error rate; if it stays above 5% for 5 minutes, escalate to the platform on-call and mark the alert as a hurt-level incident.
3. After correcting the tagging, confirm the `usage` endpoint reports the provider so the new alerting rule in `provider-error` dashboards can route to the right responder.

## CPB-0804 – Codex backend-api `Not Found`

**Symptom**: Translations still target `https://chatgpt.com/backend-api/codex/responses`, which now returns `404 Not Found`. The problem manifests as a `backend-api` status in the `management/logs` stream that cannot be mapped to the new `v1/responses` path.

**Validation commands**

- `curl -sS http://localhost:8317/v0/management/logs | jq '.logs[] | select(.provider == "codex" and (.path | contains("backend-api/codex")) and .status_code == 404)'`
- `curl -sS http://localhost:8317/v1/responses -H "Authorization: Bearer <api-key>" -H "Content-Type: application/json" -d '{"model":"codex","messages":[{"role":"user","content":"ping"}],"stream":false}' -w "%{http_code}"`
- `curl -sS http://localhost:8317/v1/metrics/providers | jq '.data[] | select(.provider == "codex") | {error_rate, last_seen}'`
- `rg -n "backend-api/codex" config.example.yaml config.yaml`

**Runbook steps**

1. Use the management log command above to confirm the 404 comes from the old `backend-api/codex` target. If the request still hits that path, re-point the translator overrides in `config.yaml` (or environment overrides such as `CLIPROXY_PROVIDER_CODEX_BASE_URL`) to whatever URL serves the current Responses protocol.
2. Re-run the `curl` to `/v1/responses` with the same payload to verify the translation path can resolve to an upstream that still works; if it succeeds, redeploy the next minor release with the provider-agnostic translator patch.
3. If the problem persists after a config change, capture the raw `logs` and `metrics` output and hand it to the translations team together with the failing request body, because the final fix involves sharing translator hooks and the compatibility matrix described in the quickstart docs.

---
Last reviewed: `2026-02-23`
Owner: `Platform On-Call`
