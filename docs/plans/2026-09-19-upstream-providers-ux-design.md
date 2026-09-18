# Upstream Providers — Health, Tabs, and Models Overhaul (2026-09-19)

Status: Draft for review · Scope: `web/dashboard/` Upstream Providers UX + one new server endpoint
Related: [2026-09-14-nixllm-pg-first-design.md](./2026-09-14-nixllm-pg-first-design.md),
[2026-09-05-upstream-provider-editor-page-design.md](./2026-09-05-upstream-provider-editor-page-design.md),
[2026-09-03-upstream-entry-routing-strategy-design.md](./2026-09-03-upstream-entry-routing-strategy-design.md)

---

## 1. Overview & Goals

**Why now:**

- The list page (`UpstreamProvidersPage.jsx`, 2,273 lines) has no live status indicator even though `StatusDot` + `liveStatus.js::isProviderRowLive` were just shipped (`f47328d1`, `8676cdd65`). Operators can't tell at a glance which providers are healthy, on cooldown, or breaker-open.
- The editor is one long vertical form spanning 6+ conceptual sections. As schema sections grow (opencode-go quota, cloak, per-entry routing strategy), it gets harder to find the field you want.
- The model list inside the editor is flat text rows — no autocomplete from `/models-catalog`, no bulk-select from `FetchModelsInline` results, no side-by-side layout.

**Four interlocking directions, delivered in one design doc + four sequential PRs:**

1. **PR 1 — StatusDot wiring + Health route** (smallest)
   - Wire `StatusDot` + `liveStatus.js` into the list rows.
   - Ship `GET /v0/management/upstream-providers/live-status` server-side.
   - Add a `Health` column to the table (sortable), filter chips above the table, an overview card in place of the existing stat strip.
   - New route `/upstream-providers/health` — a four-quadrant view mirroring `pages/model-routing/Picker.jsx`, partitioned Live / Cooldown / Breaker / Stale-Disabled. Manual refresh button only.

2. **PR 2 — Tabbed detail page** (medium)
   - New routes `/:id/:tab` where `tab ∈ {overview, models, entries, quota, test, logs}`.
   - Old `/upstream-providers/:id` redirects to `/:id/overview`.
   - Tab strip is filtered: Quota shows only for opencode-go, Test shows only for API-key providers, Entries shows only for entry-bearing providers, the others always show.
   - Overview tab holds the current schema-driven form (Identity/Endpoint/Routing/Behavior/Cloak), unchanged behavior, just relocated.
   - File split mirrors `pages/model-routing/`: `pages/upstream-provider-editor/{index.jsx, OverviewTab.jsx, ModelsTab.jsx, EntriesTab.jsx, LogsTab.jsx, QuotaTab.jsx, TestTab.jsx, useEditorState.js}`.

3. **PR 3 — Models tab overhaul** (medium)
   - Replace the inline `ModelListEditor` rows with a two-column layout: left = `ModelListEditor` rows (preserving name/alias/display-name/fork/force-mapping metadata), right = `FetchModelsInline` with a "Select all" + bulk-add button.
   - Add search filter on the rows.
   - For opencode-go, keep the per-row wire-format select.
   - Lift current "save the provider then seed/refresh" two-step into a single "Save & Seed" button in the Models tab footer.
   - Gated by a `modelsTab` boolean flag so PR 3 can be reverted without restoring the deleted Overview-tab section.

4. **PR 4 — List page split** (small)
   - Break `UpstreamProvidersPage.jsx` into `pages/upstream-providers/{index.jsx, Table.jsx, AliasesCard.jsx, ImportModal.jsx, BulkActions.jsx, filters.js, columns.jsx, health.js}`.
   - Pure refactor — no behavior changes, all existing tests still pass.

**Refresh model everywhere:** on-demand only. A `↻ Refresh health` button next to `↻ Refresh` fetches `/upstream-providers/live-status` and updates StatusDot in place. No auto-poll, no SSE.

**Non-goals:**

- No new auto-router / picker logic — that's `pages/model-routing/`, separate concern.
- No changes to the Go storage layer beyond the new `live-status` endpoint in PR 1.
- No new auth material model.
- No redesign of the `models-catalog` page.

---

## 2. Architecture & Data Flow

### High-level component map

