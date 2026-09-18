# Upstream Providers — Models Tab Overhaul (PR 3) — Design

Status: Draft · Scope: `web/dashboard/src/pages/upstream-provider-editor/ModelsTab.jsx` + `FetchModelsInline` + the `MODELS_TAB` flag in `OverviewTab.jsx`
Related: [2026-09-19-upstream-providers-ux-design.md](./2026-09-19-upstream-providers-ux-design.md) § PR 3,
[2026-09-19-upstream-providers-pr2-design.md](./2026-09-19-upstream-providers-pr2-design.md)

---

## 1. Overview & Goals

**Why now:**
- PR 2 lifted the existing inline `models` field rendering into `ModelsTab.jsx` (verbatim). The Models tab exists but has no UX improvements over the original.
- The current `FetchModelsInline` (`web/dashboard/src/pages/manage-cpa/FetchModelsInline.jsx`) shows fetched models as a checkbox list with a single "Add selected" button — but there's no "Select all", no search filter on the configured-models column, no combined view.
- The design doc's PR 3 was always the next major chunk after PR 1 + PR 2.

**What ships in PR 3:**
1. **Flip `MODELS_TAB` flag** in `OverviewTab.jsx` from `false` to `true` — Models tab owns the inline `models` field. (No duplicate rendering between Overview and Models.)
2. **Two-column layout** in `ModelsTab.jsx`:
   - **Left column:** `ModelListEditor` rows (name/alias/display-name/fork/force-mapping metadata, preserved) + a search input above the rows. Opencode-go keeps the per-row wire-format select.
   - **Right column:** `FetchModelsInline` upgraded with a "Select all" header checkbox + "Add selected (N)" footer button.
3. **Search filter** on the left-column rows (case-insensitive across `name`, `alias`, `display-name`).
4. **Save & Seed button** in the Models tab footer (visible only for opencode-go) = `await save()` then `await seedUpstreamProviderModels(id)`. Toast: "Saved and seeded" / "Saved, but seeding failed — retry from Models tab".
5. **Bulk dedup** — "Add selected" appends rows, deduped by `id`. Toast: "Added N models" or "Skipped N duplicates".

