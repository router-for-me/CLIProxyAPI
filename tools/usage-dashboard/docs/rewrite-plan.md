# Usage Dashboard Rewrite — Landing Plan

This plan lands the rewrite described in `docs/adr/0001` through `docs/adr/0006`.
Each phase is independently mergeable; each ends with the full test suite
green and the old code path still working. No big-bang cutover.

Reference: `CONTEXT.md` for domain terms, `docs/adr/` for decisions.

---

## Phase 0 — Foundation (no behavior change)

**Goal**: new project skeleton, old code path untouched and still the default.

- [ ] 0.1 Add `pyproject.toml` with `uv` lockfile; declare current stdlib-only
      runtime. `python3 usage_dashboard.py run` still works via the existing
      entry point.
- [ ] 0.2 Add `frontend/` skeleton: `package.json`, `vite.config.ts`,
      `tsconfig.json`, `src/main.tsx`, `src/App.tsx` rendering a placeholder
      page. `pnpm dev` serves `:5173` with proxy to `:8320`.
- [ ] 0.3 Add `Makefile` targets: `make dev` (uvicorn + pnpm dev in
      parallel), `make api-types` (regenerate TS types), `make build-frontend`,
      `make test`, `make lint`.
- [ ] 0.4 Add CI workflow: lint, type-check, test on every PR.

**Acceptance**: running `make dev` shows the Vite placeholder at `:5173`
with API proxy working; the existing `dashboard.html` is still served at
`/` by the old `BaseHTTPRequestHandler`.

---

## Phase 1 — FastAPI back end, shadow API

**Goal**: FastAPI app serves all current JSON endpoints under the same paths,
in parallel with the old server. Old HTML still served by the old server.

- [ ] 1.1 `usage_dashboard/api/__init__.py`: FastAPI `app` with `lifespan`
      that starts/stops the asyncio collector task (ADR 0004). CLI `run`
      command now launches uvicorn instead of `BaseHTTPRequestHandler`.
      CLI `serve` and `collect` still work as escape hatches.
- [ ] 1.2 `usage_dashboard/models.py`: SQLModel table classes for
      `usage_events`, `key_aliases`, `collector_state`. Match the existing
      v4 schema exactly. Schema-consistency test asserts
      `SQLModel.metadata` matches the applied schema on a fresh DB.
- [ ] 1.3 Migrate `query.py` logic into FastAPI route handlers under
      `usage_dashboard/api/` — one router per resource (summary,
      timeseries, models, accounts, requests, errors, providers,
      endpoints, pricing, aliases). Response models = SQLModel schemas.
- [ ] 1.4 `usage_dashboard/collector.py`: rewrite the poll loop as an
      `asyncio.Task` using `httpx.AsyncClient` and async SQLModel sessions.
      Keep the fcntl `CollectorLock` acquisition before the first poll.
      Maintain the persisted `collector_state` keys.
- [ ] 1.5 Delete `usage_dashboard/server.py`. The old `dashboard.html`
      is temporarily moved to be served by FastAPI at `/legacy` for
      visual comparison during migration.

**Acceptance**: every JSON endpoint in `test_usage_dashboard.py` returns
the same shape as before, against the same v4 SQLite DB. The legacy
dashboard at `/legacy` renders identically to today.

---

## Phase 2 — Type contract bridge

**Goal**: TypeScript types are generated from FastAPI's OpenAPI and used
across the front end.

- [ ] 2.1 `make api-types` target: import the FastAPI app, dump
      `app.openapi()` to `openapi.json`, run `openapi-typescript` to emit
      `frontend/src/api/types.ts` with `// AUTO-GENERATED` header.
- [ ] 2.2 `frontend/src/api/client.ts`: typed fetch wrapper keyed by
      route path. `frontend/src/api/hooks/`: one TanStack Query hook per
      resource (`useSummary`, `useTimeseries`, `useModels`,
      `useAccounts`, `useRequests`, `useErrors`, `useProviders`,
      `useEndpoints`, `usePrices`, `useAliases`).
- [ ] 2.3 CI step: regenerate types, `git diff --exit-code` to fail on
      stale types.

