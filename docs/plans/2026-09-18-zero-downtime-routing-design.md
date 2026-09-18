# Zero-Downtime Routing — Design

Date: 2026-09-18
Status: Validated (brainstorming complete, not yet implemented)
Reference: OmniRoute (github.com/diegosouzapw/OmniRoute) combo + resilience design;
the prior 2026-09-11 incremental design (G2/G3/G4/G5/G6) and the 2026-09-03
entry-routing-strategy design.

## Context

NixLLM already has aggressive in-pool failover, per-entry priority, failed-pool
pinning, per-auth+model cooldown, and cooldown-aware execution loops. The
"Zero Downtime" requirement adds four operator-facing capabilities on top of
that foundation:

1. **Per-entry auto retry** — retry the *same* upstream entry a small number of
   times on transient failures before pool failover.
2. **Force failover on a `max_time` budget** — when the budget for an entry is
   exhausted mid-flight, cancel the in-flight request and move on.
3. **Picker UX** — only LIVE providers shown in the model-routing picker; new
   pins auto-assign `max(existing pinned) + 1`.
4. **Upstream provider UI/UX cleanup** — denser entry rows, inline live status,
   shared poller, bulk actions.

No new layer is introduced. All four mechanisms attach to existing paths.

## Scope and non-goals

**In scope**

- Per-entry retry (transient only) with a per-entry time budget.
- Cancellation contract for `max_time` enforcement.
- Server-side picker API: LIVE filter + atomic priority assignment on pin.
- Compact entry row + shared live-status poller in the dashboard.

**Out of scope** (YAGNI, deliberately dropped)

- Per-request retry budget shared across pool candidates (orthogonal to the
  per-entry budget; G5 already covers the closest analogue).
- Retry on auth/quota/model-mismatch errors (401/403/400/404) — those classes
  hard-failover immediately today and continue to.
- Manual priority reordering UI (auto-incremental already gives sane order).
- Cost-optimized strategy (needs registry pricing data, unrelated).
- Drag-to-reorder on the provider list.

## Architecture placement

```
                          ┌────────────────────────────────┐
   client request ──────► │  executor.Execute / ExecuteStream │
                          │   └─► inner per-entry retry loop  │  ← new (1)(2)
                          │        └─► existing single attempt │
                          │              └─► existing pool failover │
                          └────────────────────────────────┘
                                          │
                                          ▼
                          ┌────────────────────────────────┐
                          │  authManager.CooldownStateSnapshot │  ← consumed by
                          │  providerKeyIsLive (frontend)      │  ← new (3)
                          └────────────────────────────────┘

   dashboard SPA ────────► GET /v0/management/upstream-providers/live-status  ← new (4)
                       └─► GET /v0/management/model-routing/picker?model=X    ← new (3)
                       └─► POST /v0/management/model-routing/pin              ← new (3)
```

Data flow stays canonical: PG row → render → auth attribute → conductor.
The dashboard reads the same `providerKeyIsLive` rule everywhere.

## 1. Per-entry auto retry

### Configuration

New section in `internal/config/config_types.go` under `RoutingConfig`:

```go
type RoutingConfig struct {
    CooldownWait *CooldownWaitConfig `yaml:"cooldown_wait"`
    Retry        *RetryConfig        `yaml:"retry"`
}

type RetryConfig struct {
    MaxAttempts uint16        `yaml:"max_attempts"` // default 3; 0 disables
    MaxTimeMS   uint32        `yaml:"max_time_ms"`  // default 5000
    BackoffMS   uint32        `yaml:"backoff_ms"`   // default 200
    RetryOn     []int         `yaml:"retry_on"`     // default [500,502,503,504,408,429]
}
```

Per-entry overrides on `api_key_entries`:

- `retry_max_attempts smallint NULL`
- `retry_max_time_ms integer NULL`
- `retry_backoff_ms integer NULL`

When an entry override is set it replaces the global default; otherwise the
global default applies. PG migration is one ALTER TABLE; the snapshot
projection (`internal/configsnapshot/`) round-trips the three fields.

### Hot-path semantics

In `internal/runtime/executor/conductor_execution.go`:

1. The outer execution loop picks entry `E` (existing cooldown-aware logic).
2. A new inner loop wraps the single-attempt path:

```go
deadline := time.Now().Add(entryMaxTimeMS(ctx, E)) // entry override or global
for attempt := 1; ; attempt++ {
    if time.Until(deadline) <= 0 {
        break // reason: budget_exhausted
    }
    subCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
    attemptCtx := helps.AttemptCtx{
        Entry: E, Attempt: attempt, StartedAt: time.Now(),
        StreamStarted: &firstByteSent,
    }
    result, err := executeOnce(subCtx, attemptCtx, ...)
    cancel()
    firstByteSent = firstByteSent || result.streamHasFirstByte

    if firstByteSent {
        // streaming retry boundary; never retry past this point
        return result, err
    }
    if !isRetryable(result) {
        return result, err // reason: non_transient
    }
    if attempt >= entryMaxAttempts(E) {
        break // reason: attempts_exhausted
    }
    sleep(helps.NextBackoff(deadline, globalBackoff, attempt))
}
```

3. The inner loop returns one of these reasons:
   `success | non_transient | attempts_exhausted | budget_exhausted |
   stream_started | parent_ctx_done`.
4. All "give up on this entry" reasons fall through to the existing outer
   pool failover — no change to that path.

### Retryable classes

Default `retry_on`: `[500, 502, 503, 504, 408, 429]`. Network errors
(`url.Error`, EOF, TLS handshake) are retried regardless of status. The
classifier lives in `internal/runtime/executor/helps/retry_classify.go`:

```go
func IsRetryable(resp *Response, err error) bool
```

It is **not** a generalization of `isRequestInvalidError` — that one is for
4xx hard-failover gating; this one is its inverse.

### Backoff

`helps.NextBackoff(deadline, base, attempt)` returns
`min(base * 2^(attempt-1), time.Until(deadline)/2)`. Honors `Retry-After`
header when present and `Retry-After ≤ time.Until(deadline)`.

### Streaming

Retry only runs before `firstByteSent`. Once the first byte is written, the
inner loop exits with `stream_started` and the response is returned as-is.
The outer pool-failover rule "no failover after stream start" remains in force.

### Cooldown interaction

Each inner attempt calls `MarkResult` exactly as today. A 429 during a retry
still cooldowns the entry; subsequent retries see the entry in cooldown and
short-circuit via the existing `waitForCooldown` path. The inner loop and the
cooldown-aware loop remain orthogonal.

## 2. Force failover on `max_time`

### Cancellation contract

`internal/runtime/executor/helps/retry_budget.go`:

```go
// EntryBudget returns the per-entry retry budget for one request.
// Caller owns cancel(); always defer cancel().
func EntryBudget(parent context.Context, entry EntryRef) (ctx context.Context, cancel context.CancelFunc, deadline time.Time, reason *ExitReason)

type ExitReason string
const (
    ReasonSuccess         ExitReason = "success"
    ReasonNonTransient    ExitReason = "non_transient"
    ReasonAttemptsOut     ExitReason = "attempts_exhausted"
    ReasonBudgetOut       ExitReason = "budget_exhausted"
    ReasonStreamStarted   ExitReason = "stream_started"
    ReasonParentCtxDone   ExitReason = "parent_ctx_done"
)
```

Two guards ensure termination:

1. `subCtx, _ := context.WithTimeout(parent, time.Until(deadline))` — stdlib
   HTTP client cancels in-flight `Do()` when `subCtx` expires.
2. Explicit `select { case <-deadlineC: ... }` to break out of the inner
   loop even when the request is in pre-flight (connection establishment,
   DNS, header wait). Same goroutine; no goroutine leak.

### Per-entry budget vs per-request budget

The budget is **per entry**, not per request. The outer pool-failover budget
(`maxRetryCredentials`) and the parent context deadline remain orthogonal —
total request time is bounded by the outer parent context.

### Cancellation safety

- Every `subCtx` lifetime is bounded by `entryDeadline` or by the outer parent
  context, whichever fires first.
- All `defer cancel()` follow the AGENTS.md rule for wrapping defer errors.
- A failed cancellation does not affect correctness — the next attempt's
  `subCtx` is created fresh.

### Observability