```
/upstream-providers  (list)
└── pages/upstream-providers/index.jsx       — page shell, route → Table + AliasesCard
    ├── pages/upstream-providers/Table.jsx   — toolbar + StatusDot filter chips + table + bulk bar + PaginationBar
    │   └── pages/upstream-providers/columns.jsx — column definitions including <StatusDot> health column
    │   └── pages/upstream-providers/filters.js   — pure filter+sort helpers (existing behavior + health filter)
    │   └── pages/upstream-providers/health.js    — statusFromRow wrapper, getHealthSummary(rows)
    │   └── pages/upstream-providers/BulkActions.jsx
    └── pages/upstream-providers/AliasesCard.jsx — global OAuth model-alias editor (moved from UpstreamProvidersPage.jsx)

/upstream-providers/health  (new)
└── pages/upstream-providers/HealthPage.jsx — four-quadrant picker over the same provider list
    ├── pages/upstream-providers/HealthQuadrant.jsx
    └── pages/upstream-providers/HealthSummary.jsx (overview card with counts)

/upstream-providers/:id/:tab  (tabbed editor — replaces /:id)
└── pages/upstream-provider-editor/index.jsx — route shell, redirect /:id → /:id/overview
    ├── pages/upstream-provider-editor/TabBar.jsx   — filtered tab strip
    ├── pages/upstream-provider-editor/useEditorState.js — shared form state hook (lifted from current ProviderEditorForm)
    └── pages/upstream-provider-editor/OverviewTab.jsx   — schema-driven Identity/Endpoint/Routing/Behavior/Cloak (current behavior)
    └── pages/upstream-provider-editor/ModelsTab.jsx     — two-column list editor + FetchModelsInline
    └── pages/upstream-provider-editor/EntriesTab.jsx    — api_key_entries table + routing_strategy
    └── pages/upstream-provider-editor/QuotaTab.jsx      — opencode-go only; hidden for others
    └── pages/upstream-provider-editor/TestTab.jsx       — API-key only; hidden for OAuth
    └── pages/upstream-provider-editor/LogsTab.jsx       — embedded filtered views of /upstream-sync-log, /recent-events, /model-health

Reusable (already exists or new):
├── components/upstream/StatusDot.jsx — exists, just consumed
├── api/liveStatus.js — exists, gains a real endpoint
├── api/client.js — gains listUpstreamProviderLiveStatus()
└── pages/manage-cpa/FetchModelsInline.jsx — unchanged in PR 1-2; gains bulk-select in PR 3
```

### Data flow — health status (PR 1)

**List page:** on mount and on `↻ Refresh health` click, `Table.jsx` calls `fetchLiveStatus()` → `GET /v0/management/upstream-providers/live-status` → response shape:

```json
{
  "rows": {
    "12": { "is_live": true,  "cooldown_until": null,                     "breaker_open": false, "last_check_at": "2026-09-19T12:00:00Z", "last_error": null },
    "13": { "is_live": false, "cooldown_until": "2026-09-19T12:05:00Z", "breaker_open": true,  "last_check_at": "2026-09-19T11:59:30Z", "last_error": "429 rate-limited" }
  },
  "as_of": "2026-09-19T12:00:00Z"
}
```

The table merges this map onto the existing `providers` array in `useState` (no refetch of the full list). The Health column reads `statusFromRow()` (existing helper in `StatusDot.jsx`).

**Health route:** same fetch, partitioned into quadrants:

- **Live:** `is_live && !cooldown_until_is_future && !breaker_open`
- **Cooldown:** `cooldown_until > now`
- **Breaker:** `breaker_open`
- **Stale/Disabled:** otherwise

Each quadrant is a scrollable list of compact provider rows (Provider · Identifier · StatusDot · model count · action buttons). Clicking a row navigates to `/upstream-providers/:id/overview`.

**Stat-strip replacement:** the existing stat strip (Total / API Keys / OAuth / Disabled) becomes a small `HealthSummary` card with counts per quadrant, each click-to-filter. Clicking "Live" filters the underlying list to Live providers; clicking "Disabled" filters to disabled; clicking "All" clears. Counts are computed from the same data.

### Data flow — tabbed editor (PR 2)

`useEditorState(providerId)` lives at the editor route level and owns the form state, dirty marker, save handler, error set, and toast surface. It exposes:

```js
{
  state,                 // form object
  providerType,          // derived
  isEdit,                // boolean
  errors,                // derived from validate()
  touched,               // Set<string>
  dirty,                 // boolean
  saving, savingError,
  setField(path, value), // for Overview tab
  setModels(arr),
  setEntries(arr),
  touch(path),
  save(),                // POST or PUT, then toast, stay on current tab
  reset(),
  // read-only:
  availableModels,       // /v0/management/models-catalog
}
```

Each tab consumes `useEditorState()` and renders its own UI. The Save button stays in the editor's sticky header (not per-tab) so the model is "edit any tab → save once." The Models and Entries tabs mutate the same `state` via `setModels`/`setEntries`, no separate draft state. Logs and Test tabs are read-only against the persisted provider and don't touch form state. Quota tab mutates only via the existing opencode-go endpoints and shows a transient toast — no save flow.

### Data flow — Models tab (PR 3)

`ModelsTab.jsx` renders a two-column layout inside the editor:

- **Left column:** the existing `ModelListEditor` from `pages/manage-cpa/FormPrimitives.jsx` — rows of name/alias/display-name/fork/force-mapping, search input above, per-row delete + (for opencode-go) wire-format select.
- **Right column:** `FetchModelsInline` consuming the same `fetchedModels` shape but with a header checkbox for "Select all" + "Add selected" appends the chosen rows to `state.models`.

Both columns mutate the same `state.models` array via `setModels`. The tab footer shows the live count "12 models · 3 with aliases · 1 with fork flag" and a "Save & Seed" button that saves the provider then calls `POST /:id/seed-models` if `providerType === 'opencode-go'`.

