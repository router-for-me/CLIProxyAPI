# Auto Router Reliability & Routing Latency — Design

Date: 2026-09-08
Status: Approved (brainstorming session)

## Problem statement

Two recurring production symptoms on Auto Router–routed requests, with no client-side cancel:

1. **Silent hang / stop**: the request produces nothing for a long time, then dies (client timeout or dropped connection).
2. **Empty output**: the response completes quickly but carries zero content.

Additionally, routing-time overhead has measurable waste in the hot path.

## Root causes identified (code investigation)

- **Vision bridge is a synchronous upstream call with no deadline**
  (`sdk/api/handlers/vision_bridge.go`, `runVisionBridge`). When the router has a
  `vision_bridge_model` and the request contains images, the bridge call blocks the
  request path. A hung upstream bridge = a hung client request. (Symptom 1.)
- **`inflateThinkingModelMaxTokens` only protects registered models**
  (`vision_bridge.go`). Thinking models split `max_tokens` between reasoning and
  visible content; a small client cap makes reasoning consume the whole budget →
  empty content with `finish_reason=max_tokens`. The guard only activates when the
  target model is registered with `Thinking` metadata AND `MaxCompletionTokens > 0`.
  Unregistered models are unprotected. (Symptom 2.)
- **`matched=false` falls through to the synthetic provider**
  (`handlers_auto_router.go`). When `autorouter.Resolve` fails, `executionModel`
  stays the router id; `GetProviderName` returns the synthetic `auto-router`
  provider (no auth client) → generic `auth_not_found`, or (Home mode) an unknown
  model forwarded upstream. Hard to diagnose, wrong error surface.
- **Cooldown waits are invisible**: `waitForCooldown` can hold a request up to
  `maxWait` with zero log output — "stopped without reason" was often "still
  waiting in cooldown with no trace".
- **Hot-path waste**:
  - `storeRouterToConfig` rebuilds the whole `autorouter.Config` (allocations) per
    request — even on score-cache hits.
  - Score-cache key = SHA-256 over the full body; for multi-MB agent bodies
    (esp. base64 images) hashing dominates and exceeds the cost of (windowed)
    scoring itself.
  - `normalizeKeywordText(ext.FlatText)` lowercases text that `extractRequest`
    already lowercased — a full second copy of the body per request.

## Approved approach

