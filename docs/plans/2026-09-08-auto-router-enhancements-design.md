# Auto Router Enhancements — Design

Date: 2026-09-08
Status: Validated (brainstormed section-by-section; scope agreed: full design, phased delivery)
Progress: F1 (scorer v2) implemented on main 2026-09-08 — parity corpus green, DecisionSnapshot shape pinned, benchmarks recorded (large-body parity at equal allocs; F2 windowing targets the allocation).
Progress: F2 (runtime perf) implemented on main 2026-09-08 — CompiledProfile precompute cached in the profile store, process-local score cache (2048 entries, SHA-256 keys, hash-based invalidation), head+tail windowing (large-body 115.9ms→71.0ms, 41.6MB→34.4MB). F3 (observability API) and F4 (dashboard) remain.

## Context

The Auto Router (`internal/autorouter`, wired in `sdk/api/handlers/handlers_auto_router.go`) classifies each
incoming request into one of four complexity tiers (SIMPLE / MEDIUM / COMPLEX / REASONING) and maps the tier
to a concrete upstream target. Today:

- **Scoring is purely heuristic** — 7 dimensions (token count, code density, reasoning markers, technical
  terms, simple-indicator penalty, multi-step, question complexity) with fixed weights and thresholds, plus
  literal keyword-tier override rules. Per-router scoring profiles (thresholds/weights/keyword rules) live in
  PG, versioned and hashed (`internal/store/pg_auto_router_profiles.go`).
- **Fallback is config-based only** — when a tier is unmapped, resolution degrades to lower tiers
  (`internal/autorouter/router.go`). Runtime upstream failures do not re-route across tiers.
- **Observability exists** — every routed request persists a `DecisionSnapshot` (score fields, tiers, cause,
  matched rules, fallback chain, profile snapshot) into `usage_events.auto_router_decision` (jsonb), alongside
  `latency_ms`, `ttft_ms`, `cost_usd`, `failed`. The dashboard has a single-view Analysis page
  (`web/dashboard/src/pages/AutoRouterAnalysisPage.jsx`) with per-tier cards and per-model cost ranking.
- **Hot-path caching already exists** for router lookup and profile lookup
  (`pg_auto_routers.go` cachedByModel, `pg_auto_router_profiles.go` cache).

Agreed focus areas: **classification accuracy** (heuristics v2 only — no LLM judge), **runtime performance**,
and **observability/UX**.

## Goals

1. Better tier classification without extra cost or latency: structure-aware and role-aware heuristics.
2. Lower hot-path overhead: single-pass text normalization, big-body windowing, compiled profiles, score cache.
3. Richer operator tooling: decision distribution, tier↔latency/cost correlation, profile simulation over
   history, per-request replay.

## Non-goals (YAGNI)

- LLM-based classification (hybrid or LLM-first) — rejected: adds latency/cost to the routing path.
- Embeddings or ML scoring.
- Runtime (as opposed to config-based) cross-tier failover on upstream errors.
- Non-English keyword sets (existing sets stay English-only).
- New chart libraries in the dashboard (inline SVG only).
- Schema changes to `usage_events`.

## Part 1 — Scorer v2 (accuracy)

Principle: all changes additive inside `internal/autorouter`. External contracts unchanged (model ids
`router:*`, `/v1` APIs, `DecisionSnapshot` shape).

1. **Structure-aware extraction** — `extractText` currently flattens all text into one lowercase string.
   v2 adds structural signals computed from the raw (pre-lowercase) text: code-fence blocks (``` … ```),
   JSON/XML literal blocks, markdown headings. Text inside code fences counts directly toward `FieldCode`
   (in addition to the existing per-token density), so "refactor this file: <200 lines of code>" is no longer
   drowned by surrounding prose.
2. **Role-aware weighting** — text from the latest `user` message is distinguished from history/system text.
   Today a 50k-token agent system prompt dominates `FieldTokens` and pushes agent traffic to REASONING.
   v2 computes `FieldTokens` primarily from **user turns**, with history contribution capped (e.g. 20%).
3. **Keyword rules unchanged** — literal keyword-tier rules keep their semantics.
4. **No new profile fields** — the two new signals (fence count, user-turn token share) fold into the
   *computation* of the existing `FieldCode` / `FieldTokens` dimensions. `ProfileConfig.Weights`,
   profile validation, and the PG profile schema are unchanged.

