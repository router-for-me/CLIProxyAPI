# Upstream Providers — Tabbed Detail Page (PR 2) — Design

Status: Draft · Scope: `web/dashboard/` Upstream Provider editor refactor into 6 tabs
Related: [2026-09-19-upstream-providers-ux-design.md](./2026-09-19-upstream-providers-ux-design.md) § PR 2,
[2026-09-19-upstream-providers-pr1-plan.md](./2026-09-19-upstream-providers-pr1-plan.md),
[2026-09-05-upstream-provider-editor-page-design.md](./2026-09-05-upstream-provider-editor-page-design.md)

---

## 1. Overview & Goals

**Why now:**
- The current editor (`pages/upstream-provider-editor/index.jsx`, ~719 lines) is one long single-form view spanning 6+ conceptual sections. As schemas grow (opencode-go quota, cloak, per-entry routing strategy), finding the field you want is hard.
- Design doc § 5 / PR 2 was always the next step after PR 1 ships live-status indicators.
- The 5-tab conditional filtering (Quota only for opencode-go, Test only for API-key, etc.) becomes trivial once each section is its own component — currently impossible because they're all interleaved in one form.

**Locked decisions (from brainstorming):**
1. **URL shape:** nested routes `/upstream-providers/:id/:tab` where `tab ∈ {overview, models, entries, quota, test, logs}`. Old `/upstream-providers/:id` redirects to `/overview`. Unknown tab values fall back to `/overview`. Each tab is a bookmarkable URL.
2. **Shared form state:** full React Context extraction. `EditorStateProvider` wraps the route shell; `useEditorState()` hook reads/writes. Validation pipeline (form.js::validate) lifted verbatim.
3. **All 6 tabs shipped in this PR** — Overview, Models, Entries, Quota (opencode-go only), Test (API-key only), Logs.

**What ships in PR 2:**
- 6 tab components + the route shell + `useEditorState` context + `TabBar`.
- Logs tab is new (other tabs are verbatim lifts).
- Models tab gets a per-entry StatusDot column wired to `liveStatus`.
- A `modelsTab` flag in OverviewTab to gate the relocated `models` field (PR 2 ships with `modelsTab: false` default; flipped to `true` after PR 3 lands).

**Non-goals:**
- No Models tab UX overhaul — that's PR 3. Models tab in PR 2 is a verbatim lift.
- No new form validation rules.
- No changes to create mode (`/upstream-providers/new`) — it stays the single-form view with the type picker step. Tabs apply only to edit mode (where `:id` is present).
- No new server endpoints.
- No Cmd/Ctrl+S binding.
- No `localStorage` draft autosave.

**Deferred (out of PR 2 scope):**
- Diff view across revisions (design doc PR 5).
- Per-field unsaved warnings (design doc PR 8 follow-up).
- Models tab UX overhaul (PR 3).

---

## 2. Architecture & Data Flow

### Component map

```
/upstream-providers/:id                    <Navigate to /overview />
/upstream-providers/:id/:tab               <Route element={<EditorRoute />}>
└── pages/upstream-provider-editor/
    ├── index.jsx          → Route shell. Loads provider via useAsync,
    │                        wraps in EditorStateProvider, renders TabBar +
    │                        sticky header + <Outlet /> for the active tab.
    ├── useEditorState.js  → EditorStateContext + useEditorState() hook.
    │                        Owns form state, validation, save, beforeunload.
    ├── TabBar.jsx         → Filtered tab strip. Hides Entries/Quota/Test
    │                        per provider type. Active = URL :tab.
    ├── OverviewTab.jsx    → Lifts the schema-driven section rendering loop
    │                        (Identity/Endpoint/Routing/Behavior/Cloak).
    ├── ModelsTab.jsx      → Lifts the inline `models` field + FetchModelsInline.
    │                        Verbatim lift in PR 2; PR 3 overhauls.
    ├── EntriesTab.jsx     → Lifts EntriesEditor.jsx. Adds per-entry StatusDot.
    ├── QuotaTab.jsx       → Lifts OpenCodeGoPanel.jsx. Hidden for non-opencode-go.
    ├── TestTab.jsx        → Lifts TestPanel.jsx. Adds per-entry live status.
    ├── LogsTab.jsx        → New. Three sub-sections (sync events, recent
    │                        routing events, model health).
    └── form.js            → KEEP. Provides validate() + toForm() that
                              useEditorState calls. No changes to its API.
```

### Route table (registered in `App.jsx`)

