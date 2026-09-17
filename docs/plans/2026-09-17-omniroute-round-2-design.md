# OmniRoute Round 2 — Strategies, Provider Quota-Share, Live Events Feed

Date: 2026-09-17
Status: Validated (brainstorming complete, not yet implemented)

## Context

Reference study: OmniRoute (github.com/diegosouzapw/OmniRoute) combo engine, free-tier observability, and decision-header patterns. Continues the incremental-improvement arc established by the round-1 doc:

- Round-1 doc: `docs/plans/2026-09-11-omniroute-incremental-routing-design.md`
- Round-1 shipped (no new work — referenced, not re-litigated): success-decay (G2), pool-level circuit breaker (G3), `power-of-two-choices` + `least-used` strategies (G4), bounded cooldown wait (G5), 403→429 reclassifier (G6).

## Round 2 in scope

1. **Routing strategy** — three new selectors: `fill-first`, `weighted`, `headroom`. Plus `X-NixLLM-Decision` response header carrying the routing decision (chosen auth, strategy applied, breaker state, cooldown waits).
2. **Upstream provider management** — quota-share accounting: live used/remaining per auth+pool surfaced from `usage_windows`, exposed at `/v0/management/auths/:id/quota` and rendered on a `/dashboard/quota` panel. No schema change — pure read aggregation over existing tables.
3. **Recent events / logs** — wire the existing logs-page design (commit `1a786734`) to a live `/v0/management/events` endpoint backed by a bounded in-memory ring buffer of structured events.

## Out of scope (round-3 candidates)

- Cost-optimized strategy (no registry pricing data — same blocker that killed round-1 G1).
- Work-conserving scheduler (overlaps shipped G4 fairness).
- Pricing taxonomy `$0` subs vs per-token.
- Persistent (PG-backed) event store — deferred until ring-buffer retention proves insufficient.

## Architecture placement

No new layer.

- **Routing** selectors attach to the existing scheduler pick path (same place as round-1 G4).
- **`X-NixLLM-Decision`** built in the conductor `Execute*` return path (and on first stream-chunk write).
- **Quota endpoints** read-only aggregation over `usage_windows`. Live in `internal/api/handlers/management/`.
- **Events** new package `internal/events/` with one global recorder. Emission sites call `events.Global().Record(...)`.
- **Dashboard** mounts under existing `web/dashboard/` SPA, follows developer-docs endpoint catalog pattern from `web/dashboard/src/api/developerDocs.js`.

Data flow stays canonical: PG row → render → auth attribute → conductor → event record → dashboard / response header.

---

## Workstream 1 — Routing: new selectors

### `fill-first`

- **Semantics**: among ready candidates in the active pool, pick the auth whose in-flight count is **highest** below its `max_parallel_requests` cap. Keeps one auth busy to the brim before touching the next — minimizes distinct upstream connections and warms any per-key caches.
- **Implementation**: linear scan of the ready bucket. Tie-break by entry priority then auth stable id (deterministic). O(n), bucket small.
- **Failure mode**: if the chosen auth trips its cap mid-request, `MarkResult` already re-routes. No new state.

### `weighted`

- **Semantics**: existing `priority` is a strict total order. `weighted` instead treats each entry's `weight` field as a sampling weight (default 1) within the same priority bucket.
- **New config**: per-entry `weight` field on `UpstreamProviderEntry` (PG column add, renderer read, default 1). Pure additive schema change.
- **Implementation**: precompute weight-sum per priority bucket at render time; pick via weighted reservoir using prefix sums — O(log n).

### `headroom`

- **Semantics**: among ready candidates, prefer the one with the **most** remaining quota, where quota = `limit - used` from the auth's per-window counters in `usage_windows`. Tie-break by priority. Best-effort ordering, not enforcement.
- **Fallback**: when all candidates are at or below 0 headroom, `headroom` falls back to `least-used` ordering so the request still goes somewhere, with `X-NixLLM-Decision: headroom=exhausted,fallback=least-used` marker.
- **Stale data risk**: `usage_windows` is eventually consistent (writes async). Acceptable because `headroom` is best-effort; strict enforcement remains `policy.WindowFor` 429s.

### Canonicalization

Add the three to `internal/config/strategy.go` alongside round-1 entries. Same DTO/renderer/seed round-trip validation. Aliases: `fill-first`/`ff`, `weighted`/`w`, `headroom`/`hr`.

### Architecture placement

All three live in the scheduler pick path. No new layer.

---

## Workstream 1 — Routing: `X-NixLLM-Decision` header

### Header shape

```
X-NixLLM-Decision: v=1;
  model=<resolved-model>;
  auth=<auth-id>;
  channel=<executor-channel>;
  strategy=<canonical-strategy>;
  breaker=<closed|open|half-open|n/a>;
  cooldown_wait_ms=<int>;
  attempts=<int>;
  pool_strategy=<attr|fallback|compound|n/a>;
  quota_headroom=<pct|n/a>
```

