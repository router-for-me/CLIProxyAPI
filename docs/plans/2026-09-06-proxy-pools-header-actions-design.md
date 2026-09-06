# Design: Proxy Pools header actions polish (health check, batch import, deploy relay)

**Date:** 2026-09-06
**Status:** Approved (brainstorming session 2026-09-06)
**Goal:** Polish the three toolbar actions (Health Check, Batch Import, Deploy Relay) and lay the whole header out as a single right-aligned action row.

## Decisions

1. **Header layout**: adopt the existing `main__header` pattern (flex, space-between) — title/subtitle left, all actions right in one non-wrapping row via a new `.page-actions` class (`flex; gap 8px; nowrap`; wrap fallback below 900px). The `page-header`/`actions` divs currently have no CSS at all; page-scoped rules replace them. Labels shorten to fit: `■ Stop n/N` while a health check runs.
2. **Health check**: inline progress bar (reuse `.lb-bar`/`.lb-bar__fill`) rendered next to the button while running, with `n/total`. The completion flash counts per-pool results: `Health check: N healthy, M failed` (failed includes errors).
3. **Batch import**: live per-line preview under the textarea — `parseProxyLine` runs on every non-empty line, rendering `✓ url` chips (accent) and `✗ line N: message` (red); in-form dedupe marks repeats `dup`. The submit button reads `Import (N valid)` and is disabled at zero. After-import summary keeps its counts with green/amber/red coloring.
4. **Deploy relay**: per-field required validation from `RELAY_FIELDS` — Deploy disabled until complete, inline errors on touched fields; a one-line muted tip per platform pointing at where the token is created; while deploying, the button shows `Deploying…` plus a muted "May take up to 2 minutes — the worker must build first." note.

## Pure helpers (exported, `node:test`)

- `previewImportLines(text)` → array of `{line, ok, url?, error?, dup}` from the textarea content (dedupe by parsed URL, `*`-safe).
- `healthCheckFlash({ok, failed, stopped})` → the completion/stopped message.

## Out of scope

Bulk bar, row actions, add/edit modal, backend API.
