# PR 2 — Upstream Providers Tabbed Detail Page Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Split the Upstream Provider editor into 6 tabs (Overview / Models / Entries / Quota / Test / Logs) under `/upstream-providers/:id/:tab`, redirect old `/upstream-providers/:id` to `/overview`, extract form state into a React Context, conditionally filter tab visibility per provider type.

**Architecture:**
- **Routes:** Nested routes — `/upstream-providers/:id/:tab` with the existing `/upstream-providers/:id` redirecting to `/overview`. Unknown tab values fall back to `/overview`. Each tab is a sibling route so the URL is the source of truth.
- **State:** New `EditorStateProvider` (React Context) + `useEditorState()` hook. Lifts the current `ProviderEditorForm`'s ~15 `useState` calls verbatim. Validation pipeline (`form.js::validate`) lifted, not refactored.
- **Tabs:** Verbatim lifts of existing components (Overview = current schema-driven sections, Models = inline `models` field, Entries = `EntriesEditor.jsx`, Quota = `OpenCodeGoPanel.jsx`, Test = `TestPanel.jsx`, Logs = new). TabBar filters Quota/Test/Entries per provider type.

**Tech Stack:** React 18 + Vite + React Router 6 (dashboard only; no server changes). No new dependencies.

**Reference docs:**
- Design: `docs/plans/2026-09-19-upstream-providers-pr2-design.md`
- Master design: `docs/plans/2026-09-19-upstream-providers-ux-design.md` § PR 2
- PR 1 plan: `docs/plans/2026-09-19-upstream-providers-pr1-plan.md` (the patterns we follow)
- Existing helpers: `pages/upstream-provider-editor/{index.jsx, schemas.js, form.js, EntriesEditor.jsx, OpenCodeGoPanel.jsx, TestPanel.jsx, OAuthConnect.jsx}`

---

## Task 1: Create `useEditorState.js` (the context + hook)

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/useEditorState.js`

**Step 1: Read the current `index.jsx`**

Read `web/dashboard/src/pages/upstream-provider-editor/index.jsx` end-to-end (~719 lines). Identify:
- Every `useState` call (state shape + setters)
- Every `useMemo` (derived state)
- Every `useEffect` (beforeunload, OAuth create polling)
- Every event handler (`handleSave`, `handleFieldChange`, `handleAddEntry`, `handleRemoveEntry`, `handleModelsChange`, etc.)

**Step 2: Lift the state verbatim into the context**

Create `useEditorState.js` exporting:

```js
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { useToast } from '../../components/Toast.jsx';
import { useAsync } from '../../hooks/useAsync.js';
import {
  createUpstreamProvider, updateUpstreamProvider,
  listUpstreamProviderLiveStatus,
} from '../../api/client.js';
import { coerceLiveStatusResponse } from '../../api/liveStatus.js';
import { toForm, validate } from './form.js';
import { isEntryBearingType, TESTABLE_TYPES } from './schemas.js';

const EditorStateContext = createContext(null);