```jsx
<Route path="/upstream-providers/:id" element={<Navigate to="/upstream-providers/:id/overview" replace />} />
<Route path="/upstream-providers/:id/:tab" element={<EditorRoute />}>
  <Route index element={<Navigate to="overview" replace />} />
  <Route path="overview" element={<OverviewTab />} />
  <Route path="models" element={<ModelsTab />} />
  <Route path="entries" element={<EntriesTab />} />
  <Route path="quota" element={<QuotaTab />} />
  <Route path="test" element={<TestTab />} />
  <Route path="logs" element={<LogsTab />} />
  <Route path="*" element={<Navigate to="overview" replace />} />
</Route>
```

### `useEditorState` context interface

```js
{
  // Read
  state,                 // current form object (same shape as today's state)
  providerType,          // derived
  isEdit,                // boolean
  errors,                // derived via validate(state, schema, providerType, siblingNames, isEdit)
  touched,               // Set<string>
  dirty,                 // state !== initialLoadedProvider
  saving,                // boolean
  savingError,           // string | null
  availableModels,       // /v0/management/models-catalog
  liveStatus,            // /v0/management/upstream-providers/live-status

  // Mutations
  setField(path, value), // e.g. setField('base_url', 'https://...')
  setModels(arr),
  setEntries(arr),
  touch(path),

  // Lifecycle
  save(),                // POST or PUT, then toast, stay on current tab
  reset(),               // restore initialLoadedProvider

  // Derived helpers (so tabs don't have to re-derive)
  isEntryBearing,        // boolean — does this provider type have api_key_entries?
}
```

### TabBar filtering

TabBar reads `providerType` and `isEntryBearing` from context. Filters the tab list:

| Tab | Hidden when |
|---|---|
| Overview | (always shown) |
| Models | (always shown) |
| Entries | `!isEntryBearing` |
| Quota | `providerType !== 'opencode-go'` |
| Test | `providerType.startsWith('oauth:')` OR type not in `TESTABLE_TYPES` |
| Logs | (always shown) |

Active tab = URL `:tab` segment. Click navigates via `<Link to="..">` (sibling route).

### Risk: `useEditorState` extraction

The current `ProviderEditorForm` is the most-touched page in the dashboard. Risks:

1. **State shape drift** — if any field access pattern changes during the extraction, the editor silently breaks. Mitigation: lift `useState` calls verbatim into the context, no refactor.
2. **`validate()` semantics** — `form.js::validate` reads from a `form` object passed in. Context must preserve that shape exactly.
3. **`beforeunload` listener** — currently in `index.jsx`. Move to `useEditorState`. Register only when `dirty === true`, suppress during `saving`.
4. **OAuth create polling** — used only in create mode. PR 2 is edit-mode only. Don't move the polling logic.
5. **Tab state vs URL state** — TabBar uses URL `:tab` as source of truth. Tabs themselves don't need to know which tab is active.

---

## 3. Component Contracts

### `EditorRoute` (new in `pages/upstream-provider-editor/index.jsx`)

- Loads provider via `useAsync(() => getUpstreamProvider(id), [id])`.
- Loading: `<Spinner />` from Primitives.
- Error (404): `<ErrorBanner />` with "Back to list" button.
- Once loaded: wraps in `<EditorStateProvider initial={provider}>` + renders sticky header + `<TabBar />` + `<Outlet />`.

### `EditorStateProvider` (new in `useEditorState.js`)

- Accepts `initial` prop (the loaded provider object).
- Initializes `state` via `toForm(initial)` (existing helper in `form.js`).
- Computes `providerType`, `isEdit`, `isEntryBearing` from `initial`.
- Tracks `touched`, `dirty`, `saving`, `savingError`.
- Provides `setField`, `setModels`, `setEntries`, `touch`, `save`, `reset`, `availableModels`, `liveStatus`.
- Registers `beforeunload` listener when `dirty === true` and not `saving`.
- Loads `liveStatus` via `listUpstreamProviderLiveStatus()` on mount (same as Table.jsx uses).

### `TabBar.jsx`

Props: `{ activeTab, providerType, isEntryBearing }` (all from context).
Renders: 6-tab strip with hidden tabs filtered out. Each tab is a `<Link>` to the sibling route. Active tab gets `aria-current="page"`.

### `OverviewTab.jsx`

- Lifts the schema-driven section rendering loop verbatim from the current `index.jsx`.
- Replaces local `form` state access with `useEditorState()`.
- Replaces `handleFieldChange` with `setField(path, value)` from context.
- Gated by a `modelsTab` prop (default `false` in PR 2; flipped `true` after PR 3 lands). When `modelsTab === true`, the inline `models` field disappears from Overview (Models tab owns it).
- Preserves the OAuth connect sub-section rendering for create mode (still rendered in Overview even in edit mode if applicable — but OAuth connect is create-only, so Overview never renders it in edit mode).

