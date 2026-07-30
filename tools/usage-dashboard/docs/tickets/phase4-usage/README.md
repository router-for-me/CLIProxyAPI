# Phase 4 — Usage detail view (`/usage`)

**Goal**: rebuild the `/usage` view in React with three tabs (Usage / Errors /
Ranking), filter bar, infinite-scroll requests table, and per-row detail
drawer.

## Tickets

| # | Ticket | Blocks |
|---|--------|--------|
| 4.1 | [`051-usage-page-layout-and-kpis.md`](051-usage-page-layout-and-kpis.md) | — |
| 4.2 | [`052-usage-filter-bar.md`](052-usage-filter-bar.md) | 4.1 |
| 4.3 | [`053-usage-tab-infinite-scroll.md`](053-usage-tab-infinite-scroll.md) | 4.1, 4.2 |
| 4.4 | [`054-errors-tab.md`](054-errors-tab.md) | 4.1 |
| 4.5 | [`055-ranking-tab.md`](055-ranking-tab.md) | 4.1 |
| 4.6 | [`056-usage-charts.md`](056-usage-charts.md) | 4.1, 4.2 |
| 4.7 | [`057-usage-e2e.md`](057-usage-e2e.md) | 4.3, 4.4, 4.5, 4.6 |

## Phase exit criteria

- All three tabs functional against a fixture DB.
- Infinite-scroll pagination works.
- Filters apply across all tabs and charts.
- Playwright E2E confirms parity with the legacy `/usage` view.