export function EditorStateProvider({ initial, children }) {
  const toast = useToast();
  // Lift every useState from the current index.jsx verbatim. Don't refactor.
  const [state, setState] = useState(() => toForm(initial));
  const [touched, setTouched] = useState({});
  const [saving, setSaving] = useState(false);
  const [savingError, setSavingError] = useState(null);
  // ... (lift all other state)

  const providerType = state.provider_type;
  const isEdit = Boolean(initial && initial.id);
  const isEntryBearing = isEntryBearingType(providerType);

  const siblingNames = useMemo(() => /* ...lifted from index.jsx... */, [state]);
  const errors = useMemo(
    () => validate(state, /* schema */, providerType, siblingNames, isEdit),
    [state, providerType, siblingNames, isEdit],
  );

  // Load live status (PR 1 pattern).
  const [liveStatus, setLiveStatus] = useState({});
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const json = await listUpstreamProviderLiveStatus();
        if (!cancelled) setLiveStatus(coerceLiveStatusResponse(json));
      } catch { /* degrade */ }
    })();
    return () => { cancelled = true; };
  }, []);

  // Handlers (verbatim lift + minor context-aware adjustments).
  const setField = useCallback((path, value) => { /* ... */ }, []);
  const setModels = useCallback((arr) => { /* ... */ }, []);
  const setEntries = useCallback((arr) => { /* ... */ }, []);
  const touch = useCallback((path) => setTouched((t) => ({ ...t, [path]: true })), []);
  const reset = useCallback(() => setState(toForm(initial)), [initial]);
  const save = useCallback(async () => { /* ... */ }, [state, isEdit, initial, toast]);

  // beforeunload when dirty.
  const dirty = /* compute */;
  useEffect(() => {
    if (!dirty || saving) return;
    const handler = (e) => { e.preventDefault(); e.returnValue = ''; };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [dirty, saving]);

  const value = useMemo(() => ({
    state, providerType, isEdit, isEntryBearing,
    errors, touched, dirty, saving, savingError,
    setField, setModels, setEntries, touch, save, reset,
    liveStatus,
  }), [/* deps */]);

  return <EditorStateContext.Provider value={value}>{children}</EditorStateContext.Provider>;
}

export function useEditorState() {
  const ctx = useContext(EditorStateContext);
  if (!ctx) throw new Error('useEditorState must be used inside EditorStateProvider');
  return ctx;
}
```

The exact list of state + handlers + effects to lift depends on what you find in `index.jsx`. The principle: **no refactor, no API change**. Whatever the current `ProviderEditorForm` does, the context does.

**Step 3: Add `isEntryBearingType` + `TESTABLE_TYPES` exports to `schemas.js`**

`schemas.js` already defines these conceptually (the section builder excludes entries for non-entry-bearing types). Add named exports:

```js
export const TESTABLE_TYPES = ['gemini-api-key', 'claude-api-key', 'openai-compatibility', 'codex-api-key', 'xai-api-key', 'vertex-api-key', 'interactions-api-key', 'opencode-go'];
export function isEntryBearingType(providerType) {
  // Same logic the existing schemas.js uses to decide whether to include the entries editor.
  // Read schemas.js to find the predicate and lift it.
}
```

Read `schemas.js` to find the existing predicate. Use the exact same logic.

**Step 4: Verify build**

Run: `cd web/dashboard && npm run build && cd -`
Expected: build passes. The new file isn't yet imported anywhere.

**Step 5: Commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/useEditorState.js web/dashboard/src/pages/upstream-provider-editor/schemas.js
git commit -m "refactor(dashboard): extract editor state into useEditorState context"
```

---

## Task 2: Create the route shell + TabBar

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/TabBar.jsx`
- Modify: `web/dashboard/src/pages/upstream-provider-editor/index.jsx` (becomes thin shell)
- Modify: `web/dashboard/src/App.jsx` (register nested routes + redirect)

**Step 1: Create `TabBar.jsx`**

```jsx
import React from 'react';
import { NavLink, useParams } from 'react-router-dom';

const ALL_TABS = [
  { key: 'overview', label: 'Overview', alwaysShown: true },
  { key: 'models', label: 'Models', alwaysShown: true },
  { key: 'entries', label: 'Entries', condition: 'isEntryBearing' },
  { key: 'quota', label: 'Quota', condition: 'isOpencodeGo' },
  { key: 'test', label: 'Test', condition: 'isTestable' },
  { key: 'logs', label: 'Logs', alwaysShown: true },
];

// TabBar renders a filtered tab strip. Hidden tabs are filtered out
// based on the provider type. Active tab = URL :tab segment.
export function TabBar({ providerType, isEntryBearing }) {
  const { tab } = useParams();
  const isOpencodeGo = providerType === 'opencode-go';
  const isTestable = !providerType?.startsWith('oauth:') && TESTABLE_TYPES.includes(providerType);

  const visibleTabs = ALL_TABS.filter((t) => {
    if (t.alwaysShown) return true;
    if (t.condition === 'isEntryBearing') return isEntryBearing;
    if (t.condition === 'isOpencodeGo') return isOpencodeGo;
    if (t.condition === 'isTestable') return isTestable;
    return true;
  });

  return (
    <nav aria-label="Provider sections" className="upstream-editor__tabs">
      {visibleTabs.map((t) => (
        <NavLink
          key={t.key}
          to={`../${t.key}`}
          relative="path"
          className={({ isActive }) => `upstream-editor__tab ${isActive ? 'upstream-editor__tab--active' : ''}`}
          aria-current={tab === t.key ? 'page' : undefined}
        >
          {t.label}
        </NavLink>
      ))}
    </nav>
  );
}

export default TabBar;
```

**Step 2: Refactor `index.jsx` into the route shell**

Replace the entire `ProviderEditorForm` body with a thin shell:

```jsx
import React from 'react';
import { Outlet, useNavigate, useParams } from 'react-router-dom';
import { useAsync } from '../../hooks/useAsync.js';
import { getUpstreamProvider } from '../../api/client.js';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';
import { EditorStateProvider, useEditorState } from './useEditorState.js';
import { TabBar } from './TabBar.jsx';

export function UpstreamProviderEditorPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const providerAsync = useAsync(() => getUpstreamProvider(id), [id]);

  if (providerAsync.loading) return <div className="main"><Spinner /></div>;
  if (providerAsync.error) {
    return (
      <div className="main">
        <ErrorBanner error={providerAsync.error} onRetry={providerAsync.reload} />
        <button onClick={() => navigate('/upstream-providers')}>Back to list</button>
      </div>
    );
  }

  return (
    <EditorStateProvider initial={providerAsync.data}>
      <EditorShell />
    </EditorStateProvider>
  );
}

function EditorShell() {
  const navigate = useNavigate();
  const { state, providerType, isEntryBearing, dirty, saving, savingError, save } = useEditorState();

  return (
    <div className="main">
      <header className="upstream-editor__header">
        <button onClick={() => navigate('/upstream-providers')} className="upstream-editor__back">← Back</button>
        <div className="upstream-editor__header-main">
          <h1 className="upstream-editor__title">{state.name || state.label || state.email || state.file_name || 'Untitled'}</h1>
          <span className="badge">{providerType}</span>
          {dirty && <span className="upstream-editor__dirty" aria-live="polite">● unsaved changes</span>}
          {savingError && <span className="error-pill" role="alert">{savingError}</span>}
        </div>
        <button onClick={save} disabled={saving} className="btn btn-primary">
          {saving ? 'Saving…' : 'Save'}
        </button>
      </header>
      <TabBar providerType={providerType} isEntryBearing={isEntryBearing} />
      <Outlet />
    </div>
  );
}

export default UpstreamProviderEditorPage;
```

**Step 3: Register the routes in `App.jsx`**

Add (or replace the existing `/upstream-providers/:id` route):

```jsx
<Route path="/upstream-providers/:id" element={<Navigate to="/upstream-providers/:id/overview" replace />} />
<Route path="/upstream-providers/:id/:tab" element={<UpstreamProviderEditorPage />}>
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

Place this BEFORE the `/upstream-providers/:id/overview` etc. routes (React Router 6 matches in order — the redirect route comes first to handle the no-tab case).

The current `/upstream-providers/:id` route is at App.jsx around line 297. Find it and replace.

**Step 4: Verify build**

Run: `cd web/dashboard && npm run build && cd -`
Expected: build fails (OverviewTab/ModelsTab/etc. don't exist yet — they're Tasks 3-7). **That's fine.** The build will succeed once those tasks land.

