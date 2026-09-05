# Design: Dedicated Upstream Provider Editor Page

**Date:** 2026-09-05
**Status:** Implemented (feat/provider-editor-page; implementation plan at 2026-09-05-upstream-provider-editor-page-plan.md)
**Goal:** Replace the upstream-provider add/edit modal with a dedicated full-page editor routed at `/upstream-providers/new` and `/upstream-providers/:id`, split out of the 3,700-line UpstreamProvidersPage into a focused editor module.

## Decisions (from the brainstorming session)

1. **Scope:** ALL provider types (API-key + OAuth) move to the page — one edit UX, no dual patterns side by side.
2. **Routing:** dedicated routes following the existing detail-page pattern (`api-keys/:id`, `auto-routers/:id`): `/upstream-providers/new` for create, `/upstream-providers/:id` for edit. Deep-linkable, refresh-safe, back-button works.
3. **Old modal:** deleted entirely once the page covers all behavior — one edit path, no dead code, no drift risk.
4. **File structure:** the editor is split into a focused module rather than moved as one giant file.

## Architecture & File Structure

```
web/dashboard/src/pages/UpstreamProvidersPage.jsx   (~1400 lines, list only)
web/dashboard/src/pages/upstream-provider-editor/
  index.jsx        → route page: reads param (new|:id), fetch-or-create, renders layout
  schemas.js       → buildSchemas() + constants (OAUTH_TYPES, API_KEY_TYPES, URL_RE, ROUTING_STRATEGY_OPTIONS, hints)
  form.js          → buildForm/buildPayload/validateAPIKeyEntries/hydrateEntries/idHintForIdentity (pure functions)
  OAuthConnect.jsx → OAuth connect flow (auth URL → callback paste) + its helpers
  EntriesEditor.jsx → APIKeyEntriesEditor (the field engine + list sub-editors stay in manage-cpa/FormPrimitives.jsx)
```

`UpstreamProvidersPage.jsx` keeps: fetch/reload, filters, bulk actions, Delete + BulkDelete confirm modals, sync status, per-row Edit button → `navigate(/upstream-providers/:id)`, `+ New Provider` → `navigate('/upstream-providers/new')`.

**What stays a modal (unchanged):** Delete confirm, BulkDeleteConfirm, and ImportOAuthProviderModal — that is a quick import flow from the list, not the editor.

**Behavior preserved exactly:** schema-driven form, per-type conditional fields, inline validation, OAuth connect flow (for `oauth:*` types with an auth-url endpoint), toast feedback, carry-over form state when switching type in create mode.

## Routing & Data Flow

```jsx
<Route path="/upstream-providers" element={<UpstreamProvidersPage />} />
<Route path="/upstream-providers/new" element={<UpstreamProviderEditorPage />} />
<Route path="/upstream-providers/:id" element={<UpstreamProviderEditorPage />} />
```

One component handles both modes — `useParams().id === 'new'` cannot collide with a numeric PG id.

- **Create (`/new`):** `providerType` starts empty → type-picker screen (grid of type cards reusing the existing type lists). After picking, `buildForm(providerType, null)`. The chosen type lives in component state, not the URL (no use case for deep-linking create-per-type). Switching types preserves carry-over as today.
- **Edit (`/:id`):** `getUpstreamProvider(id)` on mount (API client function already exists) → `buildForm(providerType, provider)`. Loading uses `Spinner`; fetch error → `ErrorBanner` + Back, no empty form to accidentally save.
- **Save:** `buildPayload` → POST `/upstream-providers` (create) or PUT `/upstream-providers/:id` (edit) via the existing client functions. On success → toast + `navigate('/upstream-providers')`; no in-place refresh.
- **Dirty guard:** plain `window.confirm` when leaving with unsaved changes; no router-blocking library (matches the codebase: no detail page has one today).

## Page Layout & UX

```
┌────────────────────────────────────────────────┐
│ ← Back    [type icon] Provider Name    [Save] │  ← sticky header
├────────────────────────────────────────────────┤
│  Section: Identity / Endpoint / Routing / ...  │  ← schema-driven, identical
│  (fields render exactly as the modal does now) │     to today's definitions
└────────────────────────────────────────────────┘
```

- Sticky header: Back (`navigate(-1)` falling back to the list), title (provider name or "New Provider"), type badge, primary Save — always visible while scrolling the long form, the main win over the height-limited modal.
- Content max-width ~900px, single column of stacked sections — an operator form, not a dashboard.
- Create mode step 0: type-picker grid; afterwards the type is switchable via a header dropdown (carry-over still applies).
- Validation: per-field inline + aggregate banner below the header (as today).
- Success: toast + navigate back to the list.

## Modal Removal & File Split

Removed from `UpstreamProvidersPage.jsx`: `UpstreamProviderEditor` + its `<Modal>` render + `editing` state; `buildSchemas`, `buildForm`, `buildPayload`, `validateAPIKeyEntries`, `hydrateEntries`, `idHintForIdentity`; constants `MAX_ENTRY_WEIGHT`, `ROUTING_STRATEGY_OPTIONS`, `OAUTH_TYPES`, `API_KEY_TYPES`, `URL_RE`; `isOAuth/isOpenAI/isClaude`; `OAuthConnectSection` + its helpers (`extractAuthFields`, `buildOAuthCreatePayload`, `AUTH_TYPE_TO_OAUTH_CHANNEL`, `isServiceAccountJson`); `APIKeyEntriesEditor`, the list sub-editors, the field engine (`Field`/`renderField`, `PasswordInput`).

Kept on the list page: fetch/reload, filters, bulk actions, confirm modals, sync status, row buttons (now navigating), `+ New Provider` (now navigating), and `ImportOAuthProviderModal`.

**Tests:** the entries test file moves its imports to `upstream-provider-editor/form.js` + `schemas.js` (exports stay public for tests, matching the existing "exported solely for focused tests" convention). No test is deleted — only the import address changes; the 156-test suite must stay green.

**No backend changes whatsoever** — API, DTO, store, conductor untouched.

## Testing

- Pure-function tests move unchanged (assertions identical); target: 156 dashboard tests green.
- New tests for the route wrapper (within node:test as far as component testing exists in the repo; otherwise pure logic only + manual QA): mode `new` shows the type picker; picking a type renders the schema sections; mode `:id` with a mocked fetch hydrates the form; Save issues POST vs PUT correctly; the dirty guard fires on unsaved navigation.

## Error Handling

- Edit fetch failure (unknown id / PG 503): ErrorBanner + Back — no empty form, no crash.
- Validation: per-field inline + per-entry `validateAPIKeyEntries` + aggregate count banner.
- Save 400 (e.g. duplicate name): server message shown in the banner, form contents preserved (never reset).
- OAuth create failures: server message surfaced; the auth flow is never silently retried.
- Dirty navigation: browser `confirm()`.
- Concurrent editors: last-write-wins full PUT (same as the modal today; no optimistic locking in scope).

## Final verification

`npm test` (156 green) → `npm run build` green → `make dash-embed` → manual QA: create each type, edit + deep-link refresh, back button, OAuth connect flow, list page without the modal.