**Non-goals for PR 3:** no drag-and-drop, no per-row inline validation beyond what `ModelListEditor` already does, no preview of which rows are exposed via `/v1/models`.

### Server changes (PR 1 only)

`GET /v0/management/upstream-providers/live-status` — backed by the existing `authManager.CooldownStateSnapshot()` + `IsProviderRowLive` server-side helper (commit `b96dd785`). No DB schema change. Handler lives alongside `internal/api/handlers/management/upstream_providers.go` as a new exported `LiveStatus` method. Returns 503 without `PGSTORE_DSN` (mirrors `runtime_config.go`).

---

## 3. Component Contracts & Tests

### PR 1 — StatusDot wiring + Health route

#### `api/client.js`

New export:

```js
// GET /v0/management/upstream-providers/live-status
// Returns { rows: Record<string, { is_live, cooldown_until, breaker_open, last_check_at, last_error }>, as_of: ISO }
listUpstreamProviderLiveStatus()
```

Throws on non-2xx; `liveStatus.js` already has a fetch wrapper that swallows + degrades to `{}` if the endpoint 404s, so this stays backward-compatible. No new query params.

#### `api/liveStatus.js`

- Existing `fetchLiveStatus()` continues to call the same endpoint and returns the same shape.
- Add a thin `coerceLiveStatusResponse(json)` that returns `{}` when the response is empty/missing — used by `Table.jsx` and `HealthPage.jsx` to handle the "endpoint not yet shipped" case identically.
- Existing `isProviderRowLive` stays.

#### `pages/upstream-providers/index.jsx` (slim page shell)

- Calls `useAsync(listUpstreamProviders, [])` — same hook, same deps as today.
- Calls `useAsync(listUpstreamProviderLiveStatus, [])` for the health map.
- Renders `Table.jsx` and `AliasesCard.jsx` — no other DOM.
- Holds the `liveStatus` map in state; passes it down.
- Holds the `setLiveStatus` updater so `Table.jsx`'s `↻ Refresh health` button can refresh without re-listing.

#### `pages/upstream-providers/Table.jsx`

Props: `{ providers, liveStatus, onRefreshHealth, onEdit, onDelete, onBulkAction, onAliasChange }`.
Internal state: `search`, `typeFilter`, `healthFilters` (Set: live/cooldown/breaker/stale/disabled/none), `sortKey`, `sortDir`, `page`, `pageSize`, `selected` (Set<id>).

Behavior:

- Computes `filtered = applyFilters(providers, {search, typeFilter, healthFilters, liveStatus})` via `filters.js`.
- Toolbar: search input, type select, "Clear filters", row count, **`↻ Refresh health` button** (PR 1) + existing `↻ Refresh` and `+ New Provider`.
- Above the table: a row of `<FilterChip>` toggles: Live (n), Cooldown (n), Breaker (n), Stale (n), Disabled (n), All. Multi-select. Empty selection = no health filter.
- Renders table with the existing columns plus a new **Health** column between Priority and Base URL. `<StatusDot status={statusFromRow({isLive, cooldownUntil, breakerOpen})} reason={last_error || cooldownReason(until)} />`.
- Bulk action bar: unchanged shape, gains a "Health" breakdown chip strip showing per-row health state when ≥1 row is selected.
- Pagination: unchanged.

#### `pages/upstream-providers/filters.js`

Pure functions, fully unit-tested:

```js
applyFilters(providers, { search, typeFilter, healthFilters, liveStatus }) → providers[]
getHealthSummary(providers, liveStatus) → { live, cooldown, breaker, stale, disabled, total }
statusFromHealth(row, liveEntry) → 'live'|'cooldown'|'breaker_open'|'stale'|'unknown'
cooldownReason(cooldownUntil) → string
```

- `healthFilters: Set<'live'|'cooldown'|'breaker_open'|'stale'|'disabled'>` — a row passes if its derived status is in the set; empty set = pass all.
- "Disabled" comes from the row's own `disabled` field, not from `liveStatus` (live/dead is orthogonal to enabled/disabled).

#### `pages/upstream-providers/HealthPage.jsx`

Route: `/upstream-providers/health`. Sidebar entry added in `Sidebar.jsx` under the same "Overview" group, label "Upstream Health", icon `activity`.

Layout:

- Top: `<HealthSummary>` card — five click-to-filter tiles (Live · Cooldown · Breaker · Stale · Disabled). Clicking a tile navigates back to `/upstream-providers` with the corresponding filter chip pre-applied via a `?health=` query param that `Table.jsx` reads on mount.
- Below: a four-quadrant picker styled identically to `pages/model-routing/Picker.jsx`'s grid:

```
┌─────────────────────────┬─────────────────────────┐
│   Live  (n)             │   Cooldown  (n)         │
│   [row] [row] [row]     │   [row] [row]           │
├─────────────────────────┼─────────────────────────┤
│   Breaker  (n)          │   Stale / Disabled (n)  │
│   [row] [row]           │   [row] [row] [row]     │
└─────────────────────────┴─────────────────────────┘
```