New structured-log fields on every inner-loop exit:
`entry_retry_attempt`, `entry_retry_elapsed_ms`, `entry_retry_reason`,
`entry_retry_max_attempts`, `entry_retry_max_time_ms`.

The `budget_exhausted` reason feeds the alerts sweep via existing
`usage_errors` table — no new alert type. Operators can correlate a
consistently-blowing budget with `max_time_ms` set too low or an unhealthy
provider.

### Concurrent requests on the same entry

The cooldown map already serializes per-(auth, model) failure recording. The
per-entry budget is per-request, so concurrent requests each compute their
own `entryDeadline`; no shared mutable state, no locking required on the
budget.

## 3. Picker — LIVE filter + incremental priority on pin

### Scope

The model-routing picker (`web/dashboard/src/pages/model-routing/`). This
section does not touch the upstream-provider editor entries — that's covered
in section 4.

### Server-side LIVE definition

New helper `isProviderRowLive(row, authManager)` in
`internal/api/handlers/management/routing_models.go` and a mirrored client
helper `providerKeyIsLive` in `web/dashboard/src/api/liveStatus.js`.
Both wrap the same rule:

**Live evidence** (any one is enough):
- Bare executor channel reports live for the key in the runtime registry.
- Compound row key has a recent successful response in the runtime cache
  (TTL 60s, refreshed by alerts sweep).
- Compound row key is **not** in cooldown (`authManager.CooldownStateSnapshot()`
  returns `nil` for it).

**NOT live** = `pool_breaker_open` (G3) OR cooldown active OR no live evidence.

### New endpoints

`GET /v0/management/model-routing/picker?model=X`

Response:

```json
{
  "model": "gpt-4o",
  "live": [
    {"provider_key": "openai:42:sk-liveabc", "name": "OpenAI prod",
     "suggested_priority": 11, "models": ["gpt-4o", "gpt-4o-mini"]}
  ],
  "pinned": [
    {"provider_key": "openai:42:sk-livexyz", "name": "OpenAI prod A",
     "priority": 10, "is_live": true, "cooldown_until": null}
  ],
  "stale": [ /* cooldown or breaker_open, still pinnable */ ]
}
```

`POST /v0/management/model-routing/pin`

Body: `{model, provider_key, force: false}`
Server-side priority arithmetic, atomic:

```sql
INSERT INTO model_routing_entries (model, provider_key, priority, created_at)
SELECT $1, $2,
       COALESCE((SELECT MAX(priority) FROM model_routing_entries WHERE model = $1), 9) + 1,
       NOW()
ON CONFLICT (model, provider_key) DO NOTHING;
```

The default starting priority is **10** (the COALESCE fallback `9 + 1`).
The server is the single source of truth — eliminating the race where two
operators click "Pin" at once and both pick `11`.

Response: `{priority, entry_id, was_existing}`.

### Client preview

The picker previews `suggested_priority` per candidate row (from the GET
response), but the server's POST is authoritative. If the preview is stale
because another operator pinned in between, the server's response shows the
final value.

### UI structure

Four-quadrant layout (default "Compact" view):

| Section | Contents |
|---|---|
| Pinned | Existing pins with `priority`, live dot, "Unpin" |
| Live | Candidates with "Pin → priority" button (preview value) |
| Stale | Cooldown / breaker_open with "Pin anyway" button |
| Hidden | Collapsed by default; "Show all (incl. stale)" toggle |

When LIVE status flips mid-session, the row animates from Live → Stale (or
back) with a tooltip explaining the reason. Cooldown timer counts down in the
dot's tooltip.

## 4. Upstream provider UI/UX cleanup

### Audit findings

- The provider list is list-only; entry editing lives at `/upstream-providers/:id`.
- Editor tabs are "Basic / Auth / Proxy / Pool / Advanced" — adding retry
  fields would push it to 5+ tabs.
- Status badges (LIVE/STALE/COOLDOWN/BREAKER_OPEN) only appear in the alerts
  page and the routing picker — not on the provider list.
- Operators managing 50+ providers with 5+ entries each see entry fields
  scattered across tabs.

### Redesign principles

1. **One dense row per entry.** Columns: `key preview · live dot ·
   last_used · cooldown_until · retry_attempts/used · priority · actions`.
   Replaces the multi-tab stack for entry-level fields.
