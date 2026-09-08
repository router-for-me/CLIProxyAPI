# Dashboard Logs Page — Design

Date: 2026-09-08
Status: Approved

## Goal

Add a **Logs** page to the NixLLM dashboard (React + Vite SPA under
`web/dashboard/`) to view server logs (logrus output) via the existing
`GET /v0/management/logs` endpoint, with clear via `DELETE /v0/management/logs`.

No backend changes. No DB migration. No new config.

## Decisions (from brainstorming)

- **Scope**: server logs only (not request-error-logs).
- **Refresh**: manual refresh is the default; optional auto-polling toggle
  (3s interval) using the endpoint's incremental cursor.
- **Filtering**: level filter (INFO/WARN/ERROR/DEBUG/PANIC/FATAL) + text
  search, both client-side. Clear-logs button with confirmation modal.
- **Views**: toggle between **Raw** (monospace, level-colored) and **Table**
  (Timestamp | Level | Message) view.
- **Navigation**: sidebar entry in the **System** group, path `/logs`,
  icon `terminal`.

## Backend contract (existing, unchanged)

`GET /v0/management/logs` (`internal/api/handlers/management/logs.go`):

- Query params: `cursor` (incremental read), `limit`, `after` (timestamp).
- Response: `{ lines: string[], "line-count": int, "latest-timestamp": int64,
  "next-cursor": string, "cursor-reset"?: true }`.
- No cursor + `limit` → tail semantics (last N lines).
- `cursor-reset: true` means log rotation invalidated the cursor; re-tail.
- Returns **400** `"logging to file disabled"` when `logging-to-file` is off.
- `DELETE /v0/management/logs` removes rotated files and truncates the
  active log.

## Page UI

Toolbar (top):

- **Level** dropdown (All / INFO / WARN / ERROR / DEBUG / PANIC / FATAL).
- **Search** input (case-insensitive substring match on the line).
- **View** segmented toggle: `Raw | Table`.
- **Auto-refresh** toggle (default off, "● live" indicator when on).
- **Refresh** button (manual).
- **Clear…** button (red, confirmation modal → `DELETE /logs` → re-fetch).
- Counter: "N lines loaded / M shown after filter".

Log area (fills remaining viewport, scrollable):

- **Raw view**: `<pre>`-style monospace rows; level token colored
  (ERROR/FATAL red, PANIC bright red, WARN amber, DEBUG dim gray).
  Filtered-out lines are skipped at render time.
- **Table view**: columns Timestamp | Level | Message, parsed with a logrus
  regex (`time="..." level=... msg="..."`); non-matching lines render
  verbatim in the Message column (safe fallback).
- **Auto-scroll**: only when the user is already near the bottom (±50px);
  never steals scroll while reading upward.

Status bar (bottom): last-fetch timestamp, loading indicator, error badge.

## Data flow & state

State in `LogsPage`: `lines` (accumulated raw lines), `cursor`,
`autoRefresh`, `viewMode`, `levelFilter`, `search`, `fetchState`,
`lastUpdated`, `disabledReason` (400 case).

Fetch flow:

1. **Initial load**: `getLogs({limit: 1000})` (tail), store cursor.
2. **Poll tick (3s)**: `getLogs({cursor})` → append new lines. On
   `cursor-reset: true`, show a "log rotated" notice and re-tail.
3. **Manual refresh**: full re-tail (replaces contents) — keeps the
   implementation simple and avoids unbounded growth.
4. **Clear**: confirm modal → `clearLogs()` → re-fetch from scratch.

Guards:

- `lines` capped at **10,000** entries (FIFO drop of oldest); counter shows
  the cap when hit.
- Cursor lives in component state only (no URL param — the page is
  ephemeral; YAGNI).
- Polling pauses while `document.hidden`, resumes when visible again.
- Network errors during polling do not stop the interval: show a "retrying…"
  badge; after 2 consecutive errors, pause polling and require a manual
  resume (avoids hammering a restarting server).

Level detection: per-line regex `level=(\w+)`; lines without a level are
treated as "other" and shown when filter is All.

## Error handling

- **400 logging-to-file disabled**: informative banner — "Logging to file is
  disabled in config.yaml (`logging-to-file: true` required)"; toolbar
  renders disabled.
- **403**: handled by the app-global auth flow (token invalid → login).
- **Network errors while polling**: see guards above.

## Files

1. `web/dashboard/src/pages/LogsPage.jsx` — new page (toolbar, raw/table
   views, polling).
2. `web/dashboard/src/api/client.js` — add `getLogs({cursor, limit})` and
   `clearLogs()`.
3. `web/dashboard/src/App.jsx` — add `/logs` route.
4. `web/dashboard/src/components/Sidebar.jsx` — System-group entry +
   `terminal` icon.
5. `web/dashboard/src/pages/LogsPage.test.js` — unit tests for the logrus
   line parser (including non-standard line fallback) and level/search
   filtering (pure functions; polling/cursor verified manually).

## Testing

- Backend: none (endpoint already covered by `logs_test.go`).
- Frontend: unit tests for parser + filters only (following
  `ProxyPoolsPage.test.js` precedent). Polling/cursor verified manually:
  initial tail, auto-refresh toggle, level filter, search, raw/table toggle,
  clear-with-confirmation, 400 banner, `npm run build`.

## Out of scope (YAGNI)

- Request-error-logs browsing.
- Shareable/permalink log view (no cursor in URL).
- Table-view sorting, time-range picker, backend-side filtering.