- Each row in a quadrant: Provider type badge · Identifier (truncated) · `<StatusDot>` · cooldown countdown or last error inline · action buttons (Edit, Toggle Disabled).
- Empty quadrant: a thin "No providers in this state" placeholder, not a blank box.

#### Tests for PR 1

- **`filters.test.js`** — unit tests for `applyFilters`, `getHealthSummary`, `statusFromHealth`. Cover: empty `liveStatus` (degrades gracefully), mixed disabled + cooldown, search across all fields, type filter, multi-select health filter, sort key interaction.
- **`HealthSummary.test.jsx`** — clicking a tile fires `onNavigate` with the right `?health=` value; counts match summary.
- **`Table.test.jsx`** — renders Health column with correct `<StatusDot>` per fixture row; `↻ Refresh health` click calls `onRefreshHealth`; health filter chip toggling re-filters rows; bulk action bar shows health breakdown.
- **`HealthPage.test.jsx`** — providers land in the correct quadrant; click-row navigates to `/upstream-providers/:id/overview`; empty quadrants render placeholders.
- **Server: `internal/api/handlers/management/upstream_providers_live_status_test.go`** — covers handler returning aggregated snapshot from `authManager.CooldownStateSnapshot()` + a 503 when PG isn't configured.
- **Existing `StatusDot.test.js`** — no changes needed.

---

### PR 2 — Tabbed detail page

#### `pages/upstream-provider-editor/index.jsx`

Route: `/upstream-providers/:id/:tab` with `tab ∈ {overview, models, entries, quota, test, logs}`. `index.jsx`:

- Redirect `/upstream-providers/:id` → `/upstream-providers/:id/overview` via `<Navigate>` (preserving query string).
- Load the provider via `useAsync(getUpstreamProvider, [id])`.
- Render `<TabBar>` + the active tab component, both wrapped in `<EditorStateProvider>`.
- Sticky header (Back to list · Title · Type badge · Save button · "● unsaved changes" marker) is shared across all tabs.

#### `pages/upstream-provider-editor/useEditorState.js`

A React context (`EditorStateContext`) + hook `useEditorState()`. Interface:

```js
{
  state, providerType, isEdit,
  errors, touched, dirty,
  saving, savingError,
  setField(path, value),
  setModels(arr),
  setEntries(arr),
  touch(path),
  save(),
  reset(),
  availableModels,
}
```

- Same validation pipeline as today's `form.js::validate` — lifted verbatim.
- `setModels` and `setEntries` deep-merge into `state.models` / `state.api_key_entries`.
- `dirty` is computed by shallow-comparing the current `state` to the initial loaded provider.

#### `pages/upstream-provider-editor/TabBar.jsx`

Reads `providerType` and `isEntryBearing` from context. Renders tabs in order: Overview · Models · Entries · Quota · Test · Logs.

- Hides Entries when `!isEntryBearing` (OAuth providers).
- Hides Quota when `providerType !== 'opencode-go'`.
- Hides Test when `providerType.startsWith('oauth:')` OR type not in `TESTABLE_TYPES` (gemini, claude, openai-compat, codex, xai, vertex, interactions, opencode-go).
- Always shows Overview, Models, Logs.
- Active tab = the URL `:tab` segment. Clicking a tab navigates via `<Link to=../{tab}>`.

#### `pages/upstream-provider-editor/OverviewTab.jsx`

Lifts today's `form-section` rendering loop out of `index.jsx` (the loop that walks `sections`). Unchanged behavior. Uses `setField` / `touch` from context.

#### `pages/upstream-provider-editor/EntriesTab.jsx`

Lifts `EntriesEditor.jsx` (current `pages/upstream-provider-editor/EntriesEditor.jsx`) into a tab. Calls `setEntries` from context. Gains a per-entry health column (small `<StatusDot>` per row) wired through `liveStatus`.

#### `pages/upstream-provider-editor/LogsTab.jsx`

Three collapsible sections in one tab:

1. **Sync events** — `listUpstreamSyncLog({ provider, limit: 50 })` via existing `client.js` wrapper.
2. **Recent routing events** — embeds a filtered slice of `RecentEvents.jsx` (factor `RecentEventsList` if needed).
3. **Model health** — embeds a per-provider slice of `ModelHealthPage.jsx` or a link to it.

#### `pages/upstream-provider-editor/QuotaTab.jsx`

For non-opencode-go providers the tab is hidden (not rendered with "Not applicable" body). When shown, near-verbatim lift of `OpenCodeGoPanel.jsx` plus a top-bar refresh button that calls `fetchUpstreamProviderQuota(id)` for every entry.

#### `pages/upstream-provider-editor/TestTab.jsx`

Same hiding rule. Lifts `TestPanel.jsx` unchanged. Adds the live status of each entry alongside the entry dropdown.

#### Tests for PR 2

