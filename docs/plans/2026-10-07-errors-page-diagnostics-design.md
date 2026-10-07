# Errors Page — Richer Diagnostic Info — Design

Status: Draft · Scope: richer error diagnostics on the dashboard Errors page (aggregation + per-error enrichment + error classification), PG-first
Related: [2026-09-17-pg-first-control-plane-design.md](./2026-09-17-pg-first-control-plane-design.md)

---

## 1. Overview & Goals

**Why now:**

The Errors page (`web/dashboard/src/pages/ErrorsPage.jsx`) renders the failed-attempt
stream from the `usage_errors` table. Today it is strictly a **row-by-row inspector**:
a KPI strip (errors in window / failed attempts / failure rate), a flat paged table
(Time, Request ID, Key, Provider, Model, Status, error_message, Latency), and a detail
modal that shows one row at a time.

That is enough to inspect a *single* failure but not to **analyse a problem**. An
operator facing "errors are up" cannot answer the questions that actually drive a fix:

- *Which* errors dominate? Are 2,000 failures really 2,000 distinct causes, or one
  cause repeated 2,000 times?
- *What kind* of error is it — rate limit, auth, timeout, bad request, upstream 5xx?
  The status code alone is ambiguous (429 can be quota or burst; 400 covers a dozen
  client mistakes).
- *When* did it start and how is it trending? A spike at 14:00 points at a different
  root cause than a slow all-day bleed.
- *What else* shares the pattern? Without grouping, correlating failures is manual.

**Locked decisions (from brainstorming):**

1. **Scope — all three**, delivered as one cohesive package:
   (a) aggregation & patterns, (b) per-error detail enrichment, (c) error-message
   grouping/classification.
2. **Layer — backend (Go + PG).** Aggregation and classification are implemented
   server-side so they are durable and reusable; the dashboard only renders. No
   client-side aggregation over a loaded page.
3. **Storage — new columns at flush time.** Classification and fingerprint are
   computed when the flusher builds the `UsageError` and persisted on `usage_errors`,
   not recomputed at query time. This keeps aggregation queries cheap and indexable.

**What ships:**

- Two new `usage_errors` columns: `error_class` (slug) and `error_fingerprint` (short hash).
- A pure `errorclass.Classify(statusCode, body)` helper used by the flusher.
- Message normalisation so structurally-identical errors share a fingerprint.
- Three new aggregation endpoints under `usage-stats/errors/*`.
- A new **"Error Patterns"** tab on the Errors page, a class-aware KPI strip and
  timeline, a Class column on the table, and a richer detail modal.

---

## 2. Data Model & Classification

### 2.1 New columns on `usage_errors`

