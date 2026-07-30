# ADR 0005: OpenAPI-generated TypeScript contract

**Status**: Accepted
**Date**: 2026-07-27

## Context

The second-largest source of maintainer friction in the current dashboard
is **no type contract**: the Python query layer returns dicts, the JS front
end reads fields by name, and field names are held in the maintainer's
head. Renaming a column in SQL silently breaks the front end at runtime.

The rewrite moves the back end to FastAPI (ADR 0001) and the front end to
TypeScript. The question is how API response types are communicated
between the two languages.

## Decision

Make the OpenAPI schema the single source of truth, generated and consumed
automatically:

1. FastAPI emits `/openapi.json` from the Pydantic / SQLModel response
   models attached to every route.
2. `openapi-typescript` (a Node CLI) reads `/openapi.json` and emits
   `frontend/src/api/types.ts`, containing one TS interface per schema
   object and one path-typed object per route.
3. The TanStack Query hooks in `frontend/src/api/hooks/` import these
   types; the hand-written fetch wrapper in
   `frontend/src/api/client.ts` is generic and infers its return type
   from the route path.
4. The generation step runs:
   - In CI, on every PR touching `usage_dashboard/api/**`.
   - As a `pre-commit` hook for developers running locally.
   - As an explicit `make api-types` target for one-off regeneration.

The generated `types.ts` is checked into Git, so a deployed build never
silently uses a stale type file.

## Alternatives considered

- **Hand-written TS types, validated by a contract test.** Rejected:
  duplicates the schema in two languages; the contract test only catches
  drift after it has already shipped to a branch. Defeats the main goal.
- **gRPC / protobuf as the contract.** Rejected: gRPC-Web adds a transport
  layer the dashboard does not need; FastAPI's native OpenAPI is the
  idiomatic choice.
- **GraphQL.** Rejected: the API is a fixed set of read-only
  aggregations; GraphQL's selection-set value is unused.
- **tRPC.** Rejected: would require the front end and back end to share
  a TypeScript process; the back end is Python.

## Consequences

**Positive**

- Renaming a column in SQLModel → response model → OpenAPI → generated
  TS type → TypeScript compiler error in the front end. The contract is
  enforced at compile time, not run time.
- `/docs` (Swagger UI) and `/redoc` are free, always up to date.
- A future Go or Rust client of the same API can generate its own types
  from the same `/openapi.json`.

**Negative**

- A drift-prevention step is mandatory: if a developer edits
  `frontend/src/api/types.ts` by hand and forgets to regenerate, the
  types lie. Mitigated by:
  - CI step that regenerates and `git diff --exit-code`.
  - A header comment in `types.ts`: `// AUTO-GENERATED — do not edit`.
- `openapi-typescript` is one extra dev dependency (~30 KB).

**Neutral**

- The generation step needs the FastAPI app importable, which means the
  `make api-types` target starts the app in a "schema-only" mode
  (import the app, write `app.openapi()` to disk, exit).