- **`useEditorState.test.jsx`** — setField/setModels/setEntries mutate the right slice; dirty flips on first edit; validate() errors surface; save() calls POST vs PUT based on `isEdit`; reset() restores initial state.
- **`TabBar.test.jsx`** — only valid tabs render for a given provider type; OAuth hides Entries, Quota, Test; opencode-go shows Quota; claude-api-key shows Test but not Quota; clicking a tab navigates.
- **`OverviewTab.test.jsx`** — schema sections render in order; field edits flow through `setField`; `beforeunload` registered only when dirty.
- **`LogsTab.test.jsx`** — three sections render; sync events fetch with `provider` filter; "Open full model health →" link points to `/model-health?provider=:id` if model-health slice isn't feasible.
- **Route regression test** — visiting `/upstream-providers/:id` redirects to `/overview`; visiting `/upstream-providers/:id/models` renders ModelsTab; unknown tab falls back to Overview.
- **Snapshot** — minor snapshots for each tab to lock the layout.

---

### PR 3 — Models tab overhaul

Two-column grid inside `ModelsTab.jsx`:

```
┌──────────────────────────────────┬──────────────────────────────────┐
│  Configured models    (12)       │  Fetch from upstream   [↻]       │
│  [search input]                  │  Mode: ⦿ caller-side ◯ server-side│
│  ┌────────────────────────────┐  │  caller key: [password input]    │
│  │ name · alias · display     │  │                                  │
│  │ ─────────────────────────  │  │  ☐ Select all (47)               │
│  │ gpt-4o            [×]      │  │  ☐ gpt-4o      GPT-4o           │
│  │ gpt-4o-mini       [×]      │  │  ☐ gpt-4-turbo  GPT-4 Turbo      │
│  │ ...                         │  │  ☐ ft:gpt-4o    ...              │
│  └────────────────────────────┘  │  [Add selected (3)]              │
│  + Add model row                 │                                  │
└──────────────────────────────────┴──────────────────────────────────┘
                                                              [Save & Seed]
```

- Left column reuses `ModelListEditor` from `manage-cpa/FormPrimitives.jsx` verbatim — no changes to that primitive.
- A search input above the left column filters rows client-side by `name`, `alias`, `display-name`, case-insensitive.
- Right column is `FetchModelsInline` with a header checkbox ("Select all" selects/deselects all currently filtered rows), per-row checkbox, an "Add selected (N)" button that calls `setModels([...state.models, ...dedup(rows, by id)])`.
- For opencode-go: left column's `ModelListEditor` is replaced by `OpenCodeGoModelListEditor` with wire-format select per row.
- Footer: "N models · M with aliases · K with fork flag" + Save button (shared with editor header) + **Save & Seed** button (visible only for opencode-go). Save & Seed = `await save()` then `await seedUpstreamProviderModels(id)`, both must succeed for the toast to say "Saved and seeded."
- **Rollback flag:** `modelsTab` boolean in OverviewTab defaults to `true` after PR 3 lands; setting it to `false` restores the inline `models` section in OverviewTab.

#### Tests for PR 3

- **`ModelsTab.test.jsx`** — search filters rows; "Add selected" dedupes by id; "Save & Seed" only shown for opencode-go; wire-format select preserved on edit.
- **`FetchModelsInline.bulk.test.jsx`** — header checkbox toggles all filtered rows; "Add selected" disabled when 0 selected; appended rows appear in the configured list immediately.

---

### PR 4 — List page split

Pure file move. No behavior changes. Tests:

- All existing `UpstreamProvidersPage` tests pass after the move.
- A grep-based test confirms `UpstreamProvidersPage.jsx` no longer exists at the old path.

---

## 4. Error Handling, Edge Cases, and Accessibility

### PR 1 — Health wiring

| Scenario | Behavior |
|---|---|
| `GET .../live-status` returns 404 (not shipped) | `liveStatus.js` returns `{}`. Table renders rows with `statusFromRow` falling back to `stale`. StatusDot shows a gray "stale" dot. No toast, no error banner — silent degrade. |
| `GET .../live-status` returns 503 (PG not configured) | Same degrade as 404. |
| `GET .../live-status` returns 500 | `Table.jsx` catches and shows a small inline error above the Health column header; preserves the last good map. |
| `liveStatus` map is missing an entry for a row | `statusFromRow` defaults to `stale`. Next `↻ Refresh health` fills it in. |
| Stale `cooldown_until` in the past | `statusFromRow` treats as not-in-cooldown. `cooldownReason` returns null. |
| `breaker_open` field missing | Defaults to false. |
| `last_error` is a long string | Truncate to 80 chars in tooltip; full string in `aria-describedby` link "View full error". |
| 0 providers | Health column renders empty; stat strip shows zeros. |
| All providers disabled | Stale/Disabled quadrant has the rows; others show empty placeholders. |
| `disabled` field missing | `statusFromRow` treats as enabled. |
| Search query contains regex meta-chars | Plain `String.prototype.includes`, not regex. Safe. |
| `liveStatus` arrives before `providers` | Both `useAsync` hooks render skeletons until both resolve. |
| `↻ Refresh health` clicked while another fetch in-flight | Button `disabled={refreshing}` until request settles. |
| Many rows (1000+) | Quadrant list virtualizes if `>200` rows in a single quadrant (same pattern Picker.jsx uses). |

