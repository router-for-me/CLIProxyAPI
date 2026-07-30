# Phase 5 — Export, edge states, cutover

**Goal**: feature parity with the legacy dashboard, then delete legacy code
and docs. After Phase 5, the legacy `dashboard.html` and `/legacy` route
are gone, and the React app is the only UI.

## Tickets

| # | Ticket | Blocks |
|---|--------|--------|
| 5.1 | [`061-export-csv-button.md`](061-export-csv-button.md) | — |
| 5.2 | [`062-empty-loading-error-states.md`](062-empty-loading-error-states.md) | — |
| 5.3 | [`063-staticfiles-mount-and-vendoring.md`](063-staticfiles-mount-and-vendoring.md) | — |
| 5.4 | [`064-cutover-delete-legacy.md`](064-cutover-delete-legacy.md) | 5.1, 5.2, 5.3 |
| 5.5 | [`065-update-readme-deployment-docs.md`](065-update-readme-deployment-docs.md) | 5.4 |

## Phase exit criteria

- `git clone && uv sync && python3 usage_dashboard.py run` works with no
  Node installed; serves the React app at `/`.
- Legacy `dashboard.html` and `/legacy` route deleted.
- Full E2E suite green.
- README and deployment docs updated.
