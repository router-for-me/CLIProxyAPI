# Design: Routing Strategy for Upstream Provider API Key Entries

**Date:** 2026-09-03
**Status:** Implemented (feat/entry-routing-strategy; implementation plan at 2026-09-03-upstream-entry-routing-strategy-plan.md)
**Goal:** Give upstream-provider pools (Claude API Key + OpenAI Compatibility) a row-level routing strategy and per-entry priority, and — the core need — aggressive failover: when any entry fails with any error, the request is re-routed to the next entry in the same pool with zero downtime; errors only surface to the end user after the whole pool is exhausted.

## Context & Motivation

Today all API key entries of a provider row are peers: the fan-out renderer copies the row-level `Priority` to every entry, and the scheduler round-robins them. An operator cannot express "key A is primary, keys B/C are backups".

The motivating symptom (reported by the operator): when an entry hits an error/limit, cooldown is recorded but the in-flight request is **not** re-routed to the next entry — the end user sees the error.

Code verification of that symptom:

- The in-flight failover loop **already exists** (`sdk/cliproxy/auth/conductor_execution.go:275-399` `executeMixedOnce`): on a non-invalid upstream error it calls `MarkResult` (cooldown) and `continue`s, and `pickNextMixed` picks another auth (failed auths sit in `tried`).
- However `isRequestInvalidError` (`conductor_execution.go:378`, defined `conductor_cooldown.go:1709`, backed by `internal/clienterror/client_error.go:73` `IsRequestFault`) **hard-stops rotation** for errors classified as client request faults: status 400/409/413/422, or bodies carrying `invalid_request_error`, `message_too_big`, `context_length_exceeded`, etc. Many OpenAI-compatible providers report quota/credential problems in exactly this shape — cooldown happens, rotation does not, the user sees the error. This matches the reported symptom.
- Priority tiering also already exists: every synthesized auth carries a `priority` attribute; the scheduler always picks the highest non-empty priority bucket and descends when the upper tier cools down (`scheduler.go:824` `highestReadyPriorityLocked`). "Primary → backup → emergency" ordering therefore needs only a per-entry priority field — no new selection logic.

## Decisions (from the brainstorming session)

1. **Config location:** strategy is a **row-level** dropdown on the provider row; **priority** (and existing weight) are **per-entry** fields. Strategy is a property of the pool, not of a single entry.
2. **Strategy values:** `default (empty) | round-robin | weighted-round-robin | fill-first | priority | failover`, where `priority` ≡ fill-first and `failover` ≡ round-robin within a tier (Model-Routes-compatible aliases; the UI hint documents the aliasing). Default/empty inherits the global `routing.strategy` and keeps today's behavior.
3. **Provider types:** `claude-api-key` and `openai-compatibility` (the two entry-bearing types). Single-key types are untouched.
4. **Core semantic:** any strategy value set on the row opts the pool into **aggressive failover** — when an entry fails with *any* error (including request-fault-classified ones), the next entry is tried immediately; errors surface only after the whole pool is exhausted.
5. **Activation:** opt-in. Rows without a strategy behave exactly as today (request-fault hard-stop included).
6. **Rotation scope:** aggressive rotation stays **within the pool** — no jumping to another provider row or type. When the whole pool fails, the last error is returned to the user.

## Semantics Summary

- `default (empty)` — today's behavior: inherit global `routing.strategy`; request-fault errors hard-stop rotation.
- `round-robin` / `failover` — even rotation among same-priority entries (within the highest ready tier).
- `weighted-round-robin` — proportional rotation using the per-entry `weight` field.
- `fill-first` / `priority` — stick to the first entry of the tier until it is exhausted/cooldown, then the next.
- Per-entry `priority` forms tiers (higher = preferred; scheduler already descends tiers on cooldown/error). Row-level priority is inherited by entries that leave priority blank.

Known limitation (unchanged): once a stream has sent bytes to the client, switching keys cannot happen silently — failover only applies before the first byte is sent.

