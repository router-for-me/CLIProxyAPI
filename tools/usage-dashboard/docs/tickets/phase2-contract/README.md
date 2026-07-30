# Phase 2 — Type contract bridge

**Goal**: TypeScript types are generated from FastAPI's OpenAPI schema and
used across the front end. The moment a back-end response model changes,
`pnpm typecheck` fails in the front end.

## Tickets

| # | Ticket | Blocks |
|---|--------|--------|
| 2.1 | [`031-openapi-typescript-pipeline.md`](031-openapi-typescript-pipeline.md) | — |
| 2.2 | [`032-typed-fetch-client-and-query-hooks.md`](032-typed-fetch-client-and-query-hooks.md) | 2.1 |
| 2.3 | [`033-ci-stale-types-guard.md`](033-ci-stale-types-guard.md) | 2.1 |

## Phase exit criteria

- `make api-types` regenerates `frontend/src/api/types.ts` from
  `app.openapi()`.
- Every TanStack Query hook in `frontend/src/api/hooks/` is typed against
  the generated types.
- CI fails if `types.ts` is stale relative to the back-end response models.