## Part 2 — Runtime performance

All in-process; no external cache.

### 2.1 Scorer single-pass + windowing

- **Single normalization pass**: today `extractText` lowercases the full text, then `matchedKeywordRules`
  (`internal/autorouter/profile.go`) re-normalizes the *entire* text char-by-char per request. v2 normalizes
  once in `ScoreWithProfile`; the normalized text feeds keyword matching, while word tokenization keeps using
  the punctuation-preserving lowercase text (needed by `looksLikeCode`). `matchedKeywordRules` receives
  pre-normalized text.
- **Windowing**: `scoreDimensions` operates on a window (head 24k + tail 8k words) for very large bodies.
  Density-based dimensions (code/technical/simple) stay stable; worst-case allocation drops from O(body).
  **Keyword rules still scan the full text** — they are the operator's deterministic override and are bounded
  (≤100 rules × ≤20 keywords).
- Pre-sized builders in `extractMessageString`/`extractText` to cut allocations.

### 2.2 Score cache (process-local LRU)

- Key = SHA-256(rawJSON + entryProtocol + routerID + profileHash). Hit → tier/fields/total served from cache.
- Invalidation is implicit: profileHash is part of the key, so a profile upsert produces new keys.
- ~2048 entries, mutex-guarded LRU, pattern follows the existing `internal/cache`. Cache stores score results
  only — never request bodies.

### 2.3 Compiled profile precompute

- At profile load/upsert, compile once: keywords normalized, weights defaulted, thresholds validated — stored
  as a `CompiledProfile` in the `AutoRouterProfileStore` cache (invalidated on Upsert). Per-request work
  drops from repeated `normalizeKeywordText` calls (today up to 100×20 per request) to direct matching.

Snapshot semantics (`DecisionSnapshot`) and tier classification semantics are unchanged by 2.1–2.3.

## Part 3 — Observability: data layer & API

Everything derives from existing `usage_events` columns. **No schema changes.** Fundamental constraint:
request text is never persisted (by design), so simulation works from stored signals, not re-read text.

### 3.1 Decision-stats endpoint

`GET /v0/management/auto-routers/:id/decision-stats?from&to&api_key_id`

Pure jsonb aggregation over a bounded window (window mandatory, default 7 days):

- histogram of `score_total` (0.05 buckets),
- per-dimension averages (7 fields),
- `decision_cause` distribution (keyword vs scorer),
- `fallback_chain` frequency,
- mismatch count `effective_tier ≠ mapping_tier`.

### 3.2 Tier ↔ latency/cost

New query `SelectAutoRouterTierPerformance` in `internal/store/pg_usage.go`: per `tier × target model` —
COUNT, p50/p95 `latency_ms` (`percentile_cont`), avg `ttft_ms`, SUM `cost_usd`, error rate. Exposed via the
stats endpoint (`top=performance`) or a separate endpoint — decided during implementation, whichever keeps
the existing handler cleaner.

### 3.3 Profile simulation

`POST /v0/management/auto-routers/:id/profile/simulate` — body = candidate `ProfileConfig` + window.

- Server loads `auto_router_decision` events in the window (cap 10k), then:
  - **Thresholds & weights**: recompute the tier *exactly* from stored `score_fields` + candidate
    thresholds/weights → new tier vs old tier. Output: confusion matrix (old × new), % moved per tier,
    sample `request_id`s per move.
  - **Existing keyword rules**: approximately replayable from stored `matched_rules` (exact for identical
    rules).
  - **New keyword rules**: cannot be evaluated without text — explicitly flagged in the response
    (`"unsimulable": true`), never silently ignored.

### 3.4 Request replay

`GET /v0/management/auto-routers/:id/decisions/:request_id` — the full decision snapshot joined with the same
event's metrics (latency, cost, tokens). Mostly an existing-data endpoint plus rendering.

## Part 4 — Observability: dashboard UI

`AutoRouterAnalysisPage.jsx` stays the foundation; new features become **tabs** under the existing controls
(router / API key / time range), which are shared across tabs.