## Architecture & Data Model

Pipeline follows the existing flow: **PG rows → render to config.yaml → synthesizer → auth attributes**. All new fields are optional/nullable; existing rows keep their behavior.

**1. PG schema (2 idempotent migrations, `ADD COLUMN IF NOT EXISTS`)**

- `upstream_providers` + `routing_strategy TEXT NULL` — NULL/empty = default (today's behavior).
- `upstream_provider_api_key_entries` + `priority INTEGER NULL` — NULL = inherit row-level priority (same pattern as the earlier `weight` migration, `postgresstore.go:708`).

**2. Store structs + DTOs**

- `store.UpstreamProvider` + `RoutingStrategy string` (json `routing_strategy,omitempty`).
- `store.UpstreamProviderAPIKey` + `Priority *int` (json `priority,omitempty`).
- Shared entry DTO `upstreamProviderEntryReq` + `Priority *int`; provider request + `routing_strategy`. Because the DTO is shared, both types gain the fields at once.

**3. Config layer (`internal/config/`)**

- `OpenAICompatibility` + `Strategy string` (`yaml:"strategy,omitempty"`) — the strategy belongs to the pool for this type.
- `OpenAICompatibilityAPIKey` + `Priority *int` (new — priority previously existed only at pool level).
- `ClaudeKey.Priority` already exists. Fan-out now uses per-entry priority when set; unset inherits the row-level priority (as today). The strategy is carried via a carry-through field `UpstreamProviderStrategy string` (`yaml:"upstream-provider-strategy,omitempty"`, `json:"-"`) — same pattern as the existing `UpstreamProviderID`. Hand-written YAML without these fields = no pool strategy (legacy behavior).
- Validation: strategy values must be one of `round-robin, weighted-round-robin, fill-first, priority, failover`; anything else is rejected (400 at the API layer).

**4. Renderer (`internal/upstreamsync/render.go`)**

- Claude and OpenAI-compat fan-out maps per-entry priority and pool strategy onto config items. Entry priority set → item priority = entry value; unset → row-level priority (today's behavior).
- `seed.go` performs the inverse mapping so the `seed(render(state))` round-trip preserves state.

**5. Synthesizer (`internal/watcher/synthesizer/config.go`)**

- Auths synthesized from a pool with a strategy get `attrs["pool_strategy"] = <normalized value>`; pool identity already exists as `provider_key` (e.g. `claude:<rowID>`, `openai-compatible-<name>`).
- Per-entry priority flows through the existing `priority` attribute mapping.
- The presence of `pool_strategy` on an auth is what activates aggressive failover in the conductor.

## Runtime Behavior (Conductor)

Three mechanisms, each attached to a verified point in the code:

**A. Aggressive failover (the zero-downtime core).** The `isRequestInvalidError` gates in the three execution loops — `executeMixedOnce` (`conductor_execution.go:378`), `executeCountMixedOnce`, `executeStreamMixedOnce` — become conditional: when the just-failed auth carries a non-empty `pool_strategy`, the hard-stop is suppressed, the request-fault error is treated like a retryable one (`MarkResult` still records the cooldown per its existing classification — 429 backoff ladder, 401 30 minutes, etc.) and the loop `continue`s → the next entry is picked. The failed entry is already in `tried`, so it cannot be re-picked. Rows without a strategy keep the old code path untouched.

**B. Pool scoping.** When suppression first triggers, the request's candidate list narrows to the failed pool's routing key (from the auth's `provider_key`): the existing `authMatchesProvider` matching (pool + entry keys) already handles this, so rotation cannot jump to PoolB or another provider type. Once the whole pool is exhausted, the **last** error is returned (`shouldReturnLastErrorOnPickFailure` already prefers lastErr over "no auth available").

**C. Ordering.** Per-entry priority → `priority` attribute → the existing scheduler picks the highest ready tier. The row strategy governs picking *within* a tier: when a model route pins the pool **without** its own strategy, `applyPinnedRoute` (`sdk/api/handlers/handlers_routing.go:116`) uses the pool's strategy as the route default — `priority`→fill-first, `failover`→round-robin (existing `routeStrategyFromMetadata` mapping). Unpinned traffic follows the global `routing.strategy` — documented as a feature boundary.

End-to-end zero downtime: in-flight requests rotate immediately (A); subsequent requests automatically avoid cooled-down entries (existing per-model cooldown state).

## Dashboard (`web/dashboard/src/pages/UpstreamProvidersPage.jsx`)

Follows the recently added `weight` field pattern:

1. **Schema** (`buildSchemas`, ~1359): `claude-api-key` and `openai-compatibility` gain `{ name: 'routing_strategy', type: 'select', options: [default, round-robin, weighted-round-robin, fill-first, priority, failover] }` in the Identity section, with a hint: *"Empty = follow the global routing strategy. Setting any value enables aggressive failover between entries: any error on one key immediately routes to the next entry; errors surface only after the whole pool is exhausted. `priority` ≡ fill-first, `failover` ≡ round-robin."*
2. **`APIKeyEntriesEditor`** (~3115): optional **priority** column per entry row (next to weight): blank = inherit row-level priority; any integer (0 is valid = tier 0).
3. **`buildForm`**: hydrate `routing_strategy` (row) + per-entry `priority` (round-trip id + name as the existing OpenAI branch does).
4. **`buildPayload`**: emit row `routing_strategy` + per-entry `priority` (omit when blank).
5. **Validation**: extend `validateAPIKeyEntries` — priority blank-or-integer; strategy must be a member of the valid list.
6. **`modelRouteProvider.js`**: unchanged — pool/entry routing keys are already produced; the row strategy only defaults routes that pin the pool without their own strategy.
7. **Build order**: `npm run build` green, then `make dash-embed` so dist is copied into `internal/dashboardasset` before `go:embed` (known pitfall).

UX note: no "aggressive failover active" indicator elsewhere — the dropdown hint on the same row is sufficient, since the strategy is a visible property of that pool.

## Testing

**Go unit (TDD — failing test first):**

1. **`upstreamsync` render:** entries with priority → config items carry entry priority; unset → inherit row; row with strategy → stamped in the fan-out. **seed:** inverse round-trip `seed(render(state))` returns identical state (priorities + strategy preserved).
2. **Synthesizer:** auths from a strategy pool carry a normalized `pool_strategy`; without a strategy the attribute is absent.
3. **Conductor — aggressive failover core** (in `sdk/cliproxy/auth/`, next to `route_strategy_test.go`): manager + stub executor, pool of 3 strategy-bearing entries; first entry fails with **400 + `invalid_request_error` body** → next entry picked → success returned; `MarkResult` still recorded the failure. Without a strategy → today's behavior (immediate error). All entries fail → the last error is returned. Verify rotation does not jump outside the pool (a PoolB auth is never picked).
4. **Handlers:** `applyPinnedRoute` — a route pinning the pool without its own strategy + a strategy-bearing row → the pool strategy becomes the stashed default. A route with its own strategy still wins.
5. **Store/PG (`test/`)**: idempotent column migrations; PUT provider with strategy + entry priorities → persisted and round-tripped; invalid strategy value → 400.

**Frontend:** `npm run build` green; manual QA — set `failover` + priorities 10/5/1, save, re-edit (values hydrate), Model Routes picker unchanged.

**Explicitly tested error handling:** a *genuine* request fault (`context_length_exceeded`) on a strategy pool is still rotated (by design — the confirmed opt-in trade-off), then the original error is returned after the pool is exhausted; a cooled-down entry does not block the others (per-model state, not pool-level); credentials never appear in logs.

**Regression:** `go build ./cmd/server` + `go test ./...` green — especially `upstreamsync`, `synthesizer`, `config`, `store`, and the existing routing/conductor suites (default behavior must not change at all).