2. **Provider-level fields** (name, base URL, type) stay at the top of the
   editor page.
3. **Live dot is the heartbeat.** Reuses the `providerKeyIsLive` helper from
   section 3.
4. **Retry fields inline.** Two columns on the entry row: `retry_max_attempts`,
   `retry_max_time_ms`. Default state is "use global default" — the row is
   uncluttered for the 95% case.
5. **Bulk actions.** Multi-select on the list + "Pin all LIVE to model X" /
   "Set retry budget on selected".
6. **Search & filter.** Filter by LIVE/COOLDOWN/STALE; search by key preview,
   model, proxy pool.
7. **Sticky footer.** Save button in a sticky footer; shows
   "Saved · Ns ago" or "Unsaved changes" live.

### Components

- New `web/dashboard/src/pages/upstream-providers/components/EntryRow.jsx`
  — subscribes to the live-status poller.
- New `web/dashboard/src/pages/upstream-providers/components/StatusDot.jsx`
  — single source of truth for the LIVE/COOLDOWN/STALE/BREAKER_OPEN colors.
  Reuses Tailwind tokens `emerald-500` / `amber-500` / `rose-500` /
  `zinc-400`. No new color tokens.
- New `web/dashboard/src/pages/upstream-providers/components/BulkActions.jsx`.
- Modified `web/dashboard/src/pages/upstream-providers/UpstreamProvidersPage.jsx`
  — adds filter bar, search input, sticky footer, multi-select column.

### Live-status poller

`GET /v0/management/upstream-providers/live-status`

Response: `{[provider_key]: {live: bool, reason: string, cooldown_until: ts}}`.

Polling cadence:
- 15s when the tab is visible (`document.visibilityState === 'visible'`).
- 60s when hidden.
- Pauses entirely when no provider rows are mounted.

Replaces the current per-component polls — each surface polls something
different today, which is why the UI feels inconsistent.

### Migration / how to ship without breakage

- The compact entry row goes behind a feature flag
  (`VITE_FEATURE_COMPACT_ENTRY_ROW`) for one release.
- Once parity tests pass (see Testing), flip the default.
- Editor tabs are **not** removed — "Advanced (tabs)" stays as a fallback for
  power users.
- Same pattern that worked for the provider editor page move
  (`feat/provider-editor-page` → `fe7668f4`, per memory).

### Information density rationale

Operators managing 50+ providers with 5+ entries each (common in the proxy-pool
world) need "is this alive + how is it configured" at a glance. The current
modal-tabs approach is fine for the first three providers; beyond that, it
is the source of the "kurang rapih" pain.

## Error handling

- The retry budget is additive: a misbehaving helper (panic, misused context)
  must not break selection. The inner loop wraps every helper call in
  `defer recover()` that logs and converts to `budget_exhausted` — same
  pattern as `isRequestInvalidError` callers.
- Bounded wait always terminates: budget check precedes every wait; wait
  deadline is `min(earliest deadline, budget)`.
- `priority` arithmetic on the server is atomic via SQL — no partial state on
  race.
- Live-status poller failures are non-fatal: the dashboard renders the last
  known state and shows a "Stale data" badge after 90s of failed polls.

## Testing

### Unit (Go)

- `internal/runtime/executor/helps/retry_budget_test.go` — budget math,
  deadline propagation, parent-ctx-cancel interaction.
- `internal/runtime/executor/helps/retry_classify_test.go` — table-driven
  classifier (status codes, network errors, headers).
- `internal/runtime/executor/conductor_execution_test.go` — inner loop
  reasons, streaming boundary, cooldown interaction.
- `internal/api/handlers/management/routing_models_test.go` — LIVE filter,
  priority arithmetic atomicity, picker payload shape.

### Unit (React)

- `web/dashboard/src/pages/model-routing/Picker.test.jsx` — LIVE filter,
  priority suggestion preview, stale-pinning flow, four-quadrant layout.
- `web/dashboard/src/pages/upstream-providers/components/EntryRow.test.jsx`
  — render variants, live-dot transitions, retry-field defaults.
- `web/dashboard/src/api/liveStatus.test.js` — `providerKeyIsLive` truth
  table.

### Integration (`test/`)