**Non-goals:**
- No drag-and-drop between columns.
- No per-row inline validation beyond what `ModelListEditor` already does.
- No preview of which rows are exposed via `/v1/models`.
- No `FetchModelsInline` redesign for the other dashboard pages (it's used in `manage-cpa` + `OverviewTab.jsx` legacy — those continue to use the existing single-column layout).

**Deferred (out of scope):**
- PR 5 — Diff view across revisions.
- PR 6 — Per-entry health history sparkline.
- PR 8 — `localStorage` draft autosave.
- PR 14 — `models-catalog` autocomplete as the only "add model" path (this PR is a stepping stone).

---

## 2. Architecture & Data Flow

### Component map

```
pages/upstream-provider-editor/
├── ModelsTab.jsx       (modified — two-column layout, search, Save & Seed)
├── OverviewTab.jsx     (modified — MODELS_TAB flipped to true)
└── useEditorState.jsx  (no change — already exposes setModels, state, save)

pages/manage-cpa/
└── FetchModelsInline.jsx  (modified — adds Select all + Add selected (N) footer)
```

### Data flow

`ModelsTab` continues to consume `state.models` + `setModels` from `useEditorState()`. New:

- **Search filter** is local to `ModelsTab`. The filter does NOT mutate `state.models` — it just controls which rows are visible. `state.models` stays the source of truth; submit still saves all rows including filtered-out ones.
- **`FetchModelsInline` "Add selected"** calls `onAddModels(arr)` where `arr` is the deduped (by id) union of `state.models` + the user's selected fetched rows. The component computes the dedup internally so `ModelsTab` doesn't need to know about it.
- **Save & Seed** — `ModelsTab` calls `await save()` from context, then if `providerType === 'opencode-go'`, calls `seedUpstreamProviderModels(state.id)`. Both must succeed for the success toast; partial failure surfaces a specific toast.

### Component contracts

#### `ModelsTab.jsx` (modified)

```jsx
// Local state
const [search, setSearch] = useState('');
const [seedRunning, setSeedRunning] = useState(false);
const [seedError, setSeedError] = useState(null);

// Derived
const filtered = useMemo(() => {
  if (!search) return state.models || [];
  const q = search.toLowerCase();
  return (state.models || []).filter(m =>
    (m.name || '').toLowerCase().includes(q) ||
    (m.alias || '').toLowerCase().includes(q) ||
    (m.display_name || '').toLowerCase().includes(q)
  );
}, [state.models, search]);

// Render
return (
  <div className="models-tab">
    <div className="models-tab__left">
      <SearchInput value={search} onChange={setSearch} />
      <ModelListEditor rows={filtered} onChange={setModels} fieldHints={...} />
    </div>
    <div className="models-tab__right">
      <FetchModelsInline form={state} onAddModels={setModels} mode={...} callerKey={...} />
    </div>
    <footer className="models-tab__footer">
      {state.models.length} models · {state.models.filter(m => m.alias).length} with aliases · {state.models.filter(m => m.fork).length} with fork flag
      {providerType === 'opencode-go' && (
        <button onClick={handleSaveAndSeed} disabled={saving || seedRunning}>
          {seedRunning ? 'Seeding…' : 'Save & Seed'}
        </button>
      )}
    </footer>
  </div>
);
```

#### `FetchModelsInline.jsx` (modified — bulk-select additions)

The existing component renders a checkbox list of fetched models + a single "Add selected" button. Changes:

1. Add a **"Select all"** header checkbox that toggles all currently-visible (search-filtered) rows. Indeterminate state when some-but-not-all are selected.
2. Change the bottom button text to **"Add selected (N)"** where N is the live count of selected rows. Disabled when N === 0.
3. Bulk dedup inside the component: when "Add selected" is clicked, compute the union of `state.models` (current) + selected fetched rows, dedup by id, and call `onAddModels(deduped)`.
4. Toast: "Added N models" or "Skipped M duplicates" (use the existing toast pipeline).

#### `OverviewTab.jsx` (one-line flip)

Change `const MODELS_TAB = false;` to `const MODELS_TAB = true;`. The inline `models` field disappears from Overview; Models tab is the sole owner.

### Data flow — Save & Seed

```js
async function handleSaveAndSeed() {
  setSeedRunning(true);
  setSeedError(null);
  try {
    await save(); // POST or PUT — same as the editor's existing save flow
    if (providerType === 'opencode-go' && initial?.id) {
      const result = await seedUpstreamProviderModels(initial.id);
      toast.success(`Saved and seeded (${result?.added ?? 0} models)`);
    }
  } catch (err) {
    setSeedError(err.message);
    toast.error(`Save & Seed failed: ${err.message}`);
  } finally {
    setSeedRunning(false);
  }
}
```

If `save()` throws, the seed never runs. If `save()` succeeds and `seed()` throws, the toast says "Saved, but seeding failed — retry from Models tab" (the operator can click the Models tab's standalone Seed button — currently in `OpenCodeGoActions`, still rendered in the Quota tab for opencode-go).

### Risk: `MODELS_TAB` flag flip

The Overview tab currently renders the inline `models` field. Flipping the flag removes that rendering. Risks:

1. **Search input parity** — the inline field had no search; the new Models tab adds one. Operators who relied on the inline rendering must learn to use the Models tab.
2. **Save state** — the inline field was inside the form's `save()` flow. The Models tab's two-column layout still mutates `state.models` via `setModels`, which is consumed by `save()`. No regression.
3. **Opencode-go wire-format** — currently inline in Overview; moves to Models tab via `OpenCodeGoModelListEditor`. Already lifted in PR 2.

The flag flip is reversible: set `MODELS_TAB = false` to restore the inline rendering if the overhaul regresses UX.

---

## 3. Component Contracts & Tests

### `ModelsTab.test.jsx` (modified — new tests)

```jsx
test('ModelsTab: search filters rows by name/alias/display-name', () => { ... });
test('ModelsTab: search is case-insensitive', () => { ... });
test('ModelsTab: Save & Seed button only shown for opencode-go', () => { ... });
test('ModelsTab: Save & Seed calls save() then seed() in order', () => { ... });
test('ModelsTab: footer shows model count + alias count + fork count', () => { ... });
```

### `FetchModelsInline.test.jsx` (modified — bulk-select tests)

```jsx
test('FetchModelsInline: Select all toggles all filtered rows', () => { ... });
test('FetchModelsInline: header checkbox is indeterminate when partial', () => { ... });
test('FetchModelsInline: Add selected (N) disabled when N === 0', () => { ... });
test('FetchModelsInline: Add selected dedupes by id against current state.models', () => { ... });
```

### `OverviewTab` regression check

- Existing tests in `editor-render.test.jsx` should still pass.
- The `MODELS_TAB = true` flip means `OverviewTab` no longer renders the `models` field — confirm no test asserts the presence of `models` rendering in Overview.

---

## 4. Error Handling, Edge Cases, Accessibility

### Error handling

| Scenario | Behavior |
|---|---|
| Search returns 0 rows | Empty state: "No models match '{query}'." Left column stays interactive (operator can clear search or add a new row). |
| Caller-side fetch fails | Inline error above fetch results (existing FetchModelsInline behavior). Configured models unaffected. |
| Caller key 401 | Same banner with "401 Unauthorized" sub-message. |
| Server-side registry fetch fails | "Couldn't list server registry for this provider." |
| Add selected dedupes | Toast: "Added N models" / "Skipped M duplicates." |
| Save & Seed — save OK, seed fails | Toast: "Saved, but seeding failed — retry from Models tab." Form state preserved. |
| Save & Seed — save fails | Seed never called. Save error surfaced in header pill (existing). |
| Empty initial state | Left column shows "+ Add model row" hint; right column shows fetch controls idle. |
| Opencode-go with invalid wire-format | Inline row-level error; row highlighted (existing OpenCodeGoModelListEditor behavior). |
| Many rows added at once | No artificial throttle; toast says "Added 100 models." |

### Edge cases

- **Search special characters** — plain `String.prototype.includes`, no regex. Same convention as `filters.js`.
- **Search across empty fields** — `|| ''` ensures undefined doesn't crash.
- **Opencode-go without wire-format on a row** — existing row validation handles it.
- **Selecting all then filtering** — Select all operates on currently-visible rows; filtering after selection drops hidden rows from the selection set.

### Accessibility

- Search input: `<label>` linked via `htmlFor`. Clear-button has `aria-label="Clear search"`.
- Select all checkbox: `aria-label="Select all {count} visible rows"` (count included).
- Add selected button: `aria-disabled={selectedCount === 0}`.
- Two-column layout collapses to single column under 768px.
- Reduced motion: no animations on row add/remove.

---

## 5. Implementation Sequencing, Migration Risks, Rollback

### Sequencing

PR 3 ships as a single PR. Internal task ordering:

1. Modify `FetchModelsInline.jsx` — add Select all + bulk dedup + N counter.
2. Modify `ModelsTab.jsx` — search input + two-column layout + footer counts.
3. Add `ModelsTab.test.jsx` and `FetchModelsInline.test.jsx` tests.
4. Flip `MODELS_TAB` flag in `OverviewTab.jsx` to `true`.
5. Verify build + tests.
6. Commit + merge.

### Migration risks

- **`MODELS_TAB` flag flip is the only behavior change for existing users.** Operators visiting Overview no longer see the inline `models` field — they must click the Models tab. The TabBar is already there (PR 2).
- The opencode-go wire-format select moves from Overview → Models tab. Same UX, different location.
- No data migration. No new env vars.

### Rollback

Revert the merge commit. `MODELS_TAB = false` restores the inline rendering. The new search + Save & Seed features revert cleanly.

### Communication plan

PR 3 description: "Models tab now shows configured models and fetched-from-upstream models side-by-side. New search/filter and bulk-add. Save & Seed button combines save + model seeding for opencode-go providers. The inline models field is removed from Overview — Models tab is now the sole owner."

How to test checklist:
- Open an API-key provider, navigate to Models tab — see two-column layout.
- Type a query in the search box — rows filter live.
- Click "Add selected" on the FetchModelsInline side — dedup toast appears.
- For opencode-go only: click "Save & Seed" — both calls succeed, success toast.

---

## 6. Open Questions, Follow-Ups

### Open questions

1. **Search debounce** — apply filter on every keystroke, or debounce 200ms? Default: every keystroke (simpler, matches `filters.js` convention).
2. **Save & Seed for non-opencode-go providers** — should the button hide entirely, or be visible but disabled? Default: hide entirely.
3. **Bulk add toast** — single toast with both counts ("Added 5 (skipped 2 duplicates)") or two separate toasts? Default: single toast.

### Follow-ups (out of scope)

- PR 5 — Diff view across revisions.
- PR 6 — Per-entry health history sparkline.
- PR 8 — `localStorage` draft autosave.
- PR 14 — `models-catalog` autocomplete as the only "add model" path.

### Risks explicitly accepted

- `MODELS_TAB = true` flag flip is irreversible without a code change. Mitigation: PR description clearly states the inline field is gone.
- Two-column layout may render awkwardly on narrow screens — collapsed to single column under 768px is acceptable.
- Opencode-go `seed-models` is best-effort: if it fails after a successful save, the operator uses the Quota tab's standalone Seed button.
