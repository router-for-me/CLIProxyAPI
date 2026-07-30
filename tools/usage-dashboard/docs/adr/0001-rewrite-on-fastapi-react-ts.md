# ADR 0001: Rewrite on FastAPI + React + TypeScript

**Status**: Accepted
**Date**: 2026-07-27

## Context

The dashboard is a 2,700-line Python sidecar (`stdlib` `BaseHTTPRequestHandler`
+ `sqlite3` + `fcntl`) serving a 925-line single-file HTML/CSS/JS front end
(`dashboard.html`). It works. The pain points that triggered this decision,
confirmed during the design grilling, are:

1. **Multi-view state interference.** The two views (`/` overview and
   `/usage` detail) share one global namespace of `load*()` functions and
   DOM ids. State, filters, Chart.js instances, and the 30-second refresh
   timer leak across views. Changing one table can break the other.
2. **No type contract.** The Python query layer returns dicts; the JS reads
   fields by name. Field names are held in the maintainer's head and
   verified only at runtime.
3. **Single-file front end.** 570 lines of JS inside a `<script>` tag with
   no module boundary. Navigation by Ctrl-F.

The clean-room constraint that originally shaped this implementation
(`docs/cleanroom-design.md`) is **lifted**: the rewrite is a fresh
implementation with no upstream-derived IP.

## Decision

Rewrite both sides:

- **Backend**: FastAPI + uvicorn (asyncio) + SQLModel + httpx (async). CLI
  surface (`init` / `collect` / `serve` / `run` / `report` / `compact` /
  `quota`) and config/env schema are preserved.
- **Frontend**: React 18 + TypeScript + Vite + React Router v6 + shadcn/ui
  + Tailwind CSS + react-chartjs-2 (Chart.js retained).
- **State**: TanStack Query for server state, Zustand for UI state
  (see ADR 0003).
- **Tooling**: `uv` + `pyproject.toml` for Python, `pnpm` for the front end.
- **Deployment shape**: unchanged — single process, Python entry point,
  loopback-by-default HTTP bind.

## Alternatives considered

- **Front-end-only refactor** (split `dashboard.html` into ES modules,
  keep vanilla JS). Rejected: does not solve the type-contract gap, which
  is the second-largest source of maintainer friction.
- **Front-end-only rewrite** (React, keep stdlib Python). Rejected: the
  type-contract gap cannot be closed without an OpenAPI surface, and
  `BaseHTTPRequestHandler` cannot produce one without writing it by hand.
- **Merge into the Go server.** Rejected: couples the dashboard's release
  cadence to the proxy; forces every operator to ship dashboard code even
  when `usage-statistics-enabled: false`. The sidecar boundary is kept.
- **Next.js / SSR framework.** Rejected: requires a Node runtime in
  production. The product constraint "git clone + `python3 usage_dashboard.py
  run`" must hold; only the build step is allowed to need Node.

## Consequences

**Positive**

- OpenAPI schema generated automatically → TypeScript types generated
  automatically (ADR 0005). The type-contract gap closes by construction.
- Multi-view state interference is eliminated by per-view TanStack Query
  cache keys and per-view Zustand slices (ADR 0003).
- React + shadcn/ui matches the existing Linear/Vercel dark visual language.
- FastAPI's auto OpenAPI UI (`/docs`) is free developer ergonomics.

**Negative**

- Runtime dependencies grow from `0` to `fastapi`, `uvicorn`, `sqlmodel`,
  `sqlalchemy`, `pydantic`, `httpx`, `anyio`. Managed by `uv` + lockfile.
- Build tooling now requires Node + pnpm in the development environment.
  Production still has no Node requirement (ADR 0006).
- The 925-line `dashboard.html` and `test_usage_dashboard.py` are deleted
  in the final cutover step. Any operator-patched local fork will conflict.

**Neutral**

- `~/.cli-proxy-api/usage-dashboard/usage.sqlite` schema v4 is read as-is;
  no data migration (ADR 0002).
