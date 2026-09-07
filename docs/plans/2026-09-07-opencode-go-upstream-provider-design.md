# OpenCode Go Upstream Provider — Design

Date: 2026-09-07
Status: Validated (brainstorming session)

## Goal

Bring the OmniRoute "opencode-go" provider workflow into NixLLM as a
first-class Upstream Provider type (`opencode-go`), with manual per-entry
quota/limit checking via a button on each api_key_entry in the dashboard.

Reference implementation studied: diegosouzapw/OmniRoute (TypeScript):
- `open-sse/config/providers/registry/opencode/go/index.ts` — provider
  registration, base URL, model catalog.
- `open-sse/executors/opencode.ts` — per-model wire format dispatch.
- `open-sse/services/opencodeQuotaFetcher.ts` — quota fetch with 3 usage
  windows.

## Upstream facts (from OmniRoute source)

- Endpoint: `https://opencode.ai/zen/go/v1` — OpenAI-compatible
  (`/chat/completions`), auth `Authorization: Bearer <api_key>`.
- NOT purely OpenAI-format: several models use the Anthropic wire format
  (`/messages`, `x-api-key` + `anthropic-version: 2023-06-01`):
  `minimax-m3`, `qwen3.7-max`, `qwen3.7-plus`, `qwen3.6-plus-high`,
  `qwen3.6-plus-max`. One model (`muse-spark-1.2-contributor`) uses
  `openai-responses`; it is intentionally out of scope (falls back to the
  openai path).
