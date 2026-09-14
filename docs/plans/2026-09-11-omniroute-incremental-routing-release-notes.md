# OmniRoute-Inspired Incremental Routing Improvements — Release Notes

Date: 2026-09-14
Branch: main (already landed through commit `1522ed58`)
Scope: 5 routing-resilience improvements inspired by OmniRoute's combo/routing design — no new layers, no API breaking changes.

## Summary

Five incremental routing improvements land together, all attached to existing paths
in `sdk/cliproxy/auth` and the canonical PG-row → render → synthesizer → auth-attribute
chain. No new components; existing tests + new tests cover behavior. Two minor
backward-compat effects are documented below.

## What Changes

### G2 — Success-decay for per-model failure state (`FailureCount`)

Files: `sdk/cliproxy/auth/types.go`, `sdk/cliproxy/auth/conductor_cooldown.go`
+ tests `model_state_decay_test.go`.

Before: a model that repeatedly 5xx'd would fully reset to "healthy" on the first
success — and immediately re-enter the failed-cooldown state on the next failure.
Operators saw flapping models never recover gracefully.

After: `ModelState.FailureCount` increments on every failure and halves on every
success. The first success clears the count; subsequent successes stage recovery by
halving the residual cooldown. Residual is capped at the transient error window
(1 minute) so long-window classes (401/403 = 30 min, model-not-supported = 12 h)
don't keep a just-verified-working credential out of rotation.

Operational impact: dashboard may briefly show `Status = StatusActive` between
failures and full cooldown-clear, which is the intended behavior.

### G3 — Opt-in pool-level circuit breaker

Files: `sdk/cliproxy/auth/pool_breaker.go` (new), wiring in
`conductor_cooldown.go`/`selector.go`/`cooldown_providers.go`,
PG column `upstream_providers.circuit_breaker boolean NOT NULL DEFAULT FALSE`,
renderer/seed/synthesizer plumbing across Claude / OpenAI-compat / OpenCodeGo,
dashboard form retention.

Before: a row with 20 keys all hitting 502 produced 20 independent per-auth
cooldowns with no pool-wide signal. The pool kept wasting picks on auths that
shared a broken provider.

After: rows can opt into a pool-level circuit breaker via the `circuit_breaker`
config field. Only 408/5xx failures feed the breaker; 401/403/429 stay on the
per-auth cooldown ladder. Threshold: 8 failures in a sliding 60 s window → OPEN.
Lazy HALF_OPEN transition admits one probe; on probe success the breaker closes
and counter resets; on probe failure it reopens with doubled reset period (cap
5 min). In-memory only — restart clears every pool's state.

Observability: `PoolBreakerSnapshot` surfaces open/half-open/closed states through
the existing `GET /v0/management/cooldown-providers` endpoint (new
`pool_breakers` field). No new endpoint.

Migration: column added with `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` and
default `FALSE`; existing rows are opted out automatically.

Known scope: home-mode (`HomeEnabled`) non-stream dispatches bypass the breaker
feed (documented gap in plan; planned follow-up).

### G4 — `power-of-two-choices` and `least-used` strategies (real picks)

Files: `sdk/cliproxy/auth/scheduler.go`, strategy enum + helpers, route-metadata
mapping in `conductor_execution.go`, pinned-route strategy in `handlers_routing.go`,
service_config.go mapping, dashboard hint clarification; new test file
`scheduler_strategy_p2c_test.go` (13 cases).

Before: only fill-first / round-robin / weighted-round-robin strategies.
Operators wanting in-flight-aware load balancing had no built-in option.

After: two new canonical strategies, `power-of-two-choices` and `least-used`,
honoring all four aliases each (`p2c` / `two-random-choices` / `poweroftwochoices`,
`least-busy` / `leastused`). Strict validation rejects unknown values with a
descriptive error listing every canonical value. The active scheduler picks via
`schedulerStrategyP2C` / `schedulerStrategyLeastUsed` — p2c samples two distinct
predicate-matching candidates and returns the lower in-flight; least-used
scans for the minimum in-flight count and rotates tied candidates via the
ready-view cursor.

The implementation relies on a per-credential `inFlight` counter on
`authScheduler` (added in the prerequisite commit `1bbaa2da`); that counter is
acquired immediately after pool-breaker admission in non-stream, count-token,
and stream execution loops and released at rotation / drain completion.

Backward-compat: dashboard options are exposed immediately, so operators who
upgrade will see the new strategies in their routing-strategy picker. Picker
hint clarifies the deterministic interim behavior (now real) and mentions
in-flight awareness.

### G5 — Bounded server-side cooldown wait

Files: `internal/config/config_types.go` (new `CooldownWaitConfig`),
`config.example.yaml` (commented example), atomic knobs in
`sdk/cliproxy/auth/conductor_cooldown.go`, wiring in
`sdk/cliproxy/service_config.go` + `internal/api/server.go` + `server_reload.go`,
diff entry in `internal/watcher/diff/config_diff.go`,
test `cooldown_wait_budget_test.go`.

Before: the existing `max_retry_interval` ceiling was the only cap on how long
a single request could wait on cooldowns.

After: a new `routing.cooldown_wait.{max_wait_ms, max_attempts, reclassify_403}`
sub-config (defaults: 15000 ms / 3 attempts / false) tightens (never loosens) the
existing retry ceiling. The budget is applied per-wait in
`shouldRetryAfterError` and shares the same retry-after / 429 surface as
hard-skip classes (401/402/404). Streaming waits only before the first byte.

