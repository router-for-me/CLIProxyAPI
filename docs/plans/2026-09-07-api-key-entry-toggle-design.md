# Design: Per-entry on/off toggle for Upstream Providers API key entries

**Date:** 2026-09-07
**Status:** Approved (brainstorming session 2026-09-07)
**Goal:** Add an on/off (disabled) toggle per API key entry in the Upstream Providers detail/edit page (`/upstream-providers/:id`), letting operators deactivate one credential without deleting it.

## Current state

The entries editor (`web/dashboard/src/pages/upstream-provider-editor/EntriesEditor.jsx`) renders each entry as one row: identity, api key, proxy pool picker, proxy url, weight, priority, remove button. No `disabled` field exists anywhere at entry level today — only row-level (`upstream_providers.disabled`). The feature follows the same path weight and priority per-entry previously took.

## Decisions

1. **Toggle placement & semantics**: a switch at the left of each entry row (before the identity input), `data-testid="api-key-entry-disabled-<idx>"`. Off (default) = active; on = disabled. A disabled row renders dimmed (opacity ~0.5 on fields; api key + proxy still readable) with a small `disabled` badge. The toggle stays clickable while dimmed. Value lives in form state as `disabled: true/false` per entry via the existing `update(idx, {...})`.
2. **Commit-on-save, no instant PATCH**: the toggle only mutates local form state; commit happens on the header "Save changes" (dirty guard + auto hot-reload, same as every other field). Upstream-providers has no per-entry PATCH endpoint; adding one is out of scope.
3. **Runtime exclusion — filter at the renderer** (option a): a disabled entry is not rendered into `config.yaml` at all. Simple, no scheduler changes, works identically for hand-edited YAML. Data stays intact in PG so toggling back re-activates the entry. Rejected option: threading a flag through the scheduler/conductor (wider blast radius, no operator-visible gain).
4. **Disabled entries are still persisted and still sent in the payload** — only entries with a blank api_key are filtered out of `buildPayload`. The frontend "at least one entry" Claude validation counts disabled entries as valid (the key exists; it is just inactive), so a provider with all entries disabled can be saved without error.
5. **All-entries-disabled rendering**: the provider renders with an empty key list — same behavior as a provider with no entries today (matches no routing, no crash). No special guard.

## Backend changes (following the per-entry priority/weight pattern)

1. **Config types** (`internal/config/config_types.go`): `Disabled bool` with `yaml:"disabled,omitempty" json:"disabled,omitempty"` on `OpenAICompatibilityAPIKey` and `ClaudeKey` (the Claude fan-out item).
2. **Store** (`internal/store/pg_upstream_providers.go`): `UpstreamProviderAPIKey.Disabled bool`; migration `ALTER TABLE <entries> ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE` in `postgresstore.go` (non-null + default → legacy rows safe, no NULL scan handling); wire into INSERT/SELECT/UPDATE in `syncAPIKeyEntriesTx` + the scan loop.
3. **Management handler** (`internal/api/handlers/management/upstream_providers_types.go`): `upstreamProviderEntryReq.Disabled bool \`json:"disabled,omitempty"\`` + wiring into the store struct.
4. **Renderer** (`internal/upstreamsync/render.go`): skip entries with `Disabled: true` in the Claude and OpenAI-compat fan-out loops (`buildClaudeKeysWithPools` / `openAICompatFromProviderWithPools`).

## Frontend changes

1. **`EntriesEditor.jsx`**: the per-row toggle + dimmed styling + `disabled` badge.
2. **`form.js`**: `hydrateEntries` round-trips `disabled: !!(e && e.disabled)`; `buildForm` legacy-synthesis path (Claude single `api_key`) sets `disabled: false`; `buildPayload` sends `disabled: !!e.disabled` per entry.
3. **`schemas.js`**: unchanged (schema-driven sections don't touch the entries editor).

## Error handling

- Backend: plain bool with default false — no invalid state; migration is idempotent.
- Renderer: no guard needed for the all-disabled case (empty list renders like no entries).
- Frontend: toggle cannot produce invalid state; `buildPayload` is idempotent (toggle on→off without save = original payload).

## Testing

1. **Go** (`pg_upstream_providers_test.go`): round-trip `Disabled` (true / false / legacy row → false). **`upstreamsync` render tests**: a `Disabled: true` entry is absent from the rendered `[]config.ClaudeKey` / `OpenAICompatibility.APIKeyEntries`; active siblings still render.
2. **Frontend** (`editor.test.js`): `hydrateEntries` round-trip; `buildPayload` emits `disabled: true` and keeps keyed disabled entries (no filter regression). **`editor-render.test.jsx`**: disabled row renders the toggle checked + dimmed class.
3. **Manual**: `make dash-embed` → toggle → Save → entry absent from the rendered `config.yaml` block → toggle back → entry returns.

## Implementation order

1. Config types: `Disabled` on `OpenAICompatibilityAPIKey` + `ClaudeKey`.
2. Store: struct field + migration + INSERT/SELECT/UPDATE.
3. Handler request type + store-struct wiring.
4. Renderer: copy flag, skip disabled entries at render.
5. Frontend: `EntriesEditor.jsx` toggle + `form.js` hydrate/payload.
6. Tests + `gofmt` + `go build` verify + `make dash-embed`.

## Out of scope

- Instant per-entry PATCH endpoint (would be a separate design; auth-files-style row toggles).
- Toggle for OAuth auth-files (already exists as Enable/Disable buttons).
- Scheduler-level exclusion of disabled credentials (superseded by renderer filtering).