- **Tab 1 — Overview** (current content) plus: cause breakdown badges on tier cards; p50/p95 latency and
  error-rate columns in the "Cost per target model" table (from 3.2).
- **Tab 2 — Decision distribution**: `score_total` histogram (simple inline SVG, 20 buckets of 0.05, with
  vertical threshold lines from the decision's stored `profile_snapshot`); per-dimension average bars; cause
  bars; fallback-chain table (chain → count); mismatch count with a CTA into the Replay tab (filtered).
- **Tab 3 — Simulation**: thresholds/weights/keyword-rules form (reuse the existing profile form components
  from the router editor), a **Simulate over range** button calling 3.3, results as a confusion matrix, %
  movement, sample request_ids (click → replay). Explicit banner: "N rules unsimulable (evaluated on stored
  signals only)". **Apply** is a separate explicit action posting to the existing profile upsert endpoint —
  no auto-apply.
- **Tab 4 — Replay**: existing `/decisions` listing with compact columns (time, scored→effective tier, cause,
  target model, latency, cost, status); clicking a row opens a detail panel with all score fields, matched
  rules, fallback chain, and event metrics.

## Part 5 — Phases, error handling, testing

### Phases (each independently shippable)

| Phase | Scope | Delivers |
|---|---|---|
| **F1 — Scorer v2** | Structure-aware extraction (code fences), role-aware token share, single-pass normalization | Accuracy up, allocation down |
| **F2 — Runtime perf** | CompiledProfile precompute, score cache LRU, windowing | Lower hot-path latency |
| **F3 — Observability API** | decision-stats endpoint, tier performance query, simulate endpoint, replay endpoint | Data for the UI |
| **F4 — Dashboard UI** | 4 tabs on the Analysis page | Operator-visible features |

Ordering rationale: F1 before F2 (windowing/single-pass touch the same code); F3 before F4 (UI needs the
endpoints). F1/F2 do not change any management API shape.

### Error handling

- **Score cache**: best-effort only — on miss/error fall back to direct computation; the cache is never a
  failure point. Simple mutex LRU; no background eviction goroutine.
- **Simulate**: >10k events → truncate and flag `truncated: true`; window >90 days → 400; candidate
  ProfileConfig is normalized+validated first (same 400s as upsert) before use.
- **decision-stats**: window mandatory; missing window → 400 (prevents unbounded jsonb scans).
- Simulations only see events carrying `auto_router_decision`; events without a snapshot are counted
  separately as `unsampled_events` and reported.
- All endpoints PG-only, following the existing `requirePG` pattern (503 without `PGSTORE_DSN`).

### Testing

- **F1**: scorer unit tests — code-fence prompt vs long prose; large agent system prompt with short user turn
  must not force REASONING; golden test that `DecisionSnapshot` shape is unchanged; allocation benchmark
  before/after.
- **F2**: cache hit/miss/hash-invalidation, LRU eviction, CompiledProfile determinism, window determinism
  (stable head+tail).
- **F3**: store integration tests (percentile query correctness, truncation flag, `unsampled_events` count);
  simulate recompute parity — recomputing from stored `score_fields` must reproduce the legacy tier on the
  threshold path (property test).
- **F4**: manual dashboard pass; follow existing JS test patterns if any are introduced (none today).

### Commit strategy

One commit per phase. Design/plan docs live under `docs/` (gitignored — use `git add -f`, per repo practice).

## Key file map

| Area | Files |
|---|---|
| Scorer / resolver | `internal/autorouter/scorer.go`, `router.go`, `profile.go`, `types.go` |
| Request path | `sdk/api/handlers/handlers_auto_router.go`, `handlers_execution.go`, `handlers_stream.go` |
| Stores | `internal/store/pg_auto_routers.go`, `pg_auto_router_profiles.go`, `pg_auto_routers_resolver.go`, `pg_usage.go` |
| Management API | `internal/api/handlers/management/auto_router_stats.go`, `auto_router_profile.go`, `auto_routers.go`, `server_management.go` |
| Usage persistence | `internal/runtime/executor/helps/usage_helpers.go`, `sdk/cliproxy/usage/manager.go` |
| Dashboard | `web/dashboard/src/pages/AutoRouterAnalysisPage.jsx`, `web/dashboard/src/api/client.js` |