**Behavioral change for operators who don't set `routing.cooldown_wait`**: the
default 15 s ceiling is tighter than the 30 s ceiling from
`max-retry-interval: 30` in `config.example.yaml`. Cooldown waits of 15–30 s
that previously succeeded now fail fast with 429 + `Retry-After`. This is
intentional (per design G5) but worth flagging. At apply time the server logs
an info line when the budget clamps `max-retry-interval`:

```
routing cooldown_wait tightens max-retry-interval from 30s to 15s
```

If operators want to preserve the previous behavior, set
`routing.cooldown_wait.max_wait_ms` to at least the previous
`max-retry-interval` value.

### G6 — Narrow 403 → 429 reclassification

Files: `sdk/cliproxy/auth/conductor_cooldown.go` + test
`mark_result_quota403_reclass_test.go`.

Before: a 403 upstream answer always became a 30-minute unauthorized cooldown,
even when the response body clearly indicated a quota / rate-limit condition.
Operators saw their credentials stuck in 30-minute cooldown for what was really a
short-window quota exhaustion.

After: an opt-in `routing.cooldown_wait.reclassify_403` gate (default `false`)
sniffs 403 response bodies for narrow pattern matches — `quota`,
`rate limit`, `exceeded`, `resource_exhausted` (case-insensitive). A match
reclassifies the error as a 429 and runs it through the existing quota cooldown
ladder (1 s base, doubling, max 30 min, honoring upstream `Retry-After`).
The `billing` keyword explicitly excludes billing-class 403s, which stay in
the 402-class cooldown.

Default off. Operators enable per-environment only when they trust their
upstream providers to use 403 for genuine quota exhaustion.

## Operator Migration Checklist

1. **No config change required** to keep the existing behavior on G2 / G3 / G4 / G6.
2. **G5 has a default-tightening effect**: review `routing.max-retry-interval`
   in your config; if it is `30s` or higher, either accept the new 15 s wait
   ceiling or set `routing.cooldown_wait.max_wait_ms: 30000` explicitly.
3. **Enable G3 breaker per row** by setting `circuit_breaker: true` on
   `upstream_providers` rows you want protected. No global flag.
4. **Try G4 strategies** with one model first — change the row's
   `routing_strategy` to `power-of-two-choices` or `least-used`. The
   dashboard will preserve the flag through edits.
5. **Enable G6** only if you know your upstreams use 403 for quota conditions;
   leave it off otherwise.

## Validation

- `gofmt -l` clean on every changed package
- `go vet ./sdk/cliproxy/... ./internal/watcher/... ./internal/upstreamsync/...
  ./internal/store/... ./internal/api/handlers/management/... ./internal/config/...
  ./sdk/api/handlers/...` clean
- `go test -count=1 ./sdk/cliproxy/auth/...` (auth package including new
  scheduler strategy tests, model-state decay tests, pool-breaker wiring and
  commit-guard tests, cooldown-wait budget tests, quota-403 reclassification
  tests) passes
- `go test -count=1 ./internal/watcher/... ./internal/upstreamsync/...
  ./internal/store/... ./internal/api/handlers/management/... ./internal/config/...`
  passes — covers PG migration, render/seed, synthesizer attribute stamping
- `go test -count=1 -race ./sdk/cliproxy/auth/...` passes for all breaker
  wiring, scheduler strategy, in-flight tracking, and disable-cooling tests

## Known Scope / Follow-ups

- **Home-mode breaker feed**: home-dispatch path (`executeHome`, antigravity
  credits fallback) currently does not feed `recordFailure` / `recordSuccess` to
  the pool breaker. Tracking issue should be filed.
- **Auto-router strategy stash**: `applyAutoRouterRoute` stashes only
  `"priority"`/`"failover"`; auto-router tiers don't propagate the new p2c /
  least-used strategies. Intentional but worth documenting.
- **Cumulative cooldown-wait budget**: current budget is per-wait; worst-case
  total server-side wait is `max_attempts × max_wait_ms`, not `max_wait_ms`.
  True cumulative budgeting would need per-request state threaded through the
  Execute loops.
- **Dashboard pool breaker toggle**: the management API exposes `circuit_breaker`
  per row but the dashboard form currently preserves rather than sets the flag.
  Out-of-scope; management API only.

## Files Touched

`config.example.yaml`,
`internal/config/{config_types,strategy}.go` and tests,
`internal/store/pg_upstream_providers.go` and tests,
`internal/store/postgresstore.go`,
`internal/upstreamsync/{render,seed}.go` and tests,
`internal/watcher/{diff,scheduler/synthesizer}/...`,
`internal/api/handlers/management/{upstream_providers,cooldown_providers,config_basic}.go`,
`sdk/cliproxy/{auth,service_config}.go` and tests,
`sdk/api/handlers/{handlers_routing}.go`,
`web/dashboard/src/pages/upstream-provider-editor/{form,editor.test}.js`,
`web/dashboard/src/pages/upstream-provider-editor/schemas.js`,
`docs/plans/2026-09-11-omniroute-incremental-routing-{design,plan}.md`,
`docs/plans/2026-09-11-omniroute-incremental-routing-release-notes.md` (this file).
