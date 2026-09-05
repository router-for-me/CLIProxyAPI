# Upstream Provider Editor Page Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Move the upstream-provider add/edit modal to a dedicated routed page (`/upstream-providers/new`, `/upstream-providers/:id`), split into a focused editor module, and delete the old modal.

**Architecture:** The editor logic leaves `UpstreamProvidersPage.jsx` (3,726 lines) for a new `pages/upstream-provider-editor/` module (schemas.js, form.js, index.jsx, OAuthConnect.jsx). The list page keeps fetching/filtering/bulk actions and navigates instead of opening a modal. Field engine + sub-editors already live in `pages/manage-cpa/FormPrimitives.jsx` — the new module imports them (no move needed).

**Tech Stack:** React 19 + Vite SPA, react-router-dom, node:test runner (`web/dashboard`).

**Worktree:** `.worktrees/provider-editor-page`, branch `feat/provider-editor-page`. Commands run from that directory unless noted.

**Design doc:** `docs/plans/2026-09-05-upstream-provider-editor-page-design.md`

**Baseline:** `npm test` 156 pass / 0 fail; `go test ./...` — 3 pre-existing executor failures + `TestMountDashboardRoutes` (fails only because the worktree lacks a built `dist/`; fixed by `make dash-embed` at the end). NO Go code is touched by this feature.

**What stays on the list page (do not move):** `Stat`, `PaginationBar`, `BulkActionBar`, `BulkDeleteConfirmModal`, `GlobalOAuthModelAliasCard`, `ChannelAliasEditor`, `normalizeAliasMap`, `serializeAliasRows`, `ImportOAuthProviderModal` + its helpers (`AUTH_TYPE_TO_OAUTH_CHANNEL`, `isServiceAccountJson`, `extractAuthFields`, `buildOAuthCreatePayload`, `deriveFileName`, `PreviewCard`), Delete confirm modal, `formatRelativeTime`/`formatTime`-style list helpers.

**What moves to the editor module:** `buildSchemas` (+ its constant deps `API_KEY_TYPES`, `OAUTH_TYPES`, `URL_RE`, `TYPE_LABEL`, `ROUTING_STRATEGY_OPTIONS`, `ROUTING_STRATEGY_HINT`, `MAX_ENTRY_WEIGHT`, `identifierField`, `commonEndpoint`, `commonRouting`, `commonBehavior`, `claudeCloakSection`, `isOAuth`, `isOpenAI`, `isClaude`), `UpstreamProviderEditor` (becomes the page body), `renderInput`, `OAuthConnectSection`, `APIKeyEntriesEditor`, `validateAPIKeyEntries`, `idHintForIdentity`, `hydrateEntries`, `buildForm`, `buildPayload`, `validate`, `capitalize`, `formatRFC3339`, `strVal` (if used by moved code only — verify), `PasswordInput`/`Field`/`ToggleRow`/`ChipListEditor`/`KeyValueEditor`/`ModelListEditor` usage (import from FormPrimitives).

**CRITICAL continuity rules:** every function moves VERBATIM (cut-paste, no behavior edits); exports that tests import stay exported from the module; the 156-test suite must stay green after every task; only one task at a time.

---

### Task 1: Create `upstream-provider-editor/schemas.js`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/schemas.js`
- Test: `web/dashboard/src/pages/UpstreamProvidersPage.entries.test.js` (imports updated IN THIS TASK)

**Steps:**

1. Read `UpstreamProvidersPage.jsx` and collect every symbol `buildSchemas` and the schemas depend on: `API_KEY_TYPES`, `OAUTH_TYPES`, `URL_RE`, `TYPE_LABEL`, `ROUTING_STRATEGY_OPTIONS`, `ROUTING_STRATEGY_HINT`, `MAX_ENTRY_WEIGHT`, `isOAuth`, `isOpenAI`, `isClaude`, `capitalize` (if used by schema hints/labels), `fetchModels`-related schema flags, `FetchModelsInline` usage notes, `commonEndpoint`/`commonRouting`/`commonBehavior`/`claudeCloakSection`/`identifierField` builders. Verify with `grep -n` inside the `buildSchemas` function body (lines ~1285-1497) plus the constants region (lines ~34-90).

2. Create `schemas.js` with: the constants block (verbatim), the helper functions (`isOAuth`, `isOpenAI`, `isClaude`, `capitalize` ONLY IF schemas use it — check; if the list page also uses it, export it from schemas.js and let the list page import), and `export function buildSchemas()` (verbatim body). Copy the imports it needs (e.g. `FetchModelsInline` is used by the page's section renderer, NOT by schemas — verify; if schemas reference nothing from FormPrimitives, no imports needed). Keep `export` on `buildSchemas` (tests import it).

3. Delete those symbols from `UpstreamProvidersPage.jsx` and add `import { buildSchemas, API_KEY_TYPES, OAUTH_TYPES, TYPE_LABEL, isOAuth, isOpenAI, isClaude } from './upstream-provider-editor/schemas.js';` (only the names the list page still uses — verify each with grep; e.g. the list page uses TYPE_LABEL for delete-confirm text and type filters; `URL_RE` stays with schemas unless the list uses it — check).

4. Update the test file imports: every `import ... from './UpstreamProvidersPage.jsx'` line that names `buildSchemas` splits — `buildSchemas` now comes from `./upstream-provider-editor/schemas.js`.

5. Verify: `cd web/dashboard && npm test 2>&1 | grep -E "^# (tests|pass|fail)"` → 156/156/0. `npm run build` green.

6. Commit: `git add -A web/dashboard/src && git commit -m "refactor(dashboard): extract editor schemas into upstream-provider-editor/schemas.js"`

### Task 2: Create `upstream-provider-editor/form.js`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/form.js`
- Modify: `web/dashboard/src/pages/UpstreamProvidersPage.jsx`, `UpstreamProvidersPage.entries.test.js`

**Steps:**

1. Collect the form/data functions: `validateAPIKeyEntries` (exported), `idHintForIdentity`, `hydrateEntries`, `buildForm` (exported), `buildPayload` (exported), `validate` (exported), `formatRFC3339` (used by buildForm), `strVal` + `capitalize` (verify who uses each — `strVal` is used by buildPayload's cloak handling; `capitalize` may be used by the list page too: grep. If shared, keep a copy in form.js and one in the list, or export from form.js and import in the list — prefer one source in form.js, list imports it).

2. Create `form.js` with those functions verbatim, each with its leading comment block. Imports it needs: `MAX_ENTRY_WEIGHT` from `./schemas.js`; `isOAuth/isOpenAI/isClaude` from `./schemas.js` (verify which are used). Keep the exact export list: `validateAPIKeyEntries`, `buildForm`, `buildPayload`, `validate` (+ whatever the list page still uses).

3. Delete them from `UpstreamProvidersPage.jsx`; the list page imports what it still needs (likely only `validate` — no: `validate` is editor-only; the list uses none of these — verify with grep and import nothing if unused). NOTE: `UpstreamProviderEditor` (still in the list file at this point) calls `buildForm`/`buildPayload`/`validate` — so the list file imports them from `./upstream-provider-editor/form.js` for now (they will leave again in Task 4).

4. Update test imports for `buildForm`, `buildPayload`, `validateAPIKeyEntries` → `./upstream-provider-editor/form.js`.

5. Verify: `npm test` → 156/156/0; `npm run build` green.

6. Commit: `refactor(dashboard): extract form builders into upstream-provider-editor/form.js`

### Task 3: Create `upstream-provider-editor/OAuthConnect.jsx` + `entries.jsx`

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/OAuthConnect.jsx`
- Create: `web/dashboard/src/pages/upstream-provider-editor/entries.jsx`
- Modify: `UpstreamProvidersPage.jsx`

**Steps:**

1. `OAuthConnect.jsx` gets `OAuthConnectSection` verbatim (lines ~1862-2130) with its imports (`useState`, toast, API client functions — check its body for `fetchJSON`/client imports and add them). It does NOT take the Import modal helpers (those stay). Export default the component.
   NOTE: `OAuthConnectSection` is used ONLY by `UpstreamProviderEditor` — but that editor is still in the list file. Import it there for now.

2. `entries.jsx` gets `APIKeyEntriesEditor` verbatim (lines ~3160-3282) + its `PasswordInput` import from `../manage-cpa/FormPrimitives.jsx` (note the relative path gains a `..` level) + `validateAPIKeyEntries` import from `./form.js` + `idHintForIdentity` (move it here — it's entries-only; re-export not needed unless tests import it — they don't, verify). Export default `APIKeyEntriesEditor`.

3. Delete both from the list file; the still-present `UpstreamProviderEditor` imports them from the new module.

4. Verify: `npm test` 156/156/0; `npm run build` green. `npx vite build` if the build script differs — use `npm run build`.

5. Commit: `refactor(dashboard): extract OAuthConnect and entries editor components`

### Task 4: Create `upstream-provider-editor/index.jsx` (the routed page) and delete the modal

**Files:**
- Create: `web/dashboard/src/pages/upstream-provider-editor/index.jsx`
- Modify: `UpstreamProvidersPage.jsx` (delete editor + modal + editing state; navigate instead)
- Modify: `web/dashboard/src/App.jsx` (routes)
- Test: `web/dashboard/src/pages/upstream-provider-editor/index.test.js` (new)

**Steps:**

1. **index.jsx** — `export default function UpstreamProviderEditorPage()`:
   - `const { id } = useParams();` → `const isCreate = id === 'new'`.
   - Create mode: `providerType` state starts `''`; render a type-picker grid (two groups: API Key types + OAuth types, cards with label + description, reusing `API_KEY_TYPES`/`OAUTH_TYPES` from schemas.js — same data the modal's picker used; check the modal's picker JSX at its create branch and adapt the layout to a `.grid grid--3` page section). After pick → `setProviderType(v)`, `form` state = `buildForm(providerType, null)`. Type switchable via a header select (carry-over: pass current form as `carryOver` like the modal does — copy the modal's carry logic).
   - Edit mode: `useAsync` or `useEffect` fetch via `getUpstreamProvider(id)`; `loading` → Spinner; `error` → ErrorBanner + Back button; success → `buildForm(provider.provider_type, provider)`. Keep a `provider` state for `isEdit`, `id`, and `siblingNames` — siblingNames comes from `listUpstreamProviders()` (fetch the list once on mount in edit mode, same `isOpenAI(...).map(p => p.name).filter(Boolean)` filter).
   - Body: the ENTIRE former `UpstreamProviderEditor` JSX (lines ~1504-1797) verbatim as an inner component or inlined, minus the `<Modal>` wrapper — replace with the page layout: sticky header (`← Back` = `navigate('/upstream-providers')`, title = `isCreate ? 'New Provider' : (provider.name || provider.label || provider.file_name || 'Edit Provider')`, type badge from TYPE_LABEL, Save button) + `<div className="upstream-editor">` section stack. Add minimal CSS in `web/dashboard/src/styles/global.css` ONLY for: sticky header, `.upstream-editor` max-width 900px margin auto, type-picker grid. Match existing class conventions (check global.css naming before inventing).
   - Save: identical logic to the modal's save (`buildPayload` → create/update via API client → toast → `navigate('/upstream-providers')`). Keep the OAuth connect gating exactly (save blocked until connect completes for auth-url types — copy verbatim).
   - Dirty guard: `useEffect` tracking a `dirty` flag (form !== initial hydrated/carry state — set `true` on any field update via the existing `update` function wrapper, `false` after save); `useBeforeUnload`-style via react-router's `useBlocker` IF available in the installed react-router version (check package.json); else fallback: window `beforeunload` + `onClick` guard on the Back button with `window.confirm`. Keep it to the Back button + browser unload; not every internal link (YAGNI per design).
2. **Delete from UpstreamProvidersPage.jsx:** the `editing` state + `{editing && <UpstreamProviderEditor .../>}` block + `UpstreamProviderEditor` function + `renderInput` (moved into index.jsx) + its remaining editor-only imports. Replace `setEditing(p)` with `navigate('/upstream-providers/' + p.id)` and `setEditing({})` with `navigate('/upstream-providers/new')` (2 occurrences each — lines ~406, ~500, ~536, ~583). Remove `useNavigate` import if absent.
3. **App.jsx:** add the two routes right under the existing `/upstream-providers` route:
   ```jsx
   <Route path="/upstream-providers/new" element={<UpstreamProviderEditorPage />} />
   <Route path="/upstream-providers/:id" element={<UpstreamProviderEditorPage />} />
   ```
   Import `UpstreamProviderEditorPage` from `./pages/upstream-provider-editor/index.jsx`.
4. **Test** `index.test.js` (node:test + register-jsx, following an existing component-test file's harness if any exists — grep for a test that renders JSX; if none renders components, test only pure logic): mode detection (`new` vs numeric id), and — if component rendering isn't established in the repo's test setup — skip component tests and note manual QA in the commit.
5. Verify: `npm test` 156/156/0 (plus any new tests); `npm run build` green. Manual smoke via `npm run dev` if quick: navigate list → edit → back; create flow picks type.
6. Commit: `feat(dashboard): dedicated upstream provider editor page replaces the modal`

### Task 5: Regression + dash-embed + docs

**Steps:**

1. `cd web/dashboard && npm test` → all green (156+). `npm run build` green.
2. `make dash-embed` then `go test ./internal/api/ -run TestMountDashboardRoutes -v` → PASS.
3. `go test ./...` → exactly the 3 pre-existing executor failures, nothing else.
4. Manual QA checklist (if the dev server can run headlessly, do a curl-level check of the built SPA only — the real QA is operator's): list renders without modal code (`grep -c "UpstreamProviderEditor" UpstreamProvidersPage.jsx` → 0), routes exist in the bundle.
5. Update the design doc status line to "Status: Implemented".
6. Commit: `git add -A && git commit -m "chore: embed dashboard + mark editor page design implemented"`

---

## Execution notes

- **Move verbatim.** Any behavior tweak discovered mid-move (a bug you can't help fixing) becomes its own commit with a test, never smuggled into a move commit.
- **Imports are the hard part.** After each task, grep the list page for now-undefined symbols (`npx vite build` catches them; also `node --test` does not — build is the authority for JSX/import errors).
- **Do not touch Go.** This feature is SPA-only.
- **The test file splits gradually** (Tasks 1-2) — keep it green after each task.