Targeted fixes + minimal instrumentation (Approach A). No architecture rework
(resolver unification — "option C" — was considered and rejected: risk outweighs
the remaining win after F2's compiled profiles).

### Part 1 — Vision bridge deadline (Symptom 1)

- `runVisionBridge` executes under `context.WithTimeout(ctx, visionBridgeTimeout)`;
  `cancel` deferred.
- Config: new `vision_bridge_timeout_seconds` on the SDK config (global, not
  per-router — YAGNI). `0`/unset = default 30s; negative = bridge disabled
  (operator escape hatch).
- AGENTS.md "no timeout" exceptions list gains an explicit entry: the auto-router
  vision bridge deadline in `sdk/api/handlers/vision_bridge.go`.
- Bridge failure/deadline stays best-effort: `Warn` log (with
  `vision_bridge_model`, `target_model`, `elapsed_ms`) then continue with the
  original body — existing fallback behavior unchanged.
- Instrumentation: one `Info` log on bridge success (`elapsed_ms`, `bridge_model`,
  `target_model`, image count); one `Warn` on failure (enriched with `elapsed_ms`).

Not done: bridge retry, running the bridge in parallel with the main request
(semantic change: analysis text must exist before the body is sent), per-router
timeout.

### Part 2 — Anti-empty-output for thinking models (Symptom 2)

- **Fallback cap**: when a registered thinking model has `MaxCompletionTokens == 0`,
  inflate to a new default-thinking constant (32,000) instead of doing nothing.
  Inflation still only ever raises a client cap, never lowers it.
- **Empty-completion detection (non-streaming)**: after a routed non-streaming
  response, `finish_reason == "max_tokens"` with zero visible content becomes an
  explicit 502 error: "model X hit the token cap before producing output; raise
  max_tokens or reduce reasoning effort".
- **Streaming**: no chunk-level detection (expensive/fragile). Instead, a `Warn`
  log when a routed request's usage event records `completion_tokens == 0`
  (tool-call responses excluded). Symptom becomes measurable in production.
- Both keyed on `RouterIDFromContext(ctx) != ""` so non-router request behavior is
  unchanged.

Not done: auto-retry with a larger cap (double-billing risk), translator changes,
thinking pipeline changes.

### Part 3 — Visible silence + correct resolve-failure error

- **Cooldown wait log**: one `Info` line before a nonzero cooldown wait
  (`provider`, `model`, `wait_ms`, `attempt`) in the conductor. One line per
  waiting request, not per tick. Error semantics unchanged.
- **Resolve-failure 400**: when the request targets an enabled auto-router but
  `Resolve` fails, `resolveAutoRouterModel` reports an explicit 400:
  "auto router %s has no resolvable tier mapping for tier %s; configure a mapping
  or fix the fallback chain" — surfaced before `providersForExecution` at all
  call sites (execution ×2, stream, count). `recordPreExecutionFailure` still
  records it.
- New `autoRouterResolved.resolveFailed` (+ ready-made error) distinguishes
  "matched but unresolvable" from "not an auto-router". This also closes the leak
  where a router id could reach `AuthManager` against the synthetic provider.
- `Warn` log on the resolve-failed path: `router_id`, `tier`, `fallback_chain`.

### Part 4 — Routing-latency optimizations

- **Opt-1, cached bridging**: extend `AutoRouterStore.modelCache` entries to
  `{router, config *autorouter.Config}`; config bridged lazily on first hit,
  dropped by the existing `invalidateModelCache()` on every mutation. Resolver
  gains `AutoRouterConfigForModel` returning `(router, config)`; handler falls
  back to manual bridging for nil (test compatibility). Zero per-request
  allocations on the matched path.
- **Opt-2, skip score cache for large bodies**: bodies > 256 KiB go straight to
  `ScoreWithProfileCompiled` (scoring is already head+tail windowed since F2, so
  it is cheaper than the full-body SHA-256 key). Small repeated bodies keep
  benefiting from the cache.
- **Opt-3, one lowercase pass**: pre-lowercased flat text uses a new
  `collapseKeywordText` (space/non-alnum collapse only, no `ToLower`);
  `normalizeKeywordText` remains for raw profile keywords. Output must be
  byte-identical (parity corpus enforces).

Not done (deliberate): per-request snapshot JSON marshal (small struct; changes
touch the pinned golden shape), LRU for the score cache (FIFO ring suffices),
resolver unification (option C).

### Part 5 — Testing & rollout

Tests:
- Vision bridge deadline: fake executor sleeps past the deadline → original body
  returned, warn logged; `0` = 30s default; negative = bridge skipped.
- Inflation: registered thinking model without cap → 32k fallback; inflation
  never lowers a larger client cap (regression).
- Empty-completion: non-streaming `max_tokens` + empty content → 502 with the
  explanatory message; non-empty content passes through.
- Resolve-failure: enabled router without valid mapping → 400 +
  `recordPreExecutionFailure`; non-router requests unchanged.
- Opt-3 parity: existing scorer parity corpus byte-identical on both paths.
- Opt-1 cache: router upsert → `invalidateModelCache` → config cache cleared →
  next request uses the new config.

`go test ./...` + `go build` green before each commit.

Rollout: no DB schema, API, or dashboard changes — runtime behavior + logs only.
New logs: bridge duration (info/warn), cooldown wait (info), resolve-failed
(warn), empty-completion usage (warn). All single-line structured logrus fields;
never request bodies or tokens. New config documented in `config.example.yaml`.

Explicitly out of scope: default `bootstrap_retries = 0` and conductor
cooldown/retry policy (operational policy, not a bug).

Implementation order (one revertable commit each):
1. Part 1 — bridge deadline
2. Part 2 — max_tokens + empty detection
3. Part 3 — cooldown log + resolve-failure 400
4. Part 4 — perf optimizations