- Quota endpoint: `GET https://opencode.ai/zen/go/v1/quota` with Bearer key.
  Response wraps three windows (`5h`/`hourly`/`short`, `weekly`/`week`/`wk`,
  `monthly`/`month`/`mo`), each `{used, limit, reset_at}`; worst window wins.
  **Known limitation**: per OmniRoute's own docs the public quota API is not
  live yet (upstream issues #16017/#18648/#31084) and the default URL returns
  404 today. Design must fail-open with a clear message.
- Seed model catalog (from the OmniRoute go-tier registry): glm-5.2 (+high/max
  effort aliases, reasoning), glm-5.1, glm-5, kimi-k2.6/k2.5/k3(+max),
  mimo-v2.5(-pro), minimax-m3 (anthropic, 1M ctx, vision), minimax-m2.7/m2.5,
  qwen3.7-max/plus (anthropic), qwen3.6-plus-high/max (anthropic), hy3
  (+efforts), hy3-preview, muse-spark-1.2-contributor, grok-4.5,
  deepseek-v4-pro/flash, ox-alpha-free.

## Decisions (validated)

1. **New provider type `opencode-go`** (not an `openai-compatibility`
   preset) because the upstream mixes OpenAI and Anthropic wire formats
   per model.
2. **Dedicated executor** dispatching per model at request time, reusing
   existing executor/translator paths as engines. `internal/translator/` is
   not touched; helpers go in `internal/runtime/executor/helps/` per repo
   convention.
3. **Quota: endpoint + fail-open.** No dashboard scraping, no probe
   inference. `quota_url` overridable via `extra_config` for when OpenCode
   ships an official endpoint.
4. **Model catalog: seed + manual refresh.** Seed catalog is written on row
   creation; a dashboard button live-fetches `/v1/models` and merges (never
   removes seed entries).

## Data model

- `upstream_providers` row: `provider_type = "opencode-go"`, one row per
  OpenCode account. `base_url` defaults to
  `https://opencode.ai/zen/go/v1` (editable). `api_key_entries` used as-is
  (multi-key, weight/priority/disabled/pool binding all existing).
  No schema migration needed.
- `extra_config.quota_url`: optional override of the default quota endpoint.
- Models: existing `upstream_provider_models` child rows plus a new per-model
  attribute `wire_format: "openai" | "anthropic"` (default `"openai"`),
  stored following the existing `Thinking` map pattern in
  `upstreamProviderModelReq` — no new column.

## Runtime wiring

- Render path (`internal/upstreamsync`): new `TypeOpenCodeGo` constant; rows
  render into a new `OpenCodeGo` section on `config.Config` (same pattern as
  `ClaudeKey`/`OpenAICompatibility`), carrying entries, headers,
  priority/routing strategy, proxy pool binding, and models with their
  `wire_format`. `SeedFromArtifacts` unchanged (PG-only type; no legacy YAML).
- New executor `internal/runtime/executor/opencode_go_executor.go` registered
  for channel `opencode-go`:
  - `wire_format=anthropic` → Claude executor path (`{base}/messages`,
    `x-api-key`, `anthropic-version` header).
  - `openai` (default) → OpenAI-compat executor path
    (`{base}/chat/completions`, `Authorization: Bearer`).
  - Dispatch reads `wire_format` from registry model metadata (stamped at
    render time) — O(1) lookup, unknown values fall back to `openai`.
- `BuildAPIKeyClients` counts opencode-go entries like other key providers.

## Management API

All under `/v0/management/upstream-providers`, same auth as the rest:

- `POST /:id/seed-models` — idempotent seed fill: models already present
  (by name) are not touched or duplicated.
- `GET /:id/upstream-models?entry_id=` — live GET `/v1/models` using the
  given (or first active) entry key; returns the upstream list for the
  dashboard to merge. Errors are reported without touching the catalog.
- `POST /:id/quota` — body `{entry_id}` (same pattern as `/:id/test`).
  GET `quota_url` (default `https://opencode.ai/zen/go/v1/quota`) with that
  entry's Bearer key, 8s timeout (matches the `api_tools.go` exception).
  Response `{ok, windows[], raw, error}`. Fail-open: 404/405, 401/403,
  network errors, timeouts, malformed JSON all return `ok:false` with a
  descriptive error and raw body when available; never mutate entry state
  or cooldowns. No caching (manual clicks are low volume).

## Dashboard UI

- Provider type dropdown gains **OpenCode Go**; selecting it prefills
  `base_url`. On first create the dashboard calls `seed-models` once.
- Per api_key_entry actions, next to the existing Test button:
  - **Quota** — inline result under the entry: three window bars (5h /
    weekly / monthly) with used/limit, percent, reset time. On 404, a badge
    "Quota API belum tersedia di upstream" plus a hint to set `quota_url`.
  - Row-level **Refresh models** — calls `upstream-models` with an entry
    choice, shows a diff of newly discovered models, merges into catalog.
- Quota results are NOT persisted — purely on-demand, no polling, no alert
  integration (YAGNI).
- Model editor gains a `wire_format` dropdown (openai/anthropic, default
  openai), shown only for opencode-go rows.
- Routing picker / Model Routes need no changes; models surface through the
  existing registry path.

## Error handling

- Quota: full fail-open as above; secrets never logged (existing
  redaction conventions apply).
- Executor dispatch: unknown `wire_format` → openai path (safe default;
  majority of models are openai-format). Unknown models behave like any
  other provider's model-not-found.
- Seed: idempotent by model name. Refresh: all-disabled/no-key rows get a
  clear error; upstream fetch failures never mutate the catalog.

## Testing

- `opencode_go_executor_test.go` — per-wire_format dispatch (URL, headers,
  auth), unknown-format fallback.
- `upstream_providers_quota_test.go` — httptest stubs: happy 3-window,
  404 fail-open, 401, timeout, `quota_url` override.
- upstreamsync render tests — opencode-go row → config section correctness
  (entries, headers, wire_format stamping).
- Seed/merge idempotency tests.
- Dashboard: helper tests for quota window rendering (existing
  `ProxyPoolsPage.test.js` pattern).

Verification: `gofmt -w .`, `go build -o /tmp/test-output ./cmd/server`,
`go test ./...`, `make dash-embed` for dashboard changes.

## Out of scope

- `openai-responses` target format (muse-spark-1.2-contributor falls back to
  the openai path).
- Background quota polling, quota alerts, quota caching.
- OmniRoute's synthesized CLI identity headers (User-Agent `opencode` etc.)
  as special code — users can set them via the row's `headers` map if
  needed.
- Dashboard-scrape quota fallback.
