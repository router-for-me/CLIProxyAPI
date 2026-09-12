# Incremental Routing Improvements (OmniRoute Reference) — Design

Date: 2026-09-11
Status: Validated (brainstorming complete, not yet implemented)

## Context

Reference study: OmniRoute (github.com/diegosouzapw/OmniRoute) combo feature and
resilience design. Goal: incremental improvements to NixLLM's existing routing —
focused on **upstream proxy pools (entry routing)** and **server-side auto retry** —
not a new combo layer.

What NixLLM already has (no change needed):
- Aggressive in-pool failover, per-entry priority, failed-pool pinning
  (`docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md`).
- Per-auth+model cooldown with exponential backoff honoring `Retry-After`
  (`sdk/cliproxy/auth/conductor_cooldown.go`).
- Cooldown-aware execution loops (`waitForCooldown`, `tried` map,
  `maxRetryCredentials`).
- Recovery-deadline surfacing as 429 + `Retry-After` (commit de9336c1).

Scope (gap IDs from the brainstorm):
- **G2** — success-decay for per-model failure state
- **G3** — pool-level circuit breaker (opt-in)
- **G4** — new strategies: `power-of-two-choices`, `least-used`
- **G5** — bounded cooldown wait (server-side retry budget)
- **G6** — narrow 403→429 reclassification (opt-in)

Out of scope (dropped during brainstorm): cost-optimized strategy (G1 — needs
pricing data in registry), request admission queue (folded into G5),
`random`/`strict-random` strategies, fusion/pipeline semantics.

## Architecture placement

No new layer. All four mechanisms attach to existing paths:

1. **G2** → `Manager.MarkResult` (`conductor_cooldown.go`), success path.
2. **G3** → new type `PoolBreaker` in `sdk/cliproxy/auth/pool_breaker.go`,
   keyed by compound pool key (`channel:rowID`, same key as
   `pinPoolFromFailedAuth`), consumed by `isAuthBlockedForModel`.
3. **G4** → `internal/config/strategy.go` canonical values + scheduler pick path.
4. **G5** → existing cooldown-aware loop (`waitForCooldown` path), gated by new
   `RoutingConfig` fields. **G6** → pre-classification in `MarkResult` for 403.

Data flow stays canonical: PG row → render → auth attribute → conductor.

## G2 — Success-decay

Failure-based `ModelState` (transient 5xx, 429/404 lockout) currently recovers
only via timer expiry or full success reset. A frequently failing model can
therefore re-enter selection at full health and fail again immediately.

Change in `MarkResult` success path:
- Add/reuse a failure counter on `ModelState`.
- On success: `count = count / 2` (floor 0). `count == 0` → delete state
  (existing behavior for full-reset classes unchanged).
- If `count > 0` remains: pull `NextRetryAfter` in to half the remaining
  cooldown (floor 1s) — staged recovery instead of instant full health. The
  residual is capped at the transient cooldown window so long-window classes
  (30-min lockouts, 12h model_not_supported) don't over-block after success.

No new config; decay is always on for failure-count-based states.

## G3 — Pool-level circuit breaker (opt-in)

Problem: 408/5xx currently cooldown only the single auth+model. A row with 20
keys all hitting 502 produces 20 independent cooldowns with no pool-wide signal.

```go
// sdk/cliproxy/auth/pool_breaker.go
type PoolBreaker struct {
    mu    sync.Mutex
    pools map[string]*poolState // key: compound "channel:rowID"
}
type poolState struct {
    failures  int
    state     breakerState // CLOSED | OPEN | HALF_OPEN
    openedAt  time.Time
    lastProbe time.Time
}
```

- **Trigger**: only 408/5xx (401/403/429 belong to per-auth cooldown, never the
  breaker). Default threshold: 8 failures in a sliding 60s window → OPEN.
- **Contribution**: from `MarkResult`, only when the auth has a
  `pool_strategy` attribute AND the row opts in (`circuit_breaker: true`,
  default off).
- **Reset timeout**: default 30s → lazy transition to HALF_OPEN on
  `canExecute()` (no background timers, matching existing cooldown patterns).
  HALF_OPEN admits **one** probe (first selected auth in the pool). Probe
  success → CLOSED + counter reset; failure → OPEN again with the reset
  timeout doubled, capped at 5 min.
- **Selection integration**: `isAuthBlockedForModel` adds
  `breaker.IsOpen(poolKey)` check → skip candidates with availabilityBlock
  reason `pool_breaker_open`. Pool key resolution reuses
  `PoolStrategyForProviderKeys`.
- **Observability**: included in `CooldownStateSnapshot()` (already consumed by
  the alerts sweep) and the existing cooldown management endpoint. No new
  endpoints.