- `v=1` — version prefix for forward compatibility.
- Semicolon-delimited `key=value` pairs; URL-safe, no spaces. Header stays under ~1 KB worst case.

### Generation point

Built in the conductor's `Execute*` return path, after the chosen auth finishes its first successful read. Stream variants build it on first chunk write so streaming clients see the decision as a header before the body.

### Visibility

Always on, no opt-in. Free observability, the same way `X-Request-ID` is. Surfaced in the response header, the dashboard per-request detail drawer (correlates with the event feed), and log lines (`decision=...` appended to the existing structured log at the same call site).

### Privacy

No secrets, no request body, no user tokens. Just routing metadata. Compatible with the project's "avoid leaking secrets/tokens in logs" rule.

### Failure cases

- If the response header set fails (headers already sent, oversized value): log a warning with the proposed value truncated to 200 chars, continue. Never let header writing break the response.
- If a field is unknown (no breaker state because pool isn't opted in): emit `n/a`. Don't omit — clients can rely on field presence.

---

## Workstream 2 — Upstream provider management: quota-share accounting

### Data source

`usage_windows` (already written by the existing usage accounting path, already consumed by alerts + policy). One row per `(auth_id, model, window_size)`. Read with a single aggregation query per dashboard panel refresh.

### New endpoints

- **`GET /v0/management/auths/:id/quota`** — returns
  ```json
  {
    "auth_id": "...",
    "channel": "openai",
    "pool_strategy": "fallback",
    "windows": [
      {"size": "1m", "used": 12345, "limit": 1000000, "headroom_pct": 98.8},
      {"size": "1h", "used": 200000, "limit": 5000000, "headroom_pct": 96.0},
      {"size": "1d", "used": 1500000, "limit": 50000000, "headroom_pct": 97.0}
    ],
    "models": [
      {"model": "gpt-5", "used_1h": 80000, "limit_1h": 1000000}
    ]
  }
  ```
- **`GET /v0/management/pools/:channel:rowID/quota`** — same shape, summed across the pool's auths. The "pool" is identified by the compound key already used by `PoolStrategyForProviderKeys` and the round-1 `PoolBreaker`.

### `headroom_pct` semantics

`(limit - used) / limit * 100`, rounded to 1 decimal. Negative values (over-limit, possible when async writes lag) are surfaced as `0.0` with a separate `over_limit: true` flag.

### Dashboard

New panel at `/dashboard/quota` (mounted in the existing dashboard SPA, follows the developer-docs endpoint-catalog pattern from `web/dashboard/src/api/developerDocs.js`):

- Top section: pool cards with per-window headroom bar (one bar per window size).
- Drill-down to per-auth and per-model within a pool.
- Auto-refresh every 10 s (existing dashboard refresh primitive — same as alerts panel).

### Architecture placement

Read-only aggregation. No new ingestion path. No new table. The endpoints sit in `internal/api/handlers/management/` alongside `runtime_config.go` and use the same PG-first pattern (return 503 without `PGSTORE_DSN`).

### What this *doesn't* do (deliberate)

- No quota-based selection policy changes — `headroom` selector reads the same data lazily.
- No quota enforcement changes — `policy.WindowFor` 429s remain the source of truth.
- No new pricing fields — cost-optimized stays out per round-1 G1 reasoning.

---

## Workstream 3 — Recent events / logs: live event feed

### Event types (round-2 set)

| Type | Source | When emitted |
|---|---|---|
| `routing.decision` | conductor `Execute*` return path | every successful dispatch |
| `routing.cooldown_wait` | `waitForCooldown` | when a wait actually happened (not on immediate success) |
| `routing.attempts_exhausted` | conductor retry loop | when `max_attempts` is hit |
| `breaker.tripped` | `PoolBreaker.reportFailure` | state transition CLOSED → OPEN |
| `breaker.probe_ok` / `breaker.probe_fail` | `admitProbe` | HALF_OPEN probe verdict |
| `cooldown.reclassified` | `MarkResult` | when 403→429 reclassifier (G6) matched |
| `quota.threshold_cross` | usage writer | when an auth crosses 80% / 95% / 100% of a window — *separate from existing alerts; this is the audit log* |

### Storage: bounded in-memory ring buffer

```go
// internal/events/ring.go
type Ring struct {
    mu     sync.Mutex
    buf    []Event
    cap    int        // default 5000, configurable
    cursor int        // next write index
    full   bool
}
```

- One ring per process. Cap configurable via `events.ring_capacity` in `RoutingConfig` (default 5000; minimum 100; maximum 50000).
- Bounded by **count**, not bytes — predictable memory, simple eviction.
- Events are structured: `type, ts, request_id, model, auth_id, channel, payload (json.RawMessage)`.

### Endpoint

- **`GET /v0/management/events?type=...&auth=...&since=<rfc3339>&limit=<int>`** — newest-first, capped at 500 per page (cursor = oldest event ts returned).
- **`GET /v0/management/events/stream`** — Server-Sent Events, append-only, drains new events as they arrive. SSE chosen over WebSocket because it's one-way (server→client) and simpler to filter on the dashboard side.
- Returns 503 without `PGSTORE_DSN` to match the existing management-route pattern — even though no PG queries are involved, the gates stay consistent.

### Event emission

- Single `internal/events/Recorder` interface (`Record(Event)`).
- Each emission site calls `events.Global().Record(...)`. Recorder is a no-op stub in tests; ring-backed in production.
- Writer is non-blocking: drops events if the mutex is contended beyond a 1 ms threshold, increments an `events.dropped` counter exposed at `/v0/management/events/stats`. **Never** let event recording backpressure request handling.

### Dashboard wiring

- Existing logs page (commit `1a786734`) gets a new "Live" tab alongside the static log view.
- Live tab uses SSE; falls back to 10 s polling if EventSource fails.
- Filter chips at the top mirror the `?type=`, `?auth=` query params.

### Persistence

In-memory only, deliberate. Survives only as long as the process. Round-3 candidate: PG-backed persistent event store with downsampling, gated on operators reporting ring-buffer retention is too short.

---

## Error handling

- **`X-NixLLM-Decision`** header: if header write fails (headers already sent, oversized value), log a warning with the proposed value truncated to 200 chars, continue. Never break the response.
- **`fill-first` / `weighted` / `headroom`**: each must terminate. `fill-first` and `headroom` are O(n) scans; empty bucket returns the existing "no candidates" error. `weighted`'s prefix-sum draw is constant time once built; rebuild only on render.
- **Quota endpoints**: return 503 without `PGSTORE_DSN`. Cap query latency at 2 s; on timeout return 200 with `partial: true` and whatever windows came back.
- **Event recorder**: 1 ms contention threshold is per-write, not per-buffer. Mutex never held across a network call. Drops increment `events.dropped`.
- **SSE stream**: client disconnect drops the server goroutine within 5 s of the disconnect (existing `internal/wsrelay` pattern adapted for SSE). No goroutine leaks on dashboard tab close.

## Testing

- **Unit (per workstream)**:
  - Routing: `fill-first` ordering under mixed in-flight; `weighted` reservoir distribution correctness (chi-square over 10k draws); `headroom` fallback to `least-used` on exhaustion; canonicalization round-trips at DTO/renderer/seed for the three new strategies.
  - Header: version-prefix present, field ordering stable, `n/a` placeholders correct, oversized-value truncation logged.
  - Quota endpoints: `headroom_pct` clamping math, `over_limit` flag, 2 s timeout behavior.
  - Events: ring buffer wraparound, dropped-counter increment under mutex contention (mock mutex), query-param filtering, SSE end-to-end via httptest.
- **Integration (`test/`)**: end-to-end dispatch emitting `routing.decision` visible via `/v0/management/events?type=routing.decision`; pool breaker trip emits `breaker.tripped` + matching `routing.decision` showing `breaker=open`.
- **Canonical checks**: `gofmt -w .`; `go build -o test-output ./cmd/server && rm test-output`.

## Open questions for later (not blockers, recorded for round-3 planning)

1. **Persistent event store** — when (if ever) is the ring buffer too short? Operators can watch `events.dropped` and `events.cap`.
2. **Pricing taxonomy** — once the registry has pricing fields, cost-optimized becomes viable.
3. **Work-conserving scheduler** — overlaps round-1 G4 fairness; revisit only if observed starvation emerges in production telemetry.
4. **Subscription-tier $0 cost reporting** — needs the pricing taxonomy above first.

## References

- OmniRoute `docs/routing/AUTO-COMBO.md`, `docs/architecture/RESILIENCE_GUIDE.md`
- Round-1 doc: `docs/plans/2026-09-11-omniroute-incremental-routing-design.md`
- Pool routing strategy: `docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md`
- Auto router analysis: `docs/plans/...-auto-router-enhancements-design.md` (for SSE/dashboard patterns)
- Logs page design: commit `1a786734` (existing dashboard logs page)
- Developer-docs endpoint catalog: `web/dashboard/src/api/developerDocs.js`

## Implementation sequencing (for the implementation plan)

Roughly four commits, each independently revertable:

1. `feat(strategy): fill-first, weighted, headroom selectors + X-NixLLM-Decision header`
2. `feat(management): quota-share read endpoints + /dashboard/quota panel`
3. `feat(events): in-memory ring recorder + /v0/management/events endpoint + SSE`
4. `feat(dashboard): live events tab wired to SSE`
