# CHANGELOG

## [Unreleased] — FastAPI + React rewrite

### Breaking changes

- Python runtime now requires `uv sync` to install dependencies
  (fastapi, uvicorn, sqlmodel, httpx, pydantic).
- Legacy `BaseHTTPRequestHandler` server removed; back end is FastAPI.
- Legacy single-file `dashboard.html` removed; UI is a React SPA.
- Error response shape changed from `{"error": "..."}` to `{"detail": "..."}`.

### Added

- OpenAPI at `/docs` and `/openapi.json`.
- Auto-generated TypeScript types from OpenAPI.
- TanStack Query + Zustand state management.
- Playwright E2E suite.
- CSV export endpoint (`GET /api/v1/export/csv`).
- Key alias CRUD endpoints (`GET/PUT/DELETE /api/v1/aliases`).
- Provider and endpoint breakdown endpoints.
- Vite dev server for frontend development.
- `make dev` for parallel backend + frontend development.
- Architecture Decision Records in `docs/adr/`.

### Changed

- Frontend rebuilt as React 19 + TypeScript + Vite + shadcn/ui + Tailwind CSS.
- Backend uses FastAPI + uvicorn (asyncio) instead of stdlib BaseHTTPRequestHandler.
- Collector uses asyncio + httpx for HTTP polling.
- SQLModel for ORM (hand-written v4 migrations).
- Static files served by FastAPI StaticFiles mount from `frontend/dist/`.
- Build tooling: `uv` for Python, `pnpm` for frontend.

### Removed

- `usage_dashboard/server.py`
- `usage_dashboard/dashboard.html`
- `/legacy` route