- **Persistence**: in-memory only (deliberate — burst protection, not
  operational state).

As-built note (2026-09-12): probe admission moved OUT of the selection read
path to execution commit. The original lazy-probe design (first
`isAuthBlockedForModel` caller after expiry takes the probe) never recovered:
the pick consumes the probe, then the model-filter call (`filterExecutionModels`)
sees the pool blocked and drops the request before dispatch — no probe verdict
ever arrives. As built, `blockDeadline` is read-only (only time-based
relabels: expired OPEN→HALF_OPEN, expired probe window→re-arm), and the
single-probe slot is taken by `admitProbe` at the three execution-commit sites
(Execute/ExecuteCount/ExecuteStream, after auth preparation, right before
dispatch), so an admitted probe always corresponds to a real dispatch.
`scheduler.upsertAuth` and other read paths can no longer consume probes.

## G4 — New strategies: p2c and least-used

- Canonical values `power-of-two-choices` and `least-used` in
  `internal/config/strategy.go`; aliases accepted (`p2c`, `two-random-choices`,
  `least-busy`). Strict validation at the three existing canonicalization
  boundaries (DTO, renderer read, seed round-trip).
- Implemented in the **scheduler** (the active path), not the legacy selectors:
  - **p2c**: pick 2 random ready candidates, take the lower `inFlight`.
    Reuses the existing in-flight counter (same one used by
    `max_parallel_requests` policy). O(1).
  - **least-used**: linear scan of the ready bucket for minimum `inFlight`;
    tie-break via weighted smoothing (WRR-style) to avoid mono-provider
    starvation. O(n), buckets are small.

No new data fields. New cases in `readyByPriority`/pick path only.

## G5 — Bounded cooldown wait

New config (`internal/config/config_types.go`, `RoutingConfig`):

```yaml
routing:
  cooldown_wait:
    max_wait_ms: 15000   # total wait budget per request
    max_attempts: 3      # max re-dispatches in one wait cycle
```

Semantics when all candidates for a route are cooldown-blocked:
- Shortest deadline ≤ budget: existing `waitForCooldown` waits to the earliest
  deadline, re-selects, re-dispatches (up to `max_attempts`). Transparent to
  the client.
- All deadlines > budget: surface 429 + `Retry-After` (existing
  recovery-deadline mechanism, gated by the budget).
- Hard-skip classes: 401/402/404/invalid_grant are never waited on (already
  excluded from cooldown-wait; preserved).
- `budgetMs = max_wait_ms` is the **total** budget across attempts, not per
  attempt.
- Streaming: wait happens only before the first byte is sent
  (failover-after-stream-start stays forbidden).

As-built note (2026-09): the budget is applied **per-wait**, not cumulatively —
`shouldRetryAfterError` is stateless and applies the budget independently on
every call, so the worst-case total server-side wait is
`max_attempts x max_wait_ms`, not `max_wait_ms`. True cumulative budgeting
would need per-request state threaded through the Execute/ExecuteStream loops
and is a possible follow-up.

## G6 — Narrow 403→429 reclassification (opt-in)

- Point: start of `MarkResult`, pre-classification, 403 only.
- Pattern sniff on the error body (case-insensitive whitelist): `quota`,
  `rate limit`, `exceeded`, `resource_exhausted`. `billing` explicitly
  excluded (stays 402-class). No match → remains 403 → 30 min unauthorized
  cooldown.
- Config gate: `routing.cooldown_wait.reclassify_403`, default **false**.
- Flows into alerts via existing `usage_errors` classification — no alert
  changes.

## Error handling

- Breaker is additive: a `PoolBreaker` panic/misuse must not affect selection —
  breaker calls are pure map lookups behind one mutex; keep them allocation-free
  on the hot path.
- Bounded wait must always terminate: budget check precedes every wait; wait
  deadline is `min(earliest candidate deadline, budget)`.
- Reclassifier only rewrites classification, never the response body or status
  surfaced upstream of `MarkResult`.

## Testing

- Unit tests: decay math (G2), breaker state transitions incl. probe and
  backoff cap (G3), p2c fairness + least-used tie-break (G4), reclassifier
  pattern table-test incl. `billing` exclusion and opt-in gate (G6), bounded
  wait budget/attempts accounting (G5).
- Integration (`test/`): bounded-wait E2E (short cooldown → server-side
  success; long cooldown → 429 + Retry-After).
- Canonical checks: strategy normalization round-trips at the three
  boundaries; `gofmt`; compile verification
  `go build -o test-output ./cmd/server && rm test-output`.

## References

- OmniRoute `docs/routing/AUTO-COMBO.md`, `docs/architecture/RESILIENCE_GUIDE.md`
- `docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md`
- `docs/plans/2026-08-31-routing-remediation-plan.md`
