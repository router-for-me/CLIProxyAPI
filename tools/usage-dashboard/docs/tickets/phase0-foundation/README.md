# Phase 0 — Foundation

**Goal**: Stand up the new project skeleton without touching the old code path.
After Phase 0, `python3 usage_dashboard.py run` still uses the legacy
`BaseHTTPRequestHandler`, and a new Vite placeholder is reachable at `:5173`
for development.

## Tickets

| # | Ticket | Blocks |
|---|--------|--------|
| 0.1 | [`011-pyproject-uv-skeleton.md`](011-pyproject-uv-skeleton.md) | — |
| 0.2 | [`012-frontend-vite-skeleton.md`](012-frontend-vite-skeleton.md) | 0.1 |
| 0.3 | [`013-makefile-and-ci.md`](013-makefile-and-ci.md) | 0.1, 0.2 |
| 0.4 | [`014-ci-gate.md`](014-ci-gate.md) | 0.3 |

## Phase exit criteria

- `uv sync && python3 usage_dashboard.py run` still works and serves the
  legacy dashboard at `:8320`.
- `cd frontend && pnpm install && pnpm dev` serves a placeholder at `:5173`
  that proxies `/api` to `:8320`.
- `make lint && make test` pass on a clean clone.
- CI runs on every PR.
