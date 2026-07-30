# Usage Dashboard — Domain Context

This file is a **glossary** for the `tools/usage-dashboard` module. It is
intentionally devoid of implementation detail. Implementation decisions live
in `docs/adr/`.

## Purpose

A sidecar to CLIProxyAPI that collects per-request usage records from the
management usage queue, persists them in SQLite, and renders a local web
dashboard for historical token / cost / latency analytics. It never enters
the inference request path.

## Bounded Context

The dashboard is its own bounded context. It consumes one upstream input
(the CPA usage queue) and exposes one downstream surface (a web UI + JSON
API). It owns its own storage (`~/.cli-proxy-api/usage-dashboard/usage.sqlite`)
and lifecycle. It does not share types with the Go server process.

## Glossary

### Inputs and Sources

- **Usage Queue**: an in-memory, destructive-read queue inside the CLIProxyAPI
  Go process, exposed at `GET /v0/management/usage-queue`. Bounded by
  `redis-usage-queue-retention-seconds` (max 3600). Records the dashboard
  consumes.
- **Usage Record / Usage Event**: one item drained from the Usage Queue,
  persisted as one row in `usage_events`. Terms are interchangeable; the
  persisted form is an *event*, the in-flight form is a *record*.
- **Collector**: the component that polls the Usage Queue over HTTP and
  inserts rows transactionally. Runs as an asyncio task inside the FastAPI
  process.
- **Management Key**: the CLIProxyAPI management secret required to read the
  Usage Queue. Distinct from `dashboard_token`.

### Storage

- **usage_events**: the append-only table holding every consumed Usage
  Record. Schema v4.
- **account_hash**: a stable SHA-256 digest of the originating API key /
  OAuth identity. Used as the join key for aliases. Cannot be reversed to
  the original credential.
- **key_aliases**: table mapping `account_hash` → human-readable `alias`.
  Operator-defined; the dashboard never writes to it from the collector path.
- **collector_state**: key/value table persisting collector health (last
  poll timestamp, error counters) so the HTTP process can observe a
  collector running in the same process.
- **Schema Version**: integer in `schema_meta`. Currently 4. Migrations are
  forward-only and applied one-per-transaction.

### Queries and Aggregations

- **Range**: a `[from, to)` interval expressed as RFC 3339 UTC, OR one of the
  presets `today | 1h | 5h | 24h | 7d | 30d`. Explicit `from`/`to` wins.
- **Grouping**: bucket dimension for aggregations. Allowed values:
  `model | provider | day | hour`.
- **Price Coverage**: enumerated status of cost estimates for a given set of
  records. Values: `complete` (all records matched a price interval),
  `partial` (some records matched), `empty` (no records or no pricing),
  `unpriced` (pricing absent for every model involved).
- **Estimated Cost**: a monetary value derived from `pricing.json` × token
  counts. Never authoritative billing.

### Access Control

- **Loopback**: host in `{127.0.0.1, ::1, localhost}`. Dashboard access
  without a token is allowed only when the bind host is loopback.
- **Dashboard Token**: bearer token required when the dashboard binds to a
  non-loopback host. Read via `X-Dashboard-Token` header. Independent of
  the Management Key.

### Frontend Concepts (post-refactor)

- **Server State**: data that originates from the API (summary, timeseries,
  request rows, pricing, aliases). Owned by TanStack Query — caching,
  polling, invalidation, retries.
- **UI State**: client-only data (selected filters, column visibility,
  drawer open/close, chart mode toggles). Owned by Zustand stores.
- **View**: a top-level route. Currently `/` (Dashboard — overview KPIs
  and recent activity) and `/usage` (Usage Detail — full filterable
  analytics with tabs). A view scopes both its Server State cache keys
  and its UI state slices.