### `ModelsTab.jsx`

- Verbatim lift of the current `models` field rendering + `FetchModelsInline` sub-section.
- Uses `state.models` + `setModels` from context.
- For opencode-go: also handles the per-row `wire_format` select.
- Search filter is **out of scope for PR 2** (PR 3 adds it).

### `EntriesTab.jsx`

- Verbatim lift of `EntriesEditor.jsx`.
- Adds a per-entry StatusDot column wired through `liveStatus[String(entry.id)]`.
- Uses `state.api_key_entries` + `setEntries` from context.

### `QuotaTab.jsx`

- Verbatim lift of `OpenCodeGoPanel.jsx`.
- Read-only against persisted provider — does not touch form state.
- Calls `fetchUpstreamProviderQuota(id, entryId)` per click, shows toast.
- Calls `seedUpstreamProviderModels(id)` / `refreshUpstreamProviderModels(id, { entryId })` per click, shows toast.

### `TestTab.jsx`

- Verbatim lift of `TestPanel.jsx`.
- Read-only against persisted provider.
- Adds the live status of each entry alongside the entry dropdown so operators know if they're testing a dead key.

### `LogsTab.jsx` (new)

- Three collapsible sections (using `<details>`/`<summary>` or a small `useState` toggle):
  1. **Sync events** — `listUpstreamSyncLog({ provider: id, limit: 50 })`. Renders a compact table: timestamp · kind · message.
  2. **Recent routing events** — embeds a filtered slice. If extracting from `EventsLive.jsx` is non-trivial, render a placeholder + a link "Open full events →".
  3. **Model health** — embeds a per-provider slice. If extracting from `ModelHealthPage.jsx` is non-trivial, render a placeholder + a link.

Each section has its own `↻ Refresh` button + loading/error states.

---

## 4. Error Handling, Edge Cases, Accessibility

### Error handling

| Scenario | Behavior |
|---|---|
| Provider not found (404) | `<ErrorBanner>` with "Back to list". No tab bar shown. |
| Save fails (4xx: 409 revision conflict, 422 validation) | `savingError` exposed via context. Sticky header shows inline error pill. Active tab remains in view. **No** silent revert of unsaved edits. |
| Save fails (5xx) | Same, but error pill says "Couldn't save — retry" + `aria-live="assertive"`. |
| Tab-specific fetch fails (LogsTab sync-events, Quota probe, Test probe) | Per-tab inline error. Saving form unaffected. |
| Invalid tab in URL (`/.../foo`) | Falls back to `/overview`; logs warning once. |
| Switching tabs while a fetch is pending | AbortController on the tab's `useAsync` — aborts in-flight on tab change. |
| Crash with dirty state | `beforeunload` warns. **No** `localStorage` autosave in PR 2. |
| Quota tab open for non-opencode-go | Hidden (not rendered with placeholder). |
| Test tab open for OAuth | Hidden. |
| LogsTab on a provider with no events | Three sub-sections render empty states with per-section `↻ Refresh`. |
| Models/Entries mutations while typing in Overview | Impossible (different tabs); form state preserved across tab switches. |
| Entries tab adds invalid row | `validate()` produces entry-level error; header error pill shows count; clicking pill scrolls to first invalid entry. |

### Edge cases

- **OAuth providers without entries** — Entries tab hidden. Models tab still works (OAuth can still have model aliases).
- **Per-entry health column on EntriesTab** — If `liveStatus` hasn't been fetched yet, the cell renders "—". If `liveStatus[id]` is missing for an entry id, same.
- **Quota tab "Save & Seed"** — out of scope for PR 2 (that's PR 3's ModelsTab). Quota tab is read-only here.
- **Tab order in URL** — React Router 6 matches in declaration order. Unknown tab values (`/.../foo`) fall through to the `*` route which redirects to `/overview`.

### Accessibility

- **Tab strip:** `<nav aria-label="Provider sections">` containing `<Link>` per tab. Active tab: `aria-current="page"` + `aria-selected="true"`. Keyboard: arrow keys cycle through tabs (custom hook).
- **Tab panels:** `role="tabpanel"`, `aria-labelledby={tabId}`, `tabIndex={0}`.
- **Sticky header:** `role="region" aria-label="Editor controls"`. Save button: `aria-keyshortcuts` not set in PR 2 (binding out of scope).
- **Dirty marker:** `aria-live="polite"` on first appearance.
- **Error pill:** `role="alert"` on first appearance.
- **Scroll-to-first-error:** clicking pill focuses first invalid field + `scrollIntoView({behavior:'smooth',block:'center'})`. Reduced-motion gets instant scroll.
- **Per-field errors:** keep existing `aria-invalid="true"` + `aria-describedby={errorId}` from `FormPrimitives.jsx`. No change.
- **`beforeunload`:** registered only when `dirty === true`, suppressed during `saving`.
- **Quota bars (in QuotaTab):** `role="progressbar"` with `aria-valuenow/min/max/text`.

