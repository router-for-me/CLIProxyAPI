# 0005: Usage aggregator and management panel

Date: 2026-11-16. Status: accepted (implementation notes are appended when it lands).

## Usage aggregator

- Upstream has no token/cost aggregation. `GET /observability/usage/queue` **pops** records, so the
  panel must never use it (it would steal records from Home/RESP consumers).
- proxx registers its own usage plugin that keeps totals per day × model × provider × client API key
  (requests, failures, tokens by type, cost) plus a ring buffer of recent requests.
- It runs regardless of `usage-statistics-enabled` (that flag only gates the upstream queue)
  and can be turned off with `proxx.usage.disabled`.
- Totals are snapshotted to a JSON file (default `proxx-usage.json` next to the config) every
  minute and on shutdown, so they survive restarts.
- Exposed under `/v8/management/proxx/usage/*`.

## Panel

- Fork `router-for-me/Cli-Proxy-API-Management-Center` as `iamkunal9/proxx-panel`, cloned at `../proxx-panel`.
- "Overhaul" = a reskin, not a rewrite: a dark-first theme inspired by Linear (dense, neutral
  greys, one accent colour, subtle borders) done through the theme tokens and layout shell, so
  upstream panel updates still merge.
- New pages in new files: Spend & usage, Model groups, Pricing.
- Deploy: the backend downloads `management.html` from the latest GitHub release of
  `remote-management.panel-github-repository`; point it at `iamkunal9/proxx-panel` and tag a
  release there.

## Implementation notes (2026-11-16)

What was built (backend only; the panel lives in `iamkunal9/proxx-panel`):

- `internal/proxx/usagestats`: `Store` implements `usage.Plugin` and is registered as the named
  plugin `proxx-usage`. Buckets are day (UTC, from `RequestedAt`) × served model × provider ×
  masked client API key. Each bucket counts `requests`, `failures`, `input_tokens` (full prompt incl.
  cache reads/writes), `output_tokens` (incl. reasoning), `cached_tokens` (cache reads),
  `reasoning_tokens`, `cost_usd` (sum of `cost_details.final_cost`) and `unknown_cost_requests`
  (no final cost and non-zero tokens). Token counts come from the canonical `TokenBreakdown`.
- API keys are stored as `util.HideAPIKey` + `#` + 6 hex chars of SHA-256 (`***#…` for short keys),
  so two keys with the same visible mask stay separate and the raw key is never stored.
- Recent requests: ring buffer (`proxx.usage.recent-limit`, default 500).
- `HandleUsage` only touches memory (one dispatcher goroutine serves all plugins). A separate loop
  writes `proxx-usage.json` (or `proxx.usage.file`; relative paths are relative to the config dir)
  every 60s when changed, atomically (tmp + rename), and once more on shutdown. It is loaded once on
  start. The store clock is injectable (`SetClock`), so tests never sleep.
- `internal/proxx/proxx.go`: `Start`/`Reload`/`Shutdown` facade, called with one line each from
  `Service.Run`, `commitConfigUpdate` and `Service.Shutdown`. Hot reload re-applies `disabled`,
  `recent-limit`, the snapshot path, and all pricing settings. The aggregator runs regardless of
  `usage-statistics-enabled` (the e2e config sets it to false).

Endpoints (all under `/v8/management/proxx`, management auth):

- `GET /usage/summary?from=YYYY-MM-DD&to=YYYY-MM-DD&group_by=model|provider|api_key|day`
  (defaults: last 7 UTC days, `model`) → `{"from","to","totals":{requests,failures,input_tokens,
  output_tokens,cached_tokens,reasoning_tokens,cost_usd,unknown_cost_requests},"rows":[{"key",...}]}`.
  Rows are sorted by cost, then requests (by date for `day`). 400 on bad dates/group_by.
- `GET /usage/recent?limit=N` (default 50) → `{"items":[{timestamp,model,alias,provider,api_key,
  failed,status_code,latency_ms,stream,input_tokens,output_tokens,cached_tokens,reasoning_tokens,
  cost_usd|null,cost_source}]}`, newest first. `status_code` is 200 for successful records.
- `DELETE /usage` → `{"status":"ok"}`; clears totals and recent items (persisted on next flush).
- `GET /pricing` → `{"catalog_source","updated_at","override_upstream","disabled","models":[{model,
  provider,input,output,cache_read,cache_write,source}]}` for every model in the registry
  (unknown prices are null with `source: "unknown"`).

Limitations:

- Records are per upstream attempt: retries and failovers each count as a request.
- Records still queued in the usage manager at the moment of shutdown may miss the final snapshot.
- Group-by is over the stored dimensions only (no alias grouping; alias is in recent items).
- The snapshot holds all days forever; `DELETE /usage` is the only pruning for now.
