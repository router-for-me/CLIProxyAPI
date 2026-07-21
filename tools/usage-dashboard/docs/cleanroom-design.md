# Usage Dashboard Clean-Room Design

**Date**: 2026-07-17
**Status**: Draft → Implementation

## Motivation

The original `tools/usage-dashboard` was adapted from an upstream project that did not ship a license. To avoid redistribution risk, the implementation is rewritten from documented behavior without access to the original source.

## Compatibility

- **SQLite schema**: v3 (same columns, indexes, schema_version table). Existing v3 databases are read directly.
- **API routes**: `/api/v1/health`, `/api/v1/summary`, `/api/v1/timeseries`, `/api/v1/models`, `/api/v1/requests`, `/api/v1/auth/check`, `/api/health`, `/api/summary`, `/api/requests` — same paths, same response shapes.
- **CLI commands**: `init`, `collect`, `serve`, `run`, `report`, `compact`, `quota` — same semantics.
- **Config**: same JSON/env schema.
- **Dashboard HTML**: same visual, cleared of inheritable IP, then restored with WCAG and state fixes.

## Modules

```
usage_dashboard/
├── __init__.py          # CLI entry point, command dispatch
├── config.py            # Config loading/env merge/defaults
├── storage.py           # SQLite connection, migrations, collector state persistence
├── collector.py         # HTTP queue poll, batch insert, lock, dead-letter counter
├── query.py             # Range/group_by/model params, summary/timeseries/requests
├── pricing.py           # Load, validate, effective-dated estimate_cost
├── server.py            # HTTP handler, auth, routing, error mapping
├── static/
│   └── index.html       # Dashboard HTML (embedded)
└── test_usage_dashboard.py  # All tests
```

### `config.py`

- `load_config() -> dict` — same logic, strict env types.
- `DEFAULT_CONFIG` — default values.
- `ensure_dirs()`, `db_path_for()`, `config_path_for()`, `data_dir_for()`.

### `storage.py`

- `db_connect(cfg) -> contextmanager[Connection]` — **new**: closes connection on exit, not just commits.
- `run_migrations(cfg)` — **new**: per-version DDL in explicit `BEGIN/COMMIT`; aborts on failure without partial commit.
- `_migrate_v1()`, `_migrate_v2()`, `_migrate_v3()` — v1/v2 identical to current schema; v3 rewritten to avoid `executescript()`.
- `_migrate_v3` fix: use individual `conn.execute()` for each DDL in a single transaction, with explicit `BEGIN/ROLLBACK` guards.
- `CollectorState` — persisting to SQLite `collector_state` table instead of only in-memory, so `serve` can see collector health.
- `CollectorLock` — same fcntl approach.

### `collector.py`

- `fetch_usage_batch(cfg, count)` — HTTP poll, same auth.
- `insert_usage(cfg, items)` — transactional, per-item isolation, returns `(inserted, duplicates, errors)`.
- `collect_once(cfg)` — **new**: if `errors > 0`, sets `last_poll_ok=False` and records error count. Logs dropped count.
- `collect_forever(cfg, stop_event)` — same loop, `time.sleep(interval)`.
- `CollectorState` — same fields, but persisted to DB so health endpoint is process-safe.

### `query.py`

- `resolve_range(qs)` — same logic, but `parse_rfc3339` **no longer** falls back to `now()` for invalid input. Strict parse raises `ValueError`.
- `resolve_models(qs)`, `model_clause(models)`, `resolve_group_by(qs)` — same.
- `query_summary(cfg, qs)` — same aggregation, **fixed**: `price_coverage` returns `"empty"` when `by_model` is empty.
- `query_timeseries(cfg, qs)` — same.
- `query_models(cfg, qs)` — same.
- `query_requests(cfg, qs)` — **fixed**: cursor pagination uses strict `<` instead of `<=` to avoid duplicate boundary rows.
- `max_limit(cfg, requested)` — same.

### `pricing.py`

- `load_pricing(cfg)` — same.
- `_validate_pricing(pricing)` — **new**: validates currency, interval overlaps, non-negative rates, effective_from format. Returns errors array.
- `price_for(pricing, model, ts_epoch)` — **new**: returns `None` for records before all intervals (no fallback to latest).
- `estimate_cost(cfg, records)` — uses `_validate_pricing` result; **fixed**: rejects invalid pricing with `ValueError` rather than silent corruption. If `_validate_pricing` returns errors, `estimate_cost` raises `ValueError` and the handler returns 500.

