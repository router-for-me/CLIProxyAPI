# CLIProxyAPI Usage Dashboard

A sidecar that collects per-request usage records from CLIProxyAPI, persists
them in SQLite, and renders a local web dashboard with historical token, model,
latency, and estimated-cost analytics.

It sits **beside** CLIProxyAPI and reads the management usage queue. It never
enters the inference request path, so stopping it does not affect model
requests.

## Quick start

Requires Python >= 3.10 and `uv` (https://docs.astral.sh/uv/).

```bash
# 1. Install dependencies
uv sync

# 2. Configure CPA to emit usage records
#    Add to your CLIProxyAPI config:
#   usage-statistics-enabled: true
#   redis-usage-queue-retention-seconds: 3600

# 3. Create the data directory, SQLite database, and default config
export CLIPROXY_MANAGEMENT_KEY='<your CPA management secret>'
python3 usage_dashboard.py init

# 4. Start collector + dashboard together
python3 usage_dashboard.py run
# Dashboard listening on http://127.0.0.1:8321
```

For LAN access, edit `~/.cli-proxy-api/usage-dashboard/config.json`:

```json
{
  "dashboard_host": "0.0.0.0",
  "dashboard_port": 8321,
  "dashboard_token": "<a-long-random-token>"
}
```

Then open `http://<server-LAN-IP>:8321` and authenticate with the token.

## Development

### Prerequisites

- Python >= 3.10 with `uv` (https://docs.astral.sh/uv/)
- Node.js >= 18 with `pnpm` (https://pnpm.io/installation)

### Commands

| Command | Purpose |
|---------|---------|
| `make dev` | Start backend (uvicorn) + frontend (Vite) in parallel |
| `make dev-backend` | Start only the backend (http://127.0.0.1:8321) |
| `make dev-frontend` | Start only the Vite dev server (http://127.0.0.1:8320) |
| `make test` | Run pytest + vitest |
| `make lint` | Run ruff + oxlint |
| `make build-frontend` | `pnpm build` → `frontend/dist/` |
| `make e2e` | Run Playwright E2E tests |
| `make api-types` | Regenerate TypeScript types from OpenAPI schema |
| `make clean` | Remove build artifacts |

### Frontend development

The React app lives in `frontend/`. During development, run `make dev` to start
both the backend and the Vite dev server. The Vite dev server proxies API
requests to the backend. For production, the built frontend is served directly
by FastAPI via `StaticFiles` mount (see ADR 0006).

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     usage_dashboard.py                        │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────┐  │
│  │  Collector    │  │  FastAPI     │  │  StaticFiles       │  │
│  │  (asyncio     │  │  (uvicorn)   │  │  mount             │  │
│  │   httpx)      │  │              │  │  frontend/dist/    │  │
│  └──────┬───────┘  └──────┬───────┘  └────────────────────┘  │
│         │                  │                                  │
│         ▼                  ▼                                  │
│  ┌──────────────────────────────────────────────────────┐     │
│  │              SQLite (usage_events,                    │     │
│  │            collector_state, key_aliases)              │     │
│  └──────────────────────────────────────────────────────┘     │
│                                                              │
│  CPA management queue ──► Collector ──► SQLite               │
│                        ┌───────────┐                          │
│                        │  React    │                          │
│                        │  SPA      │                          │
│                        │ (Vite +   │                          │
│                        │  TanStack │                          │
│                        │  Query /  │                          │
│                        │  Zustand) │                          │
│                        └─────┬─────┘                          │
│                              │                                │
│  Browser ◄────── StaticFiles / JSON API ◄──── FastAPI        │
└──────────────────────────────────────────────────────────────┘
```

- **Backend**: FastAPI + uvicorn (asyncio) + SQLModel + httpx (async).
  OpenAPI schema at `/docs` and `/openapi.json`.
- **Frontend**: React 18 + TypeScript + Vite + React Router + shadcn/ui +
  Tailwind CSS + react-chartjs-2 (Chart.js).
- **State**: TanStack Query for server state, Zustand for UI state (see ADR 0003).
- **Collector**: asyncio task polling the CPA usage queue via httpx, running
  inside the same process (see ADR 0004).
- **Type contract**: OpenAPI schema → auto-generated TypeScript types (see ADR 0005).

Architecture decisions are recorded in `docs/adr/`.

## Commands

| Command | Purpose |
|---------|---------|
| `init` | Create data directory, SQLite database, and default config |
| `collect` | Run only the collector loop |
| `serve` | Run only the dashboard server (uvicorn) |
| `run` | Run collector + server together (local convenience) |
| `report <range\|from to>` | Print a JSON summary. Range presets: `today 1h 5h 24h 7d 30d`. Explicit RFC 3339 `from`/`to` overrides the preset |
| `compact --days N` | Delete usage rows older than N days |
| `quota --force` | Refresh the optional Codex quota snapshots (disabled by default; requires Codex OAuth files) |

## Configuration

Config lives at `~/.cli-proxy-api/usage-dashboard/config.json` (mode 0600) and
is merged over defaults. Environment variables override file values:

| Key | Env var | Default | Notes |
|-----|---------|---------|-------|
| `cliproxy_base_url` | `CLIPROXY_BASE_URL` | `http://127.0.0.1:8317` | CPA base URL |
| `management_key` | `CLIPROXY_MANAGEMENT_KEY` | `""` | CPA management secret (required for collect) |
| `poll_interval_seconds` | `POLL_INTERVAL_SECONDS` | `2` | Queue poll interval |
| `batch_size` | `BATCH_SIZE` | `100` | Records per poll |
| `dashboard_host` | `DASHBOARD_HOST` | `127.0.0.1` | Bind address; non-loopback requires `dashboard_token` |
| `dashboard_port` | `DASHBOARD_PORT` | `8321` | Bind port |
| `dashboard_token` | `DASHBOARD_TOKEN` | `""` | Bearer token for dashboard access |
| `quota_enabled` | `QUOTA_ENABLED` | `false` | Codex OAuth quota feature |
| `data_dir` | `USAGE_DASHBOARD_DATA_DIR` | `~/.cli-proxy-api/usage-dashboard` | SQLite + config location |

## Pricing

Optional effective-dated pricing lives at `<data_dir>/pricing.json`. Example:

```json
{
  "currency": "USD",
  "models": {
    "ts-gpt-56": [
      {
        "effective_from": "2026-07-01T00:00:00Z",
        "input_per_million": 1.25,
        "output_per_million": 10.0,
        "cached_input_per_million": 0.15
      }
    ]
  }
}
```

Models without a price entry are reported as unpriced; their token totals are
still counted, and cost coverage is marked incomplete. All monetary values are
**estimates**, not authoritative billing.

## API

OpenAPI schema is available at `/docs` (Swagger UI) and `/openapi.json`.

All analytics endpoints accept `from`/`to` (RFC 3339, UTC) and/or `range`
preset, and an optional repeated `model` filter. Explicit `from`/`to` wins.

- `GET /api/v1/health`
- `GET /api/v1/summary?range=24h&model=ts-gpt-56`
- `GET /api/v1/timeseries?from=...&to=...&group_by=model|provider|day|hour`
- `GET /api/v1/models?range=7d`
- `GET /api/v1/accounts?range=24h` — distinct account hashes in the time range, ordered by volume
- `GET /api/v1/errors?range=24h&model=...` — aggregate failed requests by status x model
- `GET /api/v1/prices` — read-only list of currently-effective model pricing intervals
- `GET /api/v1/requests?range=24h&model=...&limit=100&cursor=...`
- `GET /api/v1/aliases` — list account key aliases
- `PUT /api/v1/aliases` — upsert an account alias
- `DELETE /api/v1/aliases/{account_hash}` — delete an alias
- `GET /api/v1/export/csv?range=24h&model=...` — download raw usage as CSV
- `GET /api/v1/providers?range=24h` — provider breakdown
- `GET /api/v1/endpoints?range=24h` — endpoint breakdown

## Layout

```
usage_dashboard/
├── pyproject.toml              # Python project config, dependencies
├── Makefile                    # Dev/build/test/lint targets
├── usage_dashboard.py          # Thin shim that delegates to the package
├── usage_dashboard/
│   ├── __init__.py
│   ├── __main__.py             # CLI entry point, command dispatch
│   ├── config.py               # Config loading, env merge, defaults
│   ├── storage.py              # SQLite connection, migrations, collector state
│   ├── models.py               # SQLModel ORM models
│   ├── collector.py            # HTTP queue poll (sync fallback)
│   ├── collector_async.py      # HTTP queue poll (asyncio, httpx)
│   ├── query.py                # Range/group_by/model params, summary/timeseries
│   ├── pricing.py              # Load, validate, effective-dated estimate_cost
│   ├── api/
│   │   ├── __init__.py         # FastAPI app, auth middleware, routers
│   │   ├── schemas.py          # Pydantic request/response schemas
│   │   ├── health.py           # /api/v1/health
│   │   ├── aliases.py          # /api/v1/aliases CRUD
│   │   ├── export.py           # /api/v1/export/csv
│   │   ├── legacy_html.py      # /legacy route (legacy HTML dashboard)
│   │   └── routers/            # API route handlers
│   │       ├── summary.py      # /api/v1/summary
│   │       ├── timeseries.py   # /api/v1/timeseries
│   │       ├── models.py       # /api/v1/models
│   │       ├── accounts.py     # /api/v1/accounts
│   │       ├── requests.py     # /api/v1/requests
│   │       ├── errors.py       # /api/v1/errors
│   │       ├── prices.py       # /api/v1/prices
│   │       ├── providers.py    # /api/v1/providers
│   │       └── endpoints.py    # /api/v1/endpoints
│   └── static/                 # Vendored frontend assets (Chart.js)
├── frontend/
│   ├── package.json            # Node dependencies, scripts
│   ├── vite.config.ts          # Vite configuration
│   ├── tsconfig.json           # TypeScript configuration
│   ├── index.html              # HTML entry point
│   ├── src/
│   │   ├── main.tsx            # React entry point
│   │   ├── App.tsx             # Root component
│   │   ├── router.tsx          # React Router routes
│   │   ├── api/                # API client, hooks, types
│   │   │   ├── client.ts       # Fetch wrapper
│   │   │   ├── types.ts        # Auto-generated TypeScript types
│   │   │   ├── keys.ts         # TanStack Query key factory
│   │   │   └── hooks/          # Custom query hooks
│   │   ├── components/         # Reusable UI components
│   │   │   ├── ui/             # shadcn/ui primitives
│   │   │   ├── charts/         # Chart.js components
│   │   │   └── ...
│   │   ├── stores/             # Zustand UI state stores
│   │   ├── pages/              # Route page components
│   │   └── styles/             # Tailwind CSS globals
│   └── dist/                   # Production build output
├── e2e/                        # Playwright E2E tests
│   ├── overview.spec.ts
│   ├── usage.spec.ts
│   └── playwright.config.ts
├── tests/                      # Python tests
│   ├── test_api_*.py
│   ├── test_cli_run.py
│   └── ...
├── docs/
│   ├── adr/                    # Architecture Decision Records
│   ├── deployment.md           # Deployment guide
│   ├── rewrite-plan.md         # Planned rewrite phases
│   └── cleanroom-design.md     # Historical (superseded)
└── CHANGELOG.md
```

## Limitations

- The CPA usage queue is memory-backed, destructive on read, and bounded by
  `redis-usage-queue-retention-seconds` (max 3600). If the collector is down
  longer than the retention window, those records are lost. Run the collector
  continuously.
- Cost values are estimates derived from your `pricing.json`; they are not
  authoritative billing.
- Non-loopback dashboard access requires a bearer token and should be firewalled
  to trusted LAN CIDRs; use a TLS reverse proxy for untrusted networks.