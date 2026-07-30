# ADR Index

Architecture Decision Records for `tools/usage-dashboard`. Each ADR captures
a hard-to-reverse, surprising, trade-off-driven decision. See
`../CONTEXT.md` for the glossary.

| # | Title | Status | Date |
|---|-------|--------|------|
| 0001 | [Rewrite on FastAPI + React + TypeScript](0001-rewrite-on-fastapi-react-ts.md) | Accepted | 2026-07-27 |
| 0002 | [SQLModel + hand-written v4 migrations](0002-sqlmodel-handwritten-migrations.md) | Accepted | 2026-07-27 |
| 0003 | [Split state: TanStack Query for server, Zustand for UI](0003-split-state-tanstack-zustand.md) | Accepted | 2026-07-27 |
| 0004 | [Single-process uvicorn + asyncio collector](0004-single-process-asyncio-collector.md) | Accepted | 2026-07-27 |
| 0005 | [OpenAPI-generated TypeScript contract](0005-openapi-typescript-contract.md) | Accepted | 2026-07-27 |
| 0006 | [Vite dist mounted by FastAPI StaticFiles](0006-vite-dist-fastapi-mount.md) | Accepted | 2026-07-27 |

## ADRs considered and rejected

- **Keep stdlib Python + vanilla JS, only reorganize files.** Rejected: does
  not solve the type-contract gap or the multi-view state interference that
  motivated the refactor.
- **Merge the dashboard into the Go server process.** Rejected: couples
  release cadence; forces operators who do not want analytics to run a
  Python-free binary; the sidecar isolation is valuable.
- **SQLAlchemy + Alembic instead of SQLModel + hand-written migrations.**
  Rejected: schema is small and stable; the model/schema duplication of
  plain SQLAlchemy offers no benefit over the existing migration runner.
- **Redux Toolkit for state.** Rejected: boilerplate-to-value ratio is wrong
  for a 2-view dashboard.
- **ECharts instead of Chart.js.** Rejected: existing Chart.js config
  migrates cleanly via `react-chartjs-2`; ECharts' capability ceiling is
  not exercised by this dashboard.
