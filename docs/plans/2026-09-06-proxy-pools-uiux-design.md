# Design: Proxy Pools UI/UX polish

**Date:** 2026-09-06
**Status:** Approved (brainstorming session 2026-09-06; scope: row actions + add/edit modal)
**Goal:** Make the Proxy Pools page consistent with the dashboard's established component patterns — compact hover row actions with a real confirm modal for delete, colored status badges, toggle rows, and a sectioned add/edit modal with per-field inline validation and a live effective-URL preview.

## Decisions

1. **Approach: consistent polish.** Reuse existing components/classes only — `row-actions`, `StatusBadge`, `Modal`, `PasswordInput`, `ToggleRow`, `badge--*`. No new dependencies, no card-layout rewrite, minimal new CSS (`.pool-preview`).
2. **Row actions**: Test keeps its label (needs the `Testing…` state); Edit/Delete become compact icon+glyph buttons; all revealed on hover via the existing `.row-actions` opacity pattern.
3. **Delete** opens a confirm `Modal` (pattern of `BulkDeleteConfirmModal`): shows name + redacted URL + a bound-count warning when `bound_entry_count > 0`. A 409 from the server keeps the modal open with the count message inside it — no more `window.confirm`.
4. **Table simplification**: the `Strict` column is dropped (low-value; visible in the edit modal). Test status renders as `StatusBadge` (active→green, error→red, else muted "Not tested") with `last_error` as tooltip. `bound_entry_count` renders as a muted badge.
5. **Add/Edit modal** is sectioned ("Pool" / "Behavior"):
   - Type options carry descriptions; selecting a relay type shows an inline https note.
   - Proxy URL uses `PasswordInput` (credentials in the URL) with a **live effective-URL preview**: `socks5://redacted@host:1080?no_proxy=…&strict=true` for http pools, the plain base for relays (mirrors what `upstreamsync` renders).
   - `no_proxy` gets a format hint (exact host / `.suffix` / `*`); Strict + Active become `ToggleRow`s with consequence hints.
6. **Inline validation** via a pure exported `validatePoolForm` (name required ≥2 chars; URL absolute with scheme constrained per type; relay = https; no_proxy CSV with a lone `*`). Errors render under each field; Save is disabled while invalid.
7. **Pure helpers exported for `node:test`**: `validatePoolForm`, `effectivePreview`. Existing `parseProxyLine`/`formatTestStatus` tests unchanged.

## Error handling

- Delete 409/other errors stay inside the confirm modal; the modal closes only on success.
- Server save errors (409 duplicate name) remain in the shared `formError` under the sections.
- Success flashes keep the existing non-blocking `notice`.
- Optimistic Active toggle keeps its rollback-on-failure behavior.

## Out of scope

Relay-deploy modals, health-check bar, table→card rewrite, copy-to-clipboard, sorting/filtering.