- Bounded-wait E2E (transient 5xx → server-side success within
  `max_time_ms`).
- Bounded-wait timeout E2E (slow upstream → `budget_exhausted` → next entry
  succeeds).
- Priority arithmetic E2E (two concurrent pins → server picks distinct
  priorities 10 and 11).
- Live-status poller E2E (provider cooldown → provider-list dot transitions).

### Canonical checks

- Strategy normalization round-trips at the three boundaries (DTO, renderer
  read, seed round-trip).
- `gofmt`; compile verification
  `go build -o test-output ./cmd/server && rm test-output`.
- Pre-existing baseline: 3 Claude header-fingerprint tests in
  `internal/runtime/executor` and `TestInPlaceByteWritesAreReviewed`
  (`internal/util`) are known pre-existing failures and not regressions of
  this change (per memory `upstream-pool-routing-strategy`).

## Files touched

### Go

- `internal/config/config_types.go` — `RetryConfig`, `RoutingConfig.Retry`.
- `internal/config/strategy.go` — default retry-on list canonicalization.
- `internal/runtime/executor/conductor_execution.go` — inner retry loop,
  inner-loop exit reasons.
- `internal/runtime/executor/helps/retry_budget.go` (new) — `EntryBudget`,
  `ExitReason`, `NextBackoff`.
- `internal/runtime/executor/helps/retry_classify.go` (new) — `IsRetryable`.
- `internal/runtime/executor/helps/retry_budget_test.go` (new).
- `internal/runtime/executor/helps/retry_classify_test.go` (new).
- `internal/api/handlers/management/routing_models.go` (new) —
  `GET /picker`, `POST /pin`, `isProviderRowLive`.
- `internal/api/handlers/management/upstream_providers_live_status.go`
  (new) — `GET /live-status`.
- `internal/store/pg_normalized_import.go` — entry column projection for
  `retry_max_attempts`, `retry_max_time_ms`, `retry_backoff_ms`.
- `internal/configsnapshot/` — YAML round-trip for the new fields.
- `internal/store/pg_migrations/<ts>_retry_columns.sql` (new) — `ALTER TABLE
  api_key_entries ADD COLUMN retry_max_attempts smallint NULL, ...`.

### React

- `web/dashboard/src/api/modelRouting.js` (new) — `getPicker`, `pinProvider`.
- `web/dashboard/src/api/liveStatus.js` (new) — `providerKeyIsLive`,
  `useLiveStatus` (TanStack Query hook).
- `web/dashboard/src/pages/model-routing/Picker.jsx` (new) — four-quadrant
  layout, pin preview, stale pinning.
- `web/dashboard/src/pages/model-routing/Picker.test.jsx` (new).
- `web/dashboard/src/pages/upstream-providers/components/EntryRow.jsx`
  (new).
- `web/dashboard/src/pages/upstream-providers/components/EntryRow.test.jsx`
  (new).
- `web/dashboard/src/pages/upstream-providers/components/StatusDot.jsx`
  (new).
- `web/dashboard/src/pages/upstream-providers/components/BulkActions.jsx`
  (new).
- `web/dashboard/src/pages/upstream-providers/UpstreamProvidersPage.jsx`
  — filter bar, search, sticky footer, multi-select.
- `web/dashboard/src/pages/upstream-provider-editor/EntriesEditor.jsx` —
  feature-flagged integration with `EntryRow`.

### Documentation

- `web/dashboard/src/api/developerDocs.js` — add the two new endpoints.
- `docs/plans/2026-09-11-omniroute-incremental-routing-design.md` — add a
  note that G5's cooldown_wait budget is reused by section 1's per-entry
  default; this design supersedes the per-attempt retry portion of G5.

## References

- OmniRoute `docs/routing/AUTO-COMBO.md`, `docs/architecture/RESILIENCE_GUIDE.md`.
- `docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md`.
- `docs/plans/2026-09-11-omniroute-incremental-routing-design.md`.
- `docs/plans/2026-09-06-proxy-pools-design.md`.
- Memory: `upstream-pool-routing-strategy`,
  `routing-picker-key-alignment`, `proxy-pools-design-status`,
  `pg-first-control-plane-status`, `auto-router-f1-scorer-v2`.