# Design: Proxy Pools action buttons (selection + bulk bar, toolbar hierarchy, row actions)

**Date:** 2026-09-06
**Status:** Approved (brainstorming session 2026-09-06)
**Goal:** Bring the Proxy Pools page's action surface to parity with the dashboard's established bulk patterns — row selection with a bulk action bar (UpstreamProvidersPage patterns), a single-primary toolbar with a stoppable health check, and icon-only row actions that stay discoverable.

## Decisions

1. **Selection + bulk bar**: a leading checkbox column (width 36, ApiKeysPage pattern) with header select-all. A `BulkActionBar` (UpstreamProvidersPage shape: `N selected of M`, per-type chips, `⚡ Test` / `✓ Activate` / `⊘ Deactivate` / `✕ Delete` / `Clear`) appears when ≥1 pool is selected. Bulk semantics — server loop, one aggregate flash:
   - Test: concurrency-10 over the selection, result `{ok, failed}` from each response.
   - Activate/Deactivate: PUT `is_active` per pool.
   - Delete: a bulk confirm modal (BulkDeleteConfirmModal pattern — pool list + "cannot be undone") calling delete one-by-one; 409 bound pools are **skipped with a note** (`N deleted, M skipped (bound), K failed`), not a total failure.
   - Health check targets the selection when one exists, else all pools; label shows `Health check (N)`.
   - Selection is pruned on reload (deleted pools drop out).
2. **Toolbar hierarchy**: exactly one primary (`+ Add pool`, right side). `Deploy relay ▾` demoted to secondary; `Refresh` becomes an icon button (`⟳` + title); `Batch import` plain. While a health check runs, its button becomes `■ Stop` — an AbortController-backed flag cancels the remaining loop, partial results reload, flash "Health check stopped — N checked".
3. **Row actions**: icon-only, UpstreamProvidersPage-consistent glyphs (`✎`, `✕` replacing the 🗑 emoji) with title + aria-label; Test keeps its label only while running ("Testing…"). Page-scoped CSS raises the resting opacity of `.row-actions` to `0.25` (→1 on hover/focus-within) so actions stay discoverable for touch/keyboard.

## Pure helpers (exported, `node:test`)

- `summarizeBulkResults({deleted, skipped, failed})` → the aggregate flash message; covers the all-clean, skipped-bound, and failure shapes.
- `togglePoolSelection(ids, id)` → add/remove semantics for one pool id.

## Out of scope

Relay-deploy modals, import modal, table columns, backend API.