Both nullable `TEXT`, added by the same idempotent `ALTER TABLE` pattern already used
for `route_model` / `client_ip` / `served_model` (see `internal/store/pg_usage_errors.go`
and the store's schema-init path). Pre-existing rows carry `NULL` and read back as `""`.

| Column | Type | Content |
|---|---|---|
| `error_class` | `TEXT` | Slug in the closed set below. Empty = pre-existing row. |
| `error_fingerprint` | `TEXT` | `left(sha256(class \| provider \| model \| normalized_message), 16)` — groups structurally-identical errors. |

**Closed class set** (stable keys; display labels resolved in the frontend):

`rate_limit`, `auth`, `invalid_request`, `not_found`, `permission`, `timeout`,
`connection`, `server_error`, `overloaded`, `content_filter`, `quota`, `other`.

### 2.2 Classification helper

New package `internal/store/errorclass` (pure, no DB, no globals, unit-testable):

```go
// Classify maps an upstream failure to a stable class slug.
func Classify(statusCode int, body string) string
```

Resolution order — body wins over keywords, keywords win over status fallback
(a specific cause keyword like `timeout` or `overloaded` is more actionable than
the generic bucket the status alone would give, e.g. `server_error`):

1. **Body, structured.** Parse `error.type` / `error.code` from the raw body with
   `gjson` (the proxy already does exactly this in
   `internal/runtime/executor/codex_executor_terminal.go`, whose mapping of
   `rate_limit_error` / `authentication_error` / `permission_error` / `not_found_error`
   / `invalid_request_error` is the precedent this reuses). Also recognise
   `insufficient_quota` and `content_filter` codes.
2. **Message keywords.** Lowercased substring scan for `timeout`, `deadline exceeded`,
   `connection refused`/`reset`, `overloaded`, `quota`, `rate limit`, `unauthorized`,
   `context length`, `content filter`.
3. **Status code.** 429→`rate_limit`, 401→`auth`, 403→`permission`, 404→`not_found`,
   400/422→`invalid_request`, 408/504→`timeout`, ≥500→`server_error`.
4. **Fallback:** `other`.

`Classify` **never returns an error and never panics** — an unparseable body simply
falls through to the status/keyword arms. Classification failure must never drop a row.

### 2.3 Fingerprint normalisation

`Fingerprint(class, provider, model, message) string` normalises the message before
hashing so that textually-different-but-structurally-identical messages collapse:

- Replace runs of digits (`\d+`) with `#`.
- Replace UUIDs with `<uuid>`.
- Replace hex tokens ≥ 16 chars with `<hex>`.
- Replace quoted strings longer than 24 chars with `<str>`.
- Collapse whitespace, trim, lowercase.

Then `sha256` over `class + "\x00" + provider + "\x00" + model + "\x00" + normalized`,
truncated to 16 hex chars. Example: `"rate limit exceeded for request abc-12"` and
`"rate limit exceeded for request xyz-98"` both normalise to
`rate limit exceeded for request ###-##` and therefore share a fingerprint.

### 2.4 Flusher wiring

`internal/store/pg_usage_flusher.go` — where it already builds `UsageError` from the
in-memory `Record` (the block that sets `FailStatusCode: failStatus`,
`ErrorMessage: record.Fail.Body`), add:

```go
errorClass := errorclass.Classify(failStatus, record.Fail.Body)
errorFingerprint := errorclass.Fingerprint(errorClass, record.Provider, model, record.Fail.Body)
```

and set both on the `UsageError`. This flows through `InsertError` /
`BatchInsertErrors` / `ImportLiteLLMErrors`, whose shared column list
(`usageErrorColumnList`, currently 35 columns) gains the two fields — remember to bump
`usageErrorColumnCount` and extend the positional `args` in **all three** insert paths
and the `scanErrorRow` dest list.

### 2.5 Backfill

One-shot, best-effort, idempotent `UPDATE` for pre-existing rows: set `error_class`
from `fail_status_code` alone (the raw body was never stored, so body-based
classification is impossible for old rows), and leave `error_fingerprint` empty.
Rows with a NULL class render as `—` / "Other" in the UI; group queries treat NULL as
its own bucket. No destructive rewrite, no row is skipped or lost.

---

## 3. Aggregation Endpoints

All three live in the `usage-stats` group, require `PGSTORE_DSN` (503 otherwise), and
reuse the existing `parseUsageStatsQuery` / `filterFromQuery` shape so they are scoped
by the same window / provider / key / model / request-id filters as the rest of the page.

### 3.1 `GET /v0/management/usage-stats/errors/summary`

One round-trip powering the enriched KPI strip:

```json
{
  "total": 2043,
  "by_class":   [{ "error_class": "rate_limit", "count": 1204 }],
  "by_status":  [{ "status": 429, "count": 1204 }],
  "by_provider":[{ "provider": "anthropic", "count": 900 }],
  "top_models": [{ "model": "claude-sonnet-4-5", "count": 700 }]
}
```

### 3.2 `GET /v0/management/usage-stats/errors/groups`

Leaderboard of grouped errors — the "Error Patterns" tab's data source.

- Params: `group_by = class | fingerprint | provider | model | status` (default
  `class`), `limit` (default 20, max 200).
- Validation mirrors `SelectErrorTop` / `aggregateGroupClause` — an unknown `group_by`
  returns **400**, not an empty list.
- Response:

```json
{ "group_by": "fingerprint",
  "groups": [{
    "key": "a1b2c3d4e5f60718",
    "error_class": "rate_limit",
    "sample_message": "rate limit exceeded for request abc-12",
    "count": 1204,
    "first_seen": "2026-10-06T02:11:00Z",
    "last_seen":  "2026-10-06T14:03:00Z",
    "providers": ["anthropic"],
    "models": ["claude-sonnet-4-5"],
    "last_request_id": "req_..."
  }] }
```

`sample_message` is a real captured `error_message` from the group (selected via the
group's representative row), so the operator reads an actual message rather than a hash.
`providers` / `models` are distinct-value arrays within the group.

### 3.3 `GET /v0/management/usage-stats/errors/timeline`

Per-class time series for the trend chart — a bucket × class matrix, reusing the
existing `SelectErrorTimeSeries` interval handling (`minute` / `hour` / `day`):

```json
{ "interval": "hour",
  "series": [{ "error_class": "rate_limit",
               "points": [{ "bucket": "2026-10-06T14:00:00Z", "count": 88 }] }] }
```

### 3.4 Query layer

New functions in `internal/store/pg_usage_errors.go`, each built on `buildWhereClause`
so the filter shape stays identical to `SelectErrors` / `SelectErrorCount`:

- `SelectErrorSummary(ctx, filter) (ErrorSummary, error)`
- `SelectErrorGroups(ctx, filter, groupBy string, limit int) ([]ErrorGroup, error)`
- `SelectErrorTimeline(ctx, filter, interval string) ([]ErrorClassSeries, error)`

A composite index `(requested_at DESC, error_class)` backs the class-scoped
aggregations. `group_by` is mapped to a *whitelisted* column expression (never string
interpolation of caller input), consistent with `dimensionColumn`.

The **listing** endpoint (`GET /usage-stats/errors`) also gains `error_class` and
`error_fingerprint` filters, carried on `UsageFilter` and applied by
`buildWhereClause` like every other filter. These two fields are errors-only (the
columns exist on `usage_errors`, not `usage_events`); the drill-down interactions in
§4.1 / §4.2 / §4.4 (chip click, group-row click, "same pattern" link) all resolve to
one of these two parameters, so the table, the KPI strip, and the patterns panel all
share a single filter vocabulary.

---

## 4. Dashboard (React)

All new UI reuses existing primitives (`DetailRow`, `CopyButton`, `badge`, `seg-group`,
`FilterSelect`, `Spinner`, `ErrorBanner`, `EmptyState`, `Modal`) and lives in
`ErrorsPage.jsx` plus new presentational components alongside it. Tab/selection state
uses `useState` + `localStorage`, mirroring the existing auto-refresh preference.

### 4.1 Enriched KPI strip

The three static stat cards become a class-aware strip:

- Total / failed / failure-rate remain.
- `by_class` renders as coloured chips (rate_limit=amber, auth=red, timeout=purple,
  server_error=dark red, …) with counts; clicking a chip filters the table below.
- A compact per-class timeline (from `/errors/timeline`) sits beneath, so a spike is
  visible without leaving the page.

### 4.2 "Error Patterns" tab

A new tab above the `Failed attempts` table hosting `ErrorGroupsPanel`:

- Segmented control to choose the grouping dimension: **Class / Message / Provider /
  Model / Status** (maps to `group_by`).
- Leaderboard table: class badge, `sample_message` (truncated, copyable), count,
  first/last seen, involved providers & models.
- Clicking a group row **applies that group as a table filter** (e.g. sets
  `error_class`, or a fingerprint filter) and scrolls to the detail table, so the
  operator drills from pattern → individual requests in one click.

### 4.3 Table changes

- New **Class** column (badge) immediately after Status.
- Row background subtly tinted by class so the table scans by type at a glance.
- error_message column unchanged.

### 4.4 Detail modal enrichment

- `error_class` badge in the modal header, beside `Error #id`.
- Surface persisted-but-currently-hidden fields: `entry_provider_key`, `network_rtt_ms`,
  `user_id`.
- Class label plus a **"N other errors with the same pattern"** link (via fingerprint)
  that opens the table filtered to that group.
- Action buttons: **Copy request ID**, **View logs** (navigate to LogsPage filtered by
  request_id), **Check provider health** (navigate to Provider Performance).

---

## 5. Error Handling & Edge Cases

- **Endpoints:** unknown `group_by` / bad filter → 400 (structured body, same envelope
  as `GetUsageErrors`); no PG → 503; PG error → 500 with a structured message. No
  panics in handlers.
- **Classification is non-fatal:** a parse failure yields `other`; it never fails a
  flush and never drops a row.
- **Legacy rows:** NULL `error_class` renders as `—` and falls into an "Other" bucket
  in group/summary queries; NULL `error_fingerprint` cannot match the fingerprint
  filter and is excluded from fingerprint grouping.
- **Degradation:** each new panel owns its loading / error / empty state
  (`ErrorBanner` + `EmptyState`), so one failing endpoint never blanks the page.
- **i18n-ready:** stored values are stable slugs; display labels are resolved in the
  frontend so wording can change without touching persisted data.

---

## 6. Migration & Rollout

1. Idempotent migration adds the two columns and the `(requested_at DESC, error_class)`
   index, run from the store's schema-init path like the existing `ALTER`s.
2. One-shot best-effort backfill of `error_class` from `fail_status_code` for old rows
   (§2.5); fingerprint left empty.
3. Flusher change ships with the migration so new rows are populated immediately.
4. Frontend is embedded via `make dash-embed` (dist copied into
   `internal/dashboardasset` before `go:embed` — see the dashboard embed note).

---

## 7. Testing

**Go unit**

- `errorclass.Classify` — table-driven: body-only, status-only, keyword-only,
  body-overrides-status, unparseable body, empty body, each class slug.
- `errorclass.Fingerprint` — digit / UUID / hex / quoted-string collapsing produces
  equal fingerprints for structurally-identical messages, unequal for different ones.
- Migration idempotency — run the schema init twice; columns/index exist once.
- Store queries against the existing PG test harness (`pg_usage_errors_test.go`):
  summary counts, groups by each dimension, timeline bucketing.

**Handler tests**

- Invalid `group_by` and bad filters → 400; missing PG → 503; response shape for each
  new endpoint.

**Flusher test**

- A failed `Record` produces a `UsageError` with non-empty `error_class` and
  `error_fingerprint`.

**Frontend tests** (`*.test.jsx`, following `ErrorsPage` / `ProxyPoolsPage.test.jsx`)

- `ErrorGroupsPanel` renders groups from data; shows `EmptyState` when empty.
- Clicking a group updates the table filter.
- Class badge renders in the table row and in the detail modal.

**Final verification**

```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
go test ./...
cd web/dashboard && npm run build
```

---

## 8. Out of Scope

- Client-side aggregation or classification (explicitly rejected in favour of backend).
- Re-computing class/fingerprint at query time (rejected in favour of flush-time columns).
- Alerting on error classes — the existing alerts subsystem (`alerts.go`) is untouched;
  wiring class-based alerts is a possible follow-up.
- Storing the raw upstream body inside `usage_errors` — bodies continue to live in the
  request-body capture store and are surfaced through the existing `EventBodiesSection`.
