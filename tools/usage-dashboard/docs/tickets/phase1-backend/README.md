# Phase 1 — FastAPI back end, shadow API

**Goal**: FastAPI serves every current JSON endpoint under the same paths as
the legacy server, against the same v4 SQLite DB. The legacy HTML is served
by FastAPI at `/legacy` during migration. The asyncio collector lives inside
the FastAPI process.

All endpoints in the legacy `test_usage_dashboard.py` must pass unchanged
against FastAPI — they are the **contract**.

## Tickets

| # | Ticket | Blocks |
|---|--------|--------|
| 1.1 | [`021-sqlmodel-tables-schema-consistency.md`](021-sqlmodel-tables-schema-consistency.md) | — |
| 1.2 | [`022-fastapi-app-lifespan-collector.md`](022-fastapi-app-lifespan-collector.md) | 1.1 |
| 1.3 | [`023-async-collector-httpx.md`](023-async-collector-httpx.md) | 1.1, 1.2 |
| 1.4 | [`024-api-routers-read-only.md`](024-api-routers-read-only.md) | 1.2 |
| 1.5 | [`025-api-aliases-mutations.md`](025-api-aliases-mutations.md) | 1.4 |
| 1.6 | [`026-api-export-csv.md`](026-api-export-csv.md) | 1.4 |
| 1.7 | [`027-serve-legacy-html-at-legacy.md`](027-serve-legacy-html-at-legacy.md) | 1.2 |
| 1.8 | [`028-cutover-cli-run-to-uvicorn.md`](028-cutover-cli-run-to-uvicorn.md) | 1.3, 1.4, 1.5, 1.6, 1.7 |
| 1.9 | [`029-delete-legacy-server-py.md`](029-delete-legacy-server-py.md) | 1.8 |

## Phase exit criteria

- `uv run python usage_dashboard.py run` starts uvicorn + asyncio collector
  in one process.
- Every test in `test_usage_dashboard.py` passes unmodified.
- `/legacy` renders the old dashboard HTML.
- `usage_dashboard/server.py` is deleted.