### `server.py`

- `make_handler(cfg)` — same `public_paths`, `_gate()`, `do_GET()` route table.
- `DashboardHandler` — **new** error mapping: `ValueError` from request validation → 400; `ValueError` from pricing/configuration → 500; other exceptions → 500 with stable message.
- `json_response()` — **new**: adds `X-Content-Type-Options: nosniff` header.
- `is_authorized(handler, cfg)` — same.
- `is_loopback(host)` — same.
- `serve(cfg, ready=None)` — same.

### `static/index.html`

- Embedded in `__init__.py` as `DASHBOARD_HTML`.
- **Fixed**: model filter loading failure shows error message (not silent).
- **Fixed**: `loadModels()` uses current range filter, not the select's current value.
- **Fixed**: data refresh failure marks stale data as stale.
- **Added**: `next_cursor` handling for request history "load more".
- **Added**: `aria-label`, `role="status"`, `aria-live="polite"` on live regions.
- **Added**: `role="dialog"`, `aria-modal`, focus trap on login overlay.
- **Added**: accessible data table fallback for canvas charts.
- **Added**: `:focus-visible` outline.
- **Fixed**: KPI grid breakpoint at 960px instead of 900px to prevent overflow.
- **Fixed**: warning color `#b7791f` → `#92400e` (contrast 4.5:1).
- **Fixed**: control border `#d9dee7` → `#a0aec0` (contrast 3:1).
- **Added**: empty state messages for accounts, models, requests tables.
- **Added**: degraded collector banner.

## Data Flow

```
CPA management queue
    ↓ (HTTP poll, Bearer auth)
collector.py :: collect_once()
    ↓ (batch insert, transactional)
SQLite (usage_events, collector_state)
    ↑ (query)
server.py :: do_GET()
    ↓ (JSON response)
Browser dashboard
```

## Error Handling

| Category | Error | Handler Status | Response |
|----------|-------|----------------|----------|
| Request | Invalid `from`/`to` | 400 | `{"error": "..."}` |
| Request | Invalid `range` preset | 400 | `{"error": "..."}` |
| Request | Invalid `cursor` | 400 | `{"error": "invalid cursor"}` |
| Config | Unreadable config | Exit | SystemExit |
| Config | Invalid pricing | 500 | `{"error": "pricing configuration error"}` |
| Server | Unhandled exception | 500 | `{"error": "internal error"}` |
| Auth | Missing token for non-loopback | 503 | `{"error": "non-loopback bind requires dashboard_token"}` |
| Auth | Invalid token | 401 | `{"error": "unauthorized"}` |
| Collector | Network/auth failure | Health degraded | `last_poll_ok=false` |
| Collector | Parsing errors > 0 | Health degraded | `last_poll_ok=false` with error count |

## Testing

Test classes (same as current, plus new cases):

- `TestMigrations`: v1-v3, idempotence, **injected failure rollback proof**.
- `TestInsertion`: idempotent, sensitive fields, hash, malformed record isolation, three-tuple return.
- `TestQueries`: summary range, model filter, multi-model, empty result, invalid range, timeseries group_by, pagination stability, **strict `<` pagination**, `price_coverage="empty"` for empty.
- `TestPricing`: effective-dated, coverage, unknown model, before-all-intervals, all-models-costed, **invalid pricing fails fast**.
- `TestCollectorHTTP`: drain queue, auth failure, error redaction, missing key, **dropped records reported as degraded**.
- `TestCollectorLock`: exclusive lock, release/reacquire.
- `TestAuthGate`: loopback allowed, non-loopback rejected, token match.
- `TestConfigFailure`: invalid JSON raises.
- `TestServerHTTP` (new): HTTP handler returns correct status codes for:
  - valid request → 200
  - invalid query → 400
  - invalid pricing → 500 (not 400)
  - unauthorized → 401
  - not found → 404
  - health endpoint returns persisted collector state

## Deployment

- systemd: `EnvironmentFile=` instead of `%(cat ...)`.
- `.gitignore`: `*.log*` covers both `runtime.log` and `runtime.log.*.bak`.
- Deploy docs: corrected.
- `runtime.log.*.bak`: deleted from source tree.

## Non-Goals

- No HTML visual redesign (keep existing look).
- No new API endpoints.
- No new CLI commands.
- No new runtime dependencies.