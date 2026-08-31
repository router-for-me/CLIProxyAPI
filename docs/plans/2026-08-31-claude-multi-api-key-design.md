# Design: Claude (API Key) Multi-Entry Support

**Date:** 2026-08-31
**Status:** Approved (brainstorming session, all sections validated)
**Goal:** The "Claude (API Key)" upstream-provider type supports multiple API key entries — same as "OpenAI Compatibility" — with per-entry identity, proxy URL, and weight, rendered as a round-robin pool with per-entry route pinning.

## Context

Today the two provider types model credentials differently:

| | Claude API Key (`claude-api-key`) | OpenAI Compatibility (`openai-compatibility`) |
|---|---|---|
| Dashboard secret input | One `api_key` password field (`UpstreamProvidersPage.jsx` `apiKeyBase`) | `api_key_entries` list editor (`APIKeyEntriesEditor`) |
| PG persistence | `upstream_providers.api_key` column | `upstream_provider_api_key_entries` child table (synced by stable child id) |
| config.yaml | flat `claude-api-key:` list, one key per item | `openai-compatibility[i].api-key-entries[j]` |
| Renderer | `render.go` `claudeKeyFromProvider` reads `p.APIKey`, **ignores `p.APIKeyEntries`** | `openAICompatFromProvider` maps entries |
| Route pinning | per-provider-row only (`claude:<rowID>`) | per-entry (`<providerKey>:key-<entryID>`) |

Key findings that shaped this design:

- The store and management API already accept `api_key_entries` for **any** provider type — there is no per-type gate. `store.UpstreamProvider` carries both shapes; `syncAPIKeyEntriesTx` does id-stable upserts regardless of type.
- The auth manager already round-robins every synthesized auth with `Provider: "claude"` — N Claude rows already load-balance natively. The gap is purely in the authoring model (grouping, per-entry identity/proxy/weight, per-entry pinning), not in load balancing.
- `config.ClaudeKey` already has per-item `Weight`; `OpenAICompatibilityAPIKey` already has `UpstreamProviderEntryID` for stable per-entry identity.

## Decisions (from the brainstorming session)

1. **Form UX:** replace the single `api_key` field with the `API Key Entries` editor (same as OpenAI). Legacy rows keep working: the old key hydrates as the first entry on edit, and an untouched row continues using the `api_key` column. A submitted provider with neither entries nor a legacy key is rejected as having no credential.
2. **Per-entry pinning:** yes — each entry gets its own routing key (`claude:<rowID>:key-<entryID>`), so Model Routes can pin to a specific key, exactly like OpenAI entries.
3. **Weight per entry:** yes — the editor gains an optional weight field, benefiting Claude and OpenAI equally (the backend already supports both).
4. **Error behavior:** use the existing auth-manager cooldown mechanism — no new failover logic. Keys that hit rate limits/invalid-auth cool down; round-robin picks the others.
5. **UI scope:** only the Upstream Providers page. The legacy Manage CPA page stays single-key (it writes config.yaml directly and is out of scope).
6. **Approach:** **A — renderer fan-out.** One Claude provider row with N entries in PG fans out to N flat `claude-api-key` items in config.yaml. Full reuse of the OpenAI pipeline (child table, id-sync, round-robin, per-entry pinning). config.yaml remains valid for manual/non-dashboard users.

## Architecture & Data Model

```
Dashboard (1 row, N entries)
  → PUT /v0/management/upstream-providers/:id
    → store.UpstreamProvider.APIKeyEntries (child table, sync by-id)  [exists]
      → upstreamsync/render.go: fan-out → N × config.ClaudeKey
        → config.yaml `claude-api-key:` (flat list, N items)
          → synthesizer: N auths, provider="claude", round-robin  [exists]
```

Data model changes (3 places):

1. **PG child table** `upstream_provider_api_key_entries`: add `weight INTEGER NULL` (idempotent `ADD COLUMN IF NOT EXISTS` at schema migration, `postgresstore.go` ~692). NULL = weight 1, matching `Weight *int` semantics in config.
2. **`store.UpstreamProviderAPIKey`** and the shared request/response DTO `upstreamProviderEntryReq`: add `Weight *int` (json `weight,omitempty`). Since the DTO is shared, OpenAI entries gain weight for free.
3. **`config.ClaudeKey`**: add `UpstreamProviderEntryID int64` (`yaml:"upstream-provider-entry-id,omitempty"`, `json:"-"`), mirroring `OpenAICompatibilityAPIKey.UpstreamProviderEntryID`. The flat list shape is preserved — no new nesting, so hand-written config.yaml keeps working.

**Fan-out rule** (replaces the current `claudeKeyFromProvider` that ignores entries): a provider with ≥ 1 entries renders one `ClaudeKey` **per entry** — `APIKey` from the entry, `Weight` and `ProxyURL` per entry (entry overrides row-level `ProxyURL` when set) — plus the row-level fields copied to every item (Priority, Prefix, BaseURL, Models, Headers, ExcludedModels, RebuildMidSystemMessage, DisableCooling, Cloak, ExperimentalCCHSigning). A provider with no entries falls back to the single `p.APIKey` (legacy row). Each item carries `UpstreamProviderID` (row) and `UpstreamProviderEntryID` (entry; 0 for legacy fallback).

## Backend Changes

**1. `internal/upstreamsync/render.go` — fan-out**
`claudeKeyFromProvider` (line ~138) returns `[]config.ClaudeKey`:
- entries present → one item per entry with per-entry key/weight/proxy and row-level fields copied; stamp `UpstreamProviderID: p.ID`, `UpstreamProviderEntryID: e.ID`.
- entries empty → one legacy item from `p.APIKey` (`UpstreamProviderEntryID: 0`).
- `RenderConfig` (~line 64) appends the returned slice for `TypeClaudeAPIKey`.
- When an entry proxy is empty, retain the provider-level proxy as its default; an explicit entry proxy overrides it.

**2. `internal/upstreamsync/seed.go` — inverse collapse**
`SeedFromArtifacts` (~line 53) iterates `cfg.ClaudeKey` and calls `st.Create(providerFromClaudeKey(k))` **per item**. Today the loop is **one item → one provider row**, which is what legacy YAML expects. The fan-out shape can survive this loop unchanged because:

- Hand-written YAML entries without `UpstreamProviderID` (entry-id 0) seed exactly as they did before — each item becomes its own row with `api_key` in the parent column.
- Forward render writes back `UpstreamProviderID: p.ID` for every emitted item; a re-seed of an operator-edited YAML (whose items carry the same `UpstreamProviderID`) would still create new rows because `st.Create` always returns a fresh id and ignores the input id.

`providerFromClaudeKey` (~line 180) should copy `UpstreamProviderID` and `UpstreamProviderEntryID` into the produced `store.UpstreamProvider` *when non-zero*, so a future iteration that supports upsert-by-id (or alternative seeding strategies) can preserve identity. With the current loop semantics this only affects observability/debugging, not persistence. For dashboard-managed rows, identity is established by the forward renderer and survives `seed(render(state))` only because the forward direction is lossless within a single boot; across boots the absence of upsert semantics means operator edits to `config.yaml` may produce a new provider row. Document this in the runtime caveat.

**3. OpenAI Compatibility renderer parity**
`openAICompatFromProvider` (~line 174) and `providerFromOpenAICompat` (~line 217) propagate the new `Weight` field on entries so OpenAI rows also retain weight across round-trips, ensuring parity with Claude entries.

**4. Weight validation — no change**
`internal/config/weight.go` already validates `claude-api-key[i].weight` per item; fan-out produces one item per entry, so per-entry weights are validated automatically.

**API layer — no change**
`toUpstreamProvider` already maps `body.APIKeyEntries` for any type; the handler routes are unchanged. Entry name normalization/validation (`normalizeUpstreamProviderEntryName`, duplicate/reserved-name rules) already apply.

## Dashboard Changes (`web/dashboard/src/pages/UpstreamProvidersPage.jsx`, `components/modelRouteProvider.js`)