**Acceptance**: `frontend/src/api/types.ts` is generated; CI fails if a
developer edits it by hand or forgets to regenerate after an API change.

---

## Phase 3 — Dashboard overview view (`/`)

**Goal**: the `/` overview view is rebuilt in React, replacing the legacy
overview.

- [ ] 3.1 shadcn/ui + Tailwind installed; base dark theme tokens match
      the existing Linear/Vercel palette.
- [ ] 3.2 Layout: `App.tsx` with React Router routes `/` and `/usage`,
      shared header (view nav, time-range selector, refresh, alias
      management drawer). Header UI state in a Zustand `uiStore`.
- [ ] 3.3 Dashboard page: 8 KPI cards (split into 2 rows), Model
      Distribution chart (Token/Cost toggle), Token Usage Trend chart,
      Quick Actions, Recent Usage table (Top 12) with row-click detail
      drawer. Chart.js via `react-chartjs-2`.
- [ ] 3.4 Shared filter state in a Zustand `filtersStore` (range, from/to,
      granularity, selected models, selected accounts). TanStack Query
      cache keys include the filter state.
- [ ] 3.5 30-second auto-refresh via TanStack Query `refetchInterval`.
- [ ] 3.6 Collector health indicator in the header (uses
      `useCollectorHealth` hook, polls `/api/v1/health`).

**Acceptance**: the React `/` view is pixel-comparable to the legacy
overview; Playwright E2E confirms KPI values, chart rendering, and the
detail drawer against a known fixture DB.

---

## Phase 4 — Usage detail view (`/usage`)

**Goal**: rebuild the `/usage` view in React.

- [ ] 4.1 Usage page layout: 4 KPI cards, 2 chart rows (Model+Provider,
      Endpoint+Trend), filter bar (Model/Account/Provider/Endpoint
      multi-selects), tab bar (Usage / Errors / Ranking), Refresh/Reset/
      Column Settings/Export buttons, error-retry banner.
- [ ] 4.2 Tab 1 (Usage): requests table with cursor pagination via
      TanStack Query `useInfiniteQuery` + `nextCursor`; column settings
      persisted to a Zustand `settingsStore`.
- [ ] 4.3 Tab 2 (Error Requests): aggregated errors table, click-through
      to filtered requests.
- [ ] 4.4 Tab 3 (Account Ranking): aggregated by account_hash with
      alias join.
- [ ] 4.5 Per-row detail drawer.

**Acceptance**: Playwright E2E confirms all three tabs, pagination,
filters, and the drawer against a fixture DB.

---

## Phase 5 — Export, edge states, cutover

**Goal**: feature parity with the legacy dashboard, then delete legacy.

- [ ] 5.1 CSV export: FastAPI `/api/v1/export` endpoint + React button
      (downloads blob via `useExport` hook).
- [ ] 5.2 Column Settings modal: show/hide columns per tab, persisted in
      `settingsStore`.
- [ ] 5.3 Empty / loading / error states across every panel.
- [ ] 5.4 Static assets: Chart.js vendored, hashed asset caching, the
      `StaticFiles(html=True)` mount (ADR 0006).
- [ ] 5.5 `pnpm build` + commit `frontend/dist/`; root `python3
      usage_dashboard.py run` serves the React app at `/`. Delete
      `dashboard.html` and the `/legacy` route.
- [ ] 5.6 Update `README.md`, `docs/deployment.md`, systemd unit,
      docker-compose snippet, `.gitignore` for the new shape.

**Acceptance**: legacy HTML gone; full E2E suite green; smoke test on a
clean `git clone && uv sync && python3 usage_dashboard.py run` works with
no Node present.

---

## Out of scope (deliberately)

- New features beyond parity. Anything in the old `.scratch/` notes that
  is not visible in the current dashboard is not added during the rewrite.
- Backward compatibility shims for the old `BaseHTTPRequestHandler` server
  after cutover. The old code is deleted.
- Internationalization beyond the current `lang="zh-CN"` strings. Strings
  move to the React components as-is; no i18n framework is introduced.