#### Accessibility (PR 1)

- StatusDot: `role="status"` + `aria-label` ("Live", "In cooldown for 2 minutes", "Circuit breaker open", "Stale"). Tooltip via `aria-describedby` pointing to a hidden text node.
- Health filter chips: `<button role="switch" aria-checked={active}>` with `aria-label="Filter: Live (12)"`. Space/Enter toggles, arrow keys move.
- Health column header: `aria-sort` reflecting sort direction when sorted by Health.
- `↻ Refresh health`: `aria-live="polite"` region announces "Health refreshed" on completion; `aria-live="assertive"` on error.
- Quadrant headings: `<h2>` per quadrant with `aria-label="Live providers, 12 total"`.
- Color is never the sole signal: every dot has a text label + textual badge in row mode.
- Focus management: `↻ Refresh health` completion keeps focus on the button. Stat-tile click that navigates moves focus to the new table heading.
- Reduced motion: count-up animations disabled under `prefers-reduced-motion: reduce`.
- Color tokens: emerald-500 / amber-500 / rose-500 / zinc-400/600 — verified during original StatusDot commit.

### PR 2 — Tabbed detail page

| Scenario | Behavior |
|---|---|
| Provider not found (404 on load) | `<ErrorBanner>` with "Back to list" button. No tab bar shown. |
| Save fails (4xx, e.g. 409 revision, 422 validation) | `savingError` exposed via context. Editor sticky header shows inline error pill next to Save button. Active tab remains in view; user fixes and re-saves. **No** silent revert. |
| Save fails (5xx) | Same, but error pill says "Couldn't save — retry" + `aria-live="assertive"`. |
| Save succeeds, dependent tab action fails | Per-tab inline error. Saving form unaffected. |
| Tab-specific fetch fails | Per-tab toast via existing pipeline. |
| Invalid tab in URL | Falls back to Overview tab; logs warning once. |
| Switching tabs while fetch pending | AbortController on tab's `useAsync` — aborts in-flight on tab change. |
| Crash with dirty state | `beforeunload` warns. No localStorage draft autosave in PR 2. |
| Quota tab open for non-opencode-go | Hidden (not rendered with placeholder). |
| Test tab open for OAuth | Hidden. |
| LogsTab with no events | Three sub-sections render empty states with their own `↻ Refresh`. |
| Models/Entries mutations while typing in Overview | Impossible (different tabs); form state is preserved across tab switches. |
| Entries tab adds invalid row | `validate()` produces entry-level error; header error pill shows count; clicking pill scrolls to first invalid entry. |
| Quota "Save & Seed" — save OK, seed fails | Toast: "Saved, but seeding failed — retry from Models tab." |
| Quota "Save & Seed" — save fails | Seed not called; save error shown. |

#### Accessibility (PR 2)

- Tab strip: `<nav aria-label="Provider sections">` containing `<Link>` per tab. Active: `aria-current="page"` + `aria-selected="true"`. Arrow keys cycle through tabs.
- Tab panels: `role="tabpanel"`, `aria-labelledby={tabId}`, `tabIndex={0}`.
- Sticky header: `role="region" aria-label="Editor controls"`. Save: `aria-keyshortcuts="Control+S"` (Cmd/Ctrl+S bound).
- Dirty marker: `aria-live="polite"` on first appearance.
- Error pill in header: `role="alert"` on first appearance.
- Scroll-to-first-error: clicking pill focuses first invalid field + `scrollIntoView({behavior:'smooth',block:'center'})`. Reduced-motion users get instant scroll.
- Per-field errors: keep existing `aria-invalid="true"` + `aria-describedby={errorId}` from `FormPrimitives.jsx`.
- `beforeunload`: registered only when dirty, suppressed during saving.
- Quota bars: `role="progressbar"` with `aria-valuenow/min/max/text`.

### PR 3 — Models tab

| Scenario | Behavior |
|---|---|
| Caller-side fetch fails | Inline error above fetch results: "Couldn't reach {base_url}." Configured models unaffected. |
| Caller key wrong / 401 | Same banner with "401 Unauthorized" sub-message + hint. |
| Server-side registry fetch fails | "Couldn't list server registry for this provider." |
| "Add selected" dedupes | Toast: "Skipped 2 duplicates." |
| "Save & Seed" — save OK, seed fails | Toast: "Saved. Seeding failed — you can retry from the Models tab." |
| "Save & Seed" — save fails | Save error shown; seed not called. |
| Search filter returns 0 rows in left | Empty state: "No models match '{query}'." |
| Wire-format invalid on opencode-go row | Inline row-level error; row highlighted. |
| Many rows at once | No artificial throttle; toast says "Added 100 models." |
| OAuth provider without entries | Models tab works for OAuth (no entries involved). |

#### Accessibility (PR 3)

- Search inputs: `<label>` linked via `htmlFor`. Clear-button on right column `aria-label="Clear search"`.
- Per-row checkboxes in right column: `<label>` wrapping row content + hidden checkbox; visible checkbox on hover/focus.
- "Select all": `aria-label="Select all 47 visible rows"` (count included).
- "Add selected (N)": `aria-disabled={selectedCount === 0}`.
- Two-column layout collapses to single column under 768px.
- Reduced motion: no animations on row add/remove.