**1. Schema (Claude):** in `buildSchemas()` (~1359), the `claude-api-key` schema adds `{ name: 'api_key_entries', label: 'API key entries', type: 'api_key_entries', hint: 'Multiple keys form a round-robin pool for this provider.' }` in the Identity section, replacing the single `api_key` field. Behavior toggles and the Cloak section stay — they are row-level and the renderer copies them to every entry item.

**2. `APIKeyEntriesEditor` (~3115):** each entry row gains an optional **weight** field next to identity / api key / proxy url: blank = default 1; positive integers only; max 1,000,000 (matching the config doc comment). Per-row hints unchanged.

**3. `buildForm` (~3246) — Claude hydration:** read `initial.api_key_entries` (round-trip `id` + server-normalized `name`, same as the OpenAI branch). If entries are empty but `initial.api_key` is set (legacy row never edited), create one entry from the old key with no id — on save, the backend's id-sync inserts it as the first entry.

**4. `buildPayload` (~3315):** the `claude-api-key` branch emits `api_key_entries` (`id`, `name`, `api_key`, `proxy_url`, `weight` — weight only when filled) **plus** the row-level Claude fields (`name`, cloak fields, toggles). The single `api_key` field is no longer sent.

**5. `modelRouteProvider.js` — Claude entry choices:** `expandProviderToChoices` (~78) currently returns one provider-level choice for all non-OpenAI types. Change: a `claude-api-key` row with `api_key_entries` also produces one entry-level choice per entry with a derivable identity (`claude:<rowID>:key-<id>`, identity via `generateEntryIdentity`), alongside the provider-level choice (`claude:<rowID>`, count = entries length) — mirroring the OpenAI branch. Existing routes pinned to `claude:<rowID>` keep working via the conductor's wildcard match.

**6. Out of scope:** the legacy Manage CPA page and its single-key modal are untouched.

## Migration & Compatibility

- **Schema:** one idempotent `ADD COLUMN IF NOT EXISTS weight INTEGER NULL` — no backfill, no destructive change.
- **Legacy rows** (api_key set, no entries) keep working with zero migration: renderer falls back to one item; the UI hydrates the old key as the first entry on first edit.
- **config.yaml round-trip:** `seed(render(state))` returns identical PG state (entry ids and weights preserved). Hand-written flat `claude-api-key` lists (entry-id 0) remain valid and seed as single-key rows.

## Error Handling

- **API 400** on duplicate entry names / reserved `key-<digits>` names — existing `normalizeUpstreamProviderEntryName` + `syncAPIKeyEntriesTx` name-swap handling are reused as-is.
- **UI inline errors per entry row:** slug `^[a-z0-9][a-z0-9_-]*$`, duplicates, reserved names — `validateAPIKeyEntries` is reused for Claude; add weight validation (blank or positive integer, ≤ 1,000,000).
- **Renderer safety:** entries with an empty `api_key` are skipped during fan-out (defensive; invalid rows should already be rejected at the sync layer).
- **Pin stability:** entry identity always derives from the persisted child-row id (`key-<entryID>`), never from the renameable name.
- **Logging:** keys never appear in logs or error messages (per repo convention).

## Testing

- **Go unit — render:** entries ≥ 1 → N items; entries empty + `api_key` set → 1 legacy item; per-entry weight/proxy carried; row-level fields copied to all items; both id fields stamped.
- **Go unit — seed:** N items sharing a row-id → 1 provider + N entries; entry-id 0 → legacy `p.APIKey`; lossless round-trip (entry ids + weights preserved).
- **Go unit — synthesizer:** `entry_provider_key = claude:<rowID>:key-<entryID>` and row `provider_key` stamped; identity from child id, not name.
- **Go integration (`test/`):** PG id-sync for Claude entries (insert/update/delete); PUT a provider with entries → rendered config.yaml contains N `claude-api-key` items.
- **Frontend:** `npm run build` green; manual QA — create a Claude provider with 2 entries, edit (entries + ids preserved), distinct weights, delete one entry, model-route picker shows provider-level + per-entry choices.
- **Regression:** `go build ./cmd/server` and `go test ./...` stay green (especially `upstreamsync`, `synthesizer`, `config`, `store`).
