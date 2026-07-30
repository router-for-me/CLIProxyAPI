# ADR 0007: Error response shape — FastAPI default

**Status**: Accepted
**Date**: 2026-07-27

## Context

FastAPI returns `{"detail": "..."}` for `HTTPException` by default; the legacy
server returned `{"error": "..."}`. The aliases mutation endpoints need to
decide which shape to use.

The legacy server also returned 400 for invalid JSON bodies; FastAPI defaults
to 422 (`RequestValidationError`). We adopt the FastAPI 400 shape for parity
with the legacy status code, but use the FastAPI `{"detail": "..."}` body.

## Decision

We adopt the FastAPI shape as canonical:

- `HTTPException` → `{"detail": "<message>"}` (FastAPI default)
- `RequestValidationError` → `{"detail": "invalid JSON"}` with 400 status
  (status code matches legacy, body shape matches FastAPI convention)

## Consequences

Positive:

- Consistent with the rest of the FastAPI app (all other endpoints already
  use `{"detail": "..."}` via `map_query_errors` and `HTTPException`).
- The front end (Phase 3+) reads `detail`.
- The legacy HTML at `/legacy` (until deleted in Phase 5) is unaffected
  because it does not exercise error paths.

Negative:

- Clients ported from the legacy server that read `{"error": "..."}` will
  need to update to `{"detail": "..."}`.