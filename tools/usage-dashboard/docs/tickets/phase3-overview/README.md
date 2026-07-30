# Phase 3 — Dashboard overview view (`/`)

**Goal**: rebuild the `/` overview view in React, replacing what the legacy
HTML served at `/`. After Phase 3, `/` serves the React app (still mounted
under `/legacy` for comparison); `/usage` still serves legacy until Phase 4.

## Tickets

| # | Ticket | Blocks |
|---|--------|--------|
| 3.1 | [`041-shadcn-tailwind-theme.md`](041-shadcn-tailwind-theme.md) | — |
| 3.2 | [`042-router-and-shared-layout.md`](042-router-and-shared-layout.md) | 3.1 |
| 3.3 | [`043-filters-and-ui-state-stores.md`](043-filters-and-ui-state-stores.md) | 3.2 |
| 3.4 | [`044-overview-kpi-cards.md`](044-overview-kpi-cards.md) | 3.2, 3.3 |
| 3.5 | [`045-overview-charts.md`](045-overview-charts.md) | 3.4 |
| 3.6 | [`046-overview-recent-usage-and-drawer.md`](046-overview-recent-usage-and-drawer.md) | 3.4 |
| 3.7 | [`047-collector-health-indicator.md`](047-collector-health-indicator.md) | 3.2 |
| 3.8 | [`048-overview-e2e.md`](048-overview-e2e.md) | 3.4, 3.5, 3.6, 3.7 |

## Phase exit criteria

- React `/` view renders pixel-comparable to the legacy overview against a
  fixture DB.
- Playwright E2E confirms KPI values, chart rendering, and the detail
  drawer.
- `/legacy` still works for visual comparison.