---

## 5. Implementation Sequencing, Migration Risks, Rollback

### Sequencing

```
PR 2 ships as a single PR. Internal task ordering:
1. useEditorState context (foundation)
2. EditorRoute shell + route registration + redirect
3. TabBar (depends on context for providerType/isEntryBearing)
4. OverviewTab (the big lift)
5. EntriesTab + QuotaTab + TestTab (verbatim lifts)
6. ModelsTab (verbatim lift, will be overhauled in PR 3)
7. LogsTab (new)
8. Tests + review
```

### Risk: `useEditorState` extraction

This is the single highest-risk part of PR 2. Mitigation:

- Lift `useState` calls verbatim — no refactor.
- Keep `form.js::validate` and `form.js::toForm` untouched. The context just calls them.
- The `beforeunload` listener moves to the context's `useEffect`.
- The OAuth create polling stays in `index.jsx`'s create mode — not touched by PR 2.
- The `useEditorState` provider MUST match the current `ProviderEditorForm`'s exact state shape and handler signatures.

### Risk: Tab URL contract

The `/upstream-providers/:id/:tab` URL change is a **breaking change** for bookmarks. Mitigation: the `/upstream-providers/:id` redirect handles old bookmarks. Document in PR description.

### Risk: TabBar conditional filtering

TabBar hides tabs based on `providerType` + `isEntryBearing`. If either is derived incorrectly, tabs become unreachable or always-hidden. Mitigation: tests for TabBar covering each provider type.

### Migration risks

- PR 2 is a UI refactor with one URL change.
- Existing `/upstream-providers/:id` redirects to `/overview`. Bookmark break is silent (no user notice).
- No data migration. No new env vars.

### Rollback

Revert the merge commit. Old `/upstream-providers/:id` URL is restored. Users mid-edit on the new tabs lose in-flight edits — acceptable for a UI refactor; flagged in PR description.

### Communication plan

PR 2 description: "Splits Upstream Provider editor into Overview / Models / Entries / Quota / Test / Logs tabs. Old `/upstream-providers/:id` URL redirects to `/overview`. Tab visibility is per provider type."

How to test checklist (5-minute operator run):
- Open Claude API key provider — verify all 6 tabs (Overview, Models, Entries, Test, Logs) — Quota hidden.
- Open opencode-go provider — verify Quota tab visible.
- Open OAuth provider (e.g. `oauth:claude`) — verify Entries + Test + Quota hidden.
- Open `/upstream-providers/123/foo` — verify redirects to `/overview`.
- Edit a field on Overview, switch to Models — verify field state preserved, dirty marker on header.
- Click Save — verify success toast + redirect to editor URL.
- Cmd+W on a dirty tab — verify browser warns before close.

---

## 6. Open Questions, Follow-Ups

### Open questions

1. **Logs tab `↻ Refresh` per section vs single refresh?** Default: per section (each sub-section fetches its own data). Alternative: one global refresh button at top. Per-section is more useful for operators investigating specific issues.
2. **Entries tab per-entry StatusDot — show or hide when entry has no auth yet?** Default: show "—" placeholder. The status signal only matters when the auth has registered.
3. **Quota tab — keep the `seed-models` / `refresh-models` buttons here, or move to Models tab?** Default: keep here (current behavior). PR 3 will add a "Save & Seed" button on Models tab; Quota tab keeps the standalone buttons.
4. **Test tab — entry dropdown shows ALL entries including disabled ones?** Default: yes, but with the disabled flag visible. Operators may want to test a disabled entry to confirm it's actually disabled.

### Follow-ups (out of PR 2)

- **PR 3** — Models tab UX overhaul (two-column layout, search, bulk-add, Save & Seed).
- **PR 5** — Diff view across revisions (per-provider).
- **PR 6** — Per-entry health history sparkline.
- **PR 8** — `localStorage` draft autosave.
- **Polish** — Cmd/Ctrl+S binding, error pill auto-dismiss timeout, scroll-to-first-error keyboard shortcut.

### Risks explicitly accepted

- **Bookmark break** — `/upstream-providers/:id` → `/overview` redirect; no user-visible "moved" notice.
- **PR 2 is a large UI refactor** — longer review. Includes comprehensive "How to test" checklist.
- **`useEditorState` extraction** — relies on the current `ProviderEditorForm`'s state shape being correctly lifted. Tests cover the round-trip; manual QA covers the UX.