### PR 4 — List page split

Pure refactor — error handling, edge cases, and a11y inherited from current `UpstreamProvidersPage.jsx`. Smoke tests per new file.

---

## 5. Implementation Sequencing, Migration Risks, Rollback

### Sequencing

```
PR 1 ───┐
         ├── PR 2 (depends on PR 1 — uses Health column in tabbed editor's Overview tab)
         │      │
         │      └── PR 3 (depends on PR 2 — ModelsTab only exists inside the tabbed editor)
         │             │
         │             └── PR 4 (independent — pure file split, can land any time after PR 3, or even after PR 1)
```

Realistic landing order: **PR 1 → PR 2 → PR 3 → PR 4**.

### PR 1 — StatusDot wiring + Health route

**Scope:**
- Server: `internal/api/handlers/management/upstream_providers.go` (add `LiveStatus` method), `internal/api/server_management.go` (route registration), `internal/api/handlers/management/upstream_providers_live_status_test.go` (new).
- Dashboard: `web/dashboard/src/api/client.js` (new export), `web/dashboard/src/api/liveStatus.js`, `web/dashboard/src/pages/UpstreamProvidersPage.jsx`. New: `web/dashboard/src/pages/upstream-providers/{Table, filters, health, columns, BulkActions}.jsx`, `HealthPage.jsx`.
- Tests: `filters.test.js`, `Table.test.jsx`, `HealthPage.test.jsx`, `HealthSummary.test.jsx`.

**Estimated diff size:** ~600 lines server (mostly test), ~1,200 lines dashboard.

**Risk:** **Low**. Purely additive. `liveStatus.js` already swallows 404s. The new Health route is a new URL — no existing bookmark breaks.

**Rollback:** revert the commit. The list returns to its current "disabled/active" pill. `/upstream-providers/health` 404s. No feature flag — graceful degrade is sufficient.

### PR 2 — Tabbed detail page

**Scope:**
- Dashboard: `web/dashboard/src/App.jsx` (route registration + redirect).
- Dashboard: `web/dashboard/src/pages/upstream-provider-editor/index.jsx` (slim shell), `form.js` (logic moves into `useEditorState.js`), `schemas.js` (unchanged).
- New: `pages/upstream-provider-editor/{useEditorState, TabBar, OverviewTab, EntriesTab, QuotaTab, TestTab, LogsTab}.jsx`. Existing `EntriesEditor.jsx`, `TestPanel.jsx`, `OpenCodeGoPanel.jsx`, `OAuthConnect.jsx` imported by new tabs.

**Estimated diff size:** ~1,400 lines (mostly new files; logic moved, not rewritten).

**Risk:** **Medium**. The current `index.jsx` is the entire editor; must preserve every behavior.

- `useEditorState` may differ subtly from current `ProviderEditorForm` internal state. Mitigation: public API matches current `useState` calls + handlers exactly; copy validation function verbatim; no refactor in this PR.
- Tab routing must preserve `?` query params. Mitigation: `<Navigate>` preserves query string.
- OAuthConnect must still work in create mode. Mitigation: PR 2 is edit-mode only. Create mode (`/upstream-providers/new`) is **not** touched in PR 2.

**Rollback:** revert the commit. The redirect from `/upstream-providers/:id` is removed. Any user mid-edit on new tabs loses in-flight edits — acceptable for a UI refactor; flagged in PR description.

**Migration note:** bookmarked `/upstream-providers/:id` URLs silently upgrade to `/overview`.

### PR 3 — Models tab overhaul

**Scope:**
- Dashboard: `web/dashboard/src/pages/upstream-provider-editor/ModelsTab.jsx` (new), `OverviewTab.jsx` (remove inline `models` section).
- Dashboard: `web/dashboard/src/pages/manage-cpa/FormPrimitives.jsx` (`ModelListEditor` unchanged; `OpenCodeGoModelListEditor` extracted).
- Dashboard: `web/dashboard/src/pages/manage-cpa/FetchModelsInline.jsx` (add header "Select all" + per-row checkboxes + bulk "Add selected").

**Estimated diff size:** ~700 lines.

**Risk:** **Low-Medium**. Smaller blast radius than PR 2.

- Subtle "auto-add on last-row blur" behavior preserved by reusing `ModelListEditor` primitive.
- `FetchModelsInline`'s caller-side state lifted into ModelsTab; API unchanged.
- "Save & Seed" combines two server calls; tested explicitly.

**Rollback:** revert the commit + set `modelsTab` flag to `false` to restore inline `models` section in OverviewTab. Flag defaults to `true` once PR 3 ships.

### PR 4 — List page split

**Scope:** delete `UpstreamProvidersPage.jsx`, create `pages/upstream-providers/{index.jsx, Table.jsx, AliasesCard.jsx, ImportModal.jsx, BulkActions.jsx}`. ~2,300 lines moved, ~50 lines net new.