For this task, verify the `useEditorState.js` and `TabBar.jsx` files at least compile in isolation:
- `cd web/dashboard && node --check src/pages/upstream-provider-editor/useEditorState.js && cd -` (won't parse JSX — use `npm run build` instead and tolerate the expected failures).

Actually: since the route registration in `App.jsx` references non-existent tab components, the build will fail. To avoid breaking `npm run build` for the whole project between Task 2 and Task 7, **register the tab routes incrementally**: in Task 2, register only the redirect + the parent route. The child routes get added in Tasks 3-7 as each tab component ships.

For Task 2, modify `App.jsx` to register ONLY:

```jsx
<Route path="/upstream-providers/:id" element={<Navigate to="/upstream-providers/:id/overview" replace />} />
<Route path="/upstream-providers/:id/:tab" element={<UpstreamProviderEditorPage />} />
```

(The parent route renders `<Outlet />`; an empty `<Outlet />` is fine.)

**Step 5: Commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/useEditorState.js web/dashboard/src/pages/upstream-provider-editor/schemas.js web/dashboard/src/pages/upstream-provider-editor/TabBar.jsx web/dashboard/src/pages/upstream-provider-editor/index.jsx web/dashboard/src/App.jsx
git commit -m "feat(dashboard): editor route shell + useEditorState + TabBar"
```

---

## Task 3: Create `OverviewTab.jsx`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/OverviewTab.jsx`

**Step 1: Lift the schema-driven section loop from `index.jsx`**

Read `web/dashboard/src/pages/upstream-provider-editor/index.jsx` and find the `ProviderEditorForm` function. Inside it, find the loop that renders `sections.map((section) => ...)`. That entire block (rendering Identity / Endpoint / Routing / Behavior / optional Cloak) lifts verbatim into `OverviewTab.jsx`, with these surgical changes:

- Replace local `form` (the current `useState` object) with `useEditorState().state`.
- Replace local `handleFieldChange(name, value)` with `useEditorState().setField(name, value)`.
- Replace local `handleAddEntry` / `handleRemoveEntry` calls with `useEditorState().setEntries(...)` (only used in the entries section of Overview; if present).
- Keep all `<Field>` / `<PasswordInput>` / `<ToggleRow>` / `<ChipListEditor>` / `<KeyValueEditor>` / `<ModelListEditor>` rendering exactly as-is.
- Keep the editor summary banner (type/identifier/dirty marker/hint) — it stays in the sticky header now, not in OverviewTab.

The `modelsTab` flag (from the design doc) defaults to `false` in PR 2. When `modelsTab === true`, the inline `models` field disappears from Overview (Models tab owns it). For now, the `models` section still renders in Overview — PR 3 will set the flag.

**Step 2: Verify build**

`cd web/dashboard && npm run build && cd -`
Expected: may still fail if ModelsTab/EntriesTab/etc. aren't imported anywhere yet. Tolerate. The important thing is that `OverviewTab.jsx` itself is syntactically valid — `node --check` won't work for JSX, but `npm run build` will give a clear syntax error if there's a problem.

**Step 3: Register OverviewTab in `App.jsx`**

Inside the `/upstream-providers/:id/:tab` parent route, add:
```jsx
<Route path="overview" element={<OverviewTab />} />
```
Plus the import + the `<Route index element={<Navigate to="overview" replace />} />` child.

**Step 4: Commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/OverviewTab.jsx web/dashboard/src/App.jsx
git commit -m "feat(dashboard): OverviewTab — schema-driven form sections"
```

---

## Task 4: Create `ModelsTab.jsx`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/ModelsTab.jsx`

**Step 1: Lift the inline `models` field rendering**

The current `index.jsx` renders the `models` field via the schema section loop. Find that block — it lives in the `models` field rendering (driven by `schemas.js`'s `models` field type). Lift it into `ModelsTab.jsx`:

```jsx
import React from 'react';
import { useEditorState } from './useEditorState.js';
import { ModelListEditor } from '../manage-cpa/FormPrimitives.jsx';
import { FetchModelsInline } from '../manage-cpa/FetchModelsInline.jsx';

export function ModelsTab() {
  const { state, setModels, availableModels } = useEditorState();
  return (
    <section>
      <ModelListEditor rows={state.models || []} onChange={setModels} />
      <FetchModelsInline form={state} onAddModels={setModels} availableModels={availableModels} />
    </section>
  );
}

export default ModelsTab;
```

The exact props of `ModelListEditor` and `FetchModelsInline` depend on what they accept today — read both files briefly to match.

For opencode-go providers, the `OpenCodeGoModelListEditor` is currently inline in `index.jsx`. Lift that too (read `index.jsx` for the `OpenCodeGoModelListEditor` definition, copy verbatim, use `providerType === 'opencode-go'` to switch).

**Step 2: Register ModelsTab in `App.jsx`**

```jsx
<Route path="models" element={<ModelsTab />} />
```

**Step 3: Build + commit**

`cd web/dashboard && npm run build && cd -`
Expected: build passes (or fails only on tasks not yet done).

```bash
git add web/dashboard/src/pages/upstream-provider-editor/ModelsTab.jsx web/dashboard/src/App.jsx
git commit -m "feat(dashboard): ModelsTab — verbatim lift"
```

---

## Task 5: Create `EntriesTab.jsx`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/EntriesTab.jsx`

**Step 1: Lift `EntriesEditor.jsx` verbatim**

`EntriesEditor.jsx` (current `pages/upstream-provider-editor/EntriesEditor.jsx`) is a near-complete entries editor. Lift it verbatim into `EntriesTab.jsx`, then update its props to consume from `useEditorState()`:

```jsx
import React from 'react';
import { useEditorState } from './useEditorState.js';
import { StatusDot, statusFromRow } from './components/StatusDot.jsx';

export function EntriesTab() {
  const { state, setEntries, liveStatus } = useEditorState();
  return (
    <EntriesEditor
      entries={state.api_key_entries || []}
      onChange={setEntries}
      liveStatus={liveStatus}
    />
  );
}
```

Then in the same file, define `EntriesEditor` (lifted verbatim from `EntriesEditor.jsx`) with one addition: a `<StatusDot>` column per entry, using `statusFromRow({ isLive, cooldownUntil, breakerOpen })` keyed by `liveStatus[entry.id]`.

If `liveStatus` doesn't have an entry for `entry.id`, render "—".

**Step 2: Register in `App.jsx`**

```jsx
<Route path="entries" element={<EntriesTab />} />
```

**Step 3: Build + commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/EntriesTab.jsx web/dashboard/src/App.jsx
git commit -m "feat(dashboard): EntriesTab with per-entry StatusDot"
```

---

## Task 6: Create `QuotaTab.jsx`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/QuotaTab.jsx`

**Step 1: Lift `OpenCodeGoPanel.jsx` verbatim**

`OpenCodeGoPanel.jsx` is a near-complete opencode-go quota/seed/refresh panel. Lift it verbatim into `QuotaTab.jsx`. Update its props to take `providerId` from the route param:

```jsx
import React from 'react';
import { useParams } from 'react-router-dom';
import { OpenCodeGoPanel } from './OpenCodeGoPanel.jsx';

export function QuotaTab() {
  const { id } = useParams();
  return <OpenCodeGoPanel providerId={id} />;
}

export default QuotaTab;
```

Read `OpenCodeGoPanel.jsx` briefly to see what props it currently accepts and adjust.

**Step 2: Register in `App.jsx`**

```jsx
<Route path="quota" element={<QuotaTab />} />
```

**Step 3: Build + commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/QuotaTab.jsx web/dashboard/src/App.jsx
git commit -m "feat(dashboard): QuotaTab — opencode-go panel"
```

---

## Task 7: Create `TestTab.jsx`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/TestTab.jsx`

**Step 1: Lift `TestPanel.jsx` verbatim**

Same pattern as QuotaTab. Lift `TestPanel.jsx` verbatim into `TestTab.jsx`. Pass `providerId` from `useParams()`. Add live status display alongside the entry dropdown if not already present.

**Step 2: Register in `App.jsx`**

```jsx
<Route path="test" element={<TestTab />} />
```

**Step 3: Build + commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/TestTab.jsx web/dashboard/src/App.jsx
git commit -m "feat(dashboard): TestTab — API-key test panel"
```

---

## Task 8: Create `LogsTab.jsx` (new)

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/LogsTab.jsx`

**Step 1: Build the three sub-sections**

```jsx
import React, { useState } from 'react';
import { useParams } from 'react-router-dom';
import { useAsync } from '../../hooks/useAsync.js';
import { listUpstreamSyncLog, listUsageEvents, listModelHealthLog } from '../../api/client.js';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';

export function LogsTab() {
  const { id } = useParams();
  return (
    <div className="form-section">
      <h2 className="form-section__title">Logs</h2>
      <SyncEventsSection providerId={id} />
      <RecentEventsSection providerId={id} />
      <ModelHealthSection providerId={id} />
    </div>
  );
}

function SyncEventsSection({ providerId }) {
  const [expanded, setExpanded] = useState(true);
  const async = useAsync(() => listUpstreamSyncLog({ provider: providerId, limit: 50 }), [providerId]);
  return (
    <Collapsible title="Sync events" expanded={expanded} onToggle={() => setExpanded((e) => !e)} onRefresh={async.reload} loading={async.loading} error={async.error}>
      <EventsTable rows={async.data?.events || []} emptyHint="No sync events for this provider." />
    </Collapsible>
  );
}

function RecentEventsSection({ providerId }) {
  // listUsageEvents — check client.js for the right call signature.
  // ...
}

function ModelHealthSection({ providerId }) {
  // listModelHealthLog — model filter, not provider. Client-side filter the results by provider.
  // ...
}
```

The exact signatures of `listUpstreamSyncLog`, `listUsageEvents`, and `listModelHealthLog` depend on what `client.js` exposes — read briefly to match.

**Step 2: Register in `App.jsx`**

```jsx
<Route path="logs" element={<LogsTab />} />
```

Plus the catch-all redirect inside the parent route:
```jsx
<Route path="*" element={<Navigate to="overview" replace />} />
```

**Step 3: Build + commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/LogsTab.jsx web/dashboard/src/App.jsx
git commit -m "feat(dashboard): LogsTab with sync events, recent events, model health"
```

---

## Task 9: Add `modelsTab` flag to OverviewTab

**Files:**
- Modify: `web/dashboard/src/pages/upstream-provider-editor/OverviewTab.jsx`

**Step 1: Wrap the `models` section rendering with a flag**

Read `OverviewTab.jsx`. Find the section that renders the `models` field. Wrap it:

```jsx
{!modelsTab && (
  <div className="form-section">{/* the existing models rendering */}</div>
)}
```

Where `modelsTab` comes from a local constant `const modelsTab = false;` at the top of the component. This makes the change a one-line toggle in PR 3.

**Step 2: Build + commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/OverviewTab.jsx
git commit -m "feat(dashboard): modelsTab flag in OverviewTab (default false)"
```

---

## Task 10: Tests for TabBar

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/TabBar.test.jsx`

**Step 1: Write failing tests**

```jsx
import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { TabBar } from './TabBar.jsx';

test('TabBar: shows all 6 tabs for opencode-go', () => {
  const html = renderToStaticMarkup(
    React.createElement(MemoryRouter, { initialEntries: ['/1/overview'] },
      React.createElement(Routes, null,
        React.createElement(Route, { path: '/:id/:tab', element: React.createElement(TabBar, { providerType: 'opencode-go', isEntryBearing: true }) })
      )
    )
  );
  assert.match(html, /Overview/);
  assert.match(html, /Models/);
  assert.match(html, /Entries/);
  assert.match(html, /Quota/);
  assert.match(html, /Test/);
  assert.match(html, /Logs/);
});

test('TabBar: hides Quota for non-opencode-go API-key types', () => {
  const html = renderToStaticMarkup(
    React.createElement(MemoryRouter, { initialEntries: ['/1/overview'] },
      React.createElement(Routes, null,
        React.createElement(Route, { path: '/:id/:tab', element: React.createElement(TabBar, { providerType: 'claude-api-key', isEntryBearing: true }) })
      )
    )
  );
  assert.doesNotMatch(html, />Quota</);
  assert.match(html, /Test/);
});

test('TabBar: hides Entries + Quota + Test for OAuth providers', () => {
  const html = renderToStaticMarkup(
    React.createElement(MemoryRouter, { initialEntries: ['/1/overview'] },
      React.createElement(Routes, null,
        React.createElement(Route, { path: '/:id/:tab', element: React.createElement(TabBar, { providerType: 'oauth:claude', isEntryBearing: false }) })
      )
    )
  );
  assert.match(html, /Overview/);
  assert.match(html, /Models/);
  assert.match(html, /Logs/);
  assert.doesNotMatch(html, />Entries</);
  assert.doesNotMatch(html, />Quota</);
  assert.doesNotMatch(html, />Test</);
});
```

**Step 2: Run via the runner directly**

`cd web/dashboard && node --import ./scripts/register-jsx.mjs --test src/pages/upstream-provider-editor/TabBar.test.jsx && cd -`

**Step 3: Commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/TabBar.test.jsx
git commit -m "test(dashboard): TabBar filtering per provider type"
```

---

## Task 11: Tests for `useEditorState`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/useEditorState.test.jsx`

**Step 1: Write failing tests**

```jsx
import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToString } from 'react-dom/server';
import { EditorStateProvider, useEditorState } from './useEditorState.js';

const sampleProvider = {
  id: 1,
  provider_type: 'claude-api-key',
  name: 'test',
  api_key: 'sk-test',
  base_url: 'https://api.example.com',
  models: [],
  api_key_entries: [],
};

test('useEditorState: provides state from initial provider', () => {
  let captured = null;
  function Probe() { captured = useEditorState(); return null; }
  renderToString(
    React.createElement(EditorStateProvider, { initial: sampleProvider },
      React.createElement(Probe, null)
    )
  );
  assert.equal(captured.providerType, 'claude-api-key');
  assert.equal(captured.isEdit, true);
  assert.equal(captured.isEntryBearing, true);
  assert.ok(captured.state);
});

test('useEditorState: setField mutates state', () => {
  let captured = null;
  function Probe() { captured = useEditorState(); return null; }
  renderToString(
    React.createElement(EditorStateProvider, { initial: sampleProvider },
      React.createElement(Probe, null)
    )
  );
  captured.setField('base_url', 'https://new.example.com');
  // Re-render to flush state.
  // ...
});

test('useEditorState: errors populated by validate()', () => {
  // ...
});
```

The exact assertions depend on what `useEditorState` exposes. Adapt to the real interface.

**Step 2: Run + commit**

```bash
git add web/dashboard/src/pages/upstream-provider-editor/useEditorState.test.jsx
git commit -m "test(dashboard): useEditorState provides state + mutations"
```

---

## Task 12: Manual smoke checklist

Run through the design doc § 5 "How to test" checklist:

1. Open Claude API key provider → 6 tabs visible, Quota hidden.
2. Open opencode-go provider → Quota tab visible.
3. Open OAuth provider → Entries + Test + Quota hidden.
4. Open `/upstream-providers/123/foo` → redirects to `/overview`.
5. Edit a field on Overview, switch to Models → field state preserved, dirty marker on header.
6. Click Save → success toast + stay on current tab.
7. Cmd+W on a dirty tab → browser warns before close.

---

## Task 13: Final build + gofmt + test pass

```bash
cd web/dashboard && npm run build && npm test
gofmt -w .
go build -o /tmp/nixllm-build ./cmd/server && rm /tmp/nixllm-build
go test ./...
```

All green.

---

## Task 14: Commit, push, merge

```bash
git push -u origin feat/upstream-providers-pr2
gh pr create --title "..." --body "..."
```

(Or merge locally per user choice.)

---

## Out of scope for PR 2

- Models tab UX overhaul (PR 3).
- Diff view across revisions (design doc PR 5).
- `localStorage` draft autosave (design doc PR 8).
- Cmd/Ctrl+S binding.
- Per-field unsaved warnings.