**Risk:** **Very low**. Pure refactor. `filters.js` and `health.js` are pure (no React imports); lint rule enforces.

**Rollback:** revert. `UpstreamProvidersPage.jsx` restored; new files become dead code.

### Cross-PR risks

- **PG dependency surface** — all four PRs preserve 503 behavior without `PGSTORE_DSN`. No new env vars.
- **Feature flag** — none. Graceful-degrade + rollback flags in PR 3 are sufficient.
- **Auth/permission boundaries** — unchanged. All routes under `MANAGEMENT_PASSWORD`.
- **Performance** — list: two fetches on mount, one per `↻ Refresh health` click. Acceptable.
- **Bundle size** — +~3KB gzipped PR 1, +~6KB PR 2-4 combined. Acceptable.
- **Test runtime** — Vitest run growth <2s.

### Communication plan

- **PR 1:** "Adds per-row health status to Upstream Providers list. New `/upstream-providers/health` dashboard page. Graceful degrade if `/live-status` endpoint isn't deployed."
- **PR 2:** "Splits Upstream Provider editor into Overview / Models / Entries / Quota / Test / Logs tabs. Old `/upstream-providers/:id` URL redirects to `/overview`. Tab visibility is per provider type."
- **PR 3:** "Models tab now shows configured models and fetched-from-upstream models side-by-side. New search/filter and bulk-add. Save & Seed button combines save + model seeding for opencode-go providers."
- **PR 4:** "Refactor only — splits 2,273-line `UpstreamProvidersPage.jsx` into focused files. No behavior changes."

Each PR includes a "How to test" section with a <5-minute operator checklist.

---

## 6. Open Questions, Follow-Ups

### Open questions

1. **Cmd/Ctrl+S scope** — bind only on mutating tabs (Overview/Models/Entries) or everywhere? *Default: mutating tabs only; ignored while saving.*
2. **Health column sort order** — Breaker → Cooldown → Stale → Disabled → Live → Unknown (worst first), or alphabetical?
3. **Health filter "All" chip** — clears all filters on click, or a meta-chip? *Default: clear-on-click.*
4. **LogsTab deep-links** — open log entries in new tab (preserve editor) or same tab? *Default: new tab.*
5. **Create flow (`/upstream-providers/new`)** — PR 5 candidate to split into tabs, or leave as single-form + type picker? *Default: leave as-is.*
6. **`disabled` pill on list rows** — keep alongside StatusDot, or let StatusDot communicate it? *Default: keep both — operator setting vs. runtime health.*
7. **Health row "Disable" action** — act on row immediately (toast), or open bulk-action bar pre-populated? *Default: act immediately.*
8. **Opencode-go quota — read-only refresh vs. live push?** *Default: PR 2 = manual refresh; SSE push is a follow-up.*

### Follow-ups (out of scope)

- **PR 5** — Diff view across revisions (per-provider).
- **PR 6** — Per-entry health history sparkline.
- **PR 7** — Bulk model fetch across providers.
- **PR 8** — localStorage draft autosave.
- **PR 9** — `/upstream-providers/new` split into tabs.
- **PR 10** — `models-catalog` page overhaul.
- **PR 11** — `/upstream-providers/activity` timeline.
- **PR 12** — Test panel improvements (streaming, batch, comparison table).
- **PR 13** — Health row → Picker "Pin" deep-link (cross-feature).
- **PR 14** — `models-catalog` autocomplete as the only "add model" path.

### Risks explicitly accepted

- PR 2 is a large UI refactor — longer review. Includes comprehensive "How to test" checklist.
- PR 3 needs the `modelsTab` rollback flag.
- `liveStatus.js` silent degrade — mitigation: `/live-status` smoke test in CI.
- No auto-refresh — operators must click `↻ Refresh health`. Next PR adds optional toggle if it causes pain.

### Test plan summary

- **Server:** `upstream_providers_live_status_test.go`.
- **Dashboard unit tests:** `filters.test.js`, `Table.test.jsx`, `HealthPage.test.jsx`, `HealthSummary.test.jsx`, `useEditorState.test.jsx`, `TabBar.test.jsx`, `OverviewTab.test.jsx`, `ModelsTab.test.jsx`, `FetchModelsInline.bulk.test.jsx`, `LogsTab.test.jsx`, smoke tests per split file in PR 4.
- **A11y:** Vitest + Testing Library `axe-core` on each new component.
- **Visual regression:** manual review only.

### Final pre-commit checks

- All file paths verified against the actual repo.
- All claimed APIs correspond to existing client wrappers or new exports explicitly listed.
- No claims about future server behavior that aren't backed by the `authManager.CooldownStateSnapshot()` + `IsProviderRowLive` work already on `main`.
- All memory file references (`upstream-pool-routing-strategy.md`, `proxy-pools-design-status.md`, `routing-picker-key-alignment.md`, `api-list-key-helpers.md`, `pg-first-control-plane-status.md`) exist and are cited accurately.
- The four-PR sequencing is sound: each PR independently shippable + revertible; PR 4 parallelizable.
