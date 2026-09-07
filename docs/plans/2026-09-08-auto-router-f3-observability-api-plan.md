# Auto Router F3 — Observability API Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Four management endpoints over `usage_events` (no schema changes): decision distribution stats (jsonb aggregation), tier↔latency/cost performance, profile simulation over history, and single-request replay.

**Architecture:** Store queries live in `internal/store/pg_usage.go` (reusing `addUsageScopeArgs` and `buildAutoRouterDecisionWhere` patterns); handlers live in `internal/api/handlers/management/auto_router_stats.go` + a new `auto_router_simulate.go`; routes registered in `internal/api/server_management.go` next to the existing `/auto-routers/:id/decisions` route. All PG-only (`requirePG`/`requireUsageStore`/`requireAutoRouters` patterns; 503 without PG).

**Tech Stack:** Go 1.26, PostgreSQL jsonb + `percentile_cont` aggregate, gin handlers.

**Working directory:** `/home/bilfid/projects/nixllm` (main, inline execution).

**Key facts verified:**
- `usage_events` has `tier`, `router_id`, `model`, `decision_cause`, `profile_version`, `profile_hash`, `auto_router_decision` (jsonb), `latency_ms`, `ttft_ms`, `failed`, `cost_usd`, `total_tokens`, `request_id`, `requested_at`, `api_key_id`.
- `DecisionSnapshot` jsonb keys: `profile_version`, `profile_hash`, `profile_snapshot`, `score_total`, `score_fields`, `reasoning_markers`, `scored_tier`, `effective_tier`, `decision_cause`, `matched_rules`, `mapping_tier`, `fallback_chain`, `target_model`.
- Route block: `internal/api/server_management.go:441-451`. Handler patterns in `auto_router_stats.go` (`parseAutoRouterStatsQuery`), `auto_router_profile.go` (`ListAutoRouterDecisions`, `requireUsageStore`, `requireAutoRouters`).

---

### Task 1: Decision-stats store query + endpoint

**Files:**
- Modify: `internal/store/pg_usage.go` (structs + `SelectAutoRouterDecisionStats`)
- Modify: `internal/api/handlers/management/auto_router_stats.go` (handler)
- Modify: `internal/api/server_management.go` (route)
- Test: `internal/store/pg_auto_router_stats_query_test.go` (SQL-shape unit tests where DB absent → skip gracefully per existing store-test patterns; check how existing pg tests gate on PG DSN)

**Step 1: Store structs + query.** `AutoRouterDecisionStats`:
```go
type AutoRouterDecisionStats struct {
	EventCount       int64                  `json:"event_count"`
	ScoreHistogram   []AutoRouterScoreBucket `json:"score_histogram"`   // bucket index 0..19 → [0.05i, 0.05(i+1))
	DimensionAverages map[string]float64     `json:"dimension_averages"` // 7 field keys
	CauseCounts      map[string]int64       `json:"cause_counts"`       // keyword vs scorer
	FallbackChains   []AutoRouterChainCount  `json:"fallback_chains"`    // chain text → count, desc
	MismatchCount    int64                  `json:"mismatch_count"`     // effective_tier <> mapping_tier
	Truncated        bool                   `json:"truncated"`          // events beyond cap not aggregated (stats query has no cap — always false; field shared with simulate response shape)
}
type AutoRouterScoreBucket struct { Bucket int `json:"bucket"`; Count int64 `json:"count"` }
type AutoRouterChainCount struct { Chain string `json:"chain"`; Count int64 `json:"count"` }
```
SQL (single pass, window-scoped): histogram via `width_bucket((auto_router_decision->>'score_total')::float8, 0, 1, 20)` GROUP BY; dimension averages via `AVG((auto_router_decision->'score_fields'->>'token_count')::float8)` etc. (7 subqueries or a lateral); cause counts GROUP BY `decision_cause`; fallback chains GROUP BY `(auto_router_decision->>'fallback_chain')::text` (fallback_chain serializes as JSON array text — key on that); mismatch via `COUNT(*) FILTER (WHERE effective_tier <> mapping_tier AND mapping_tier IS NOT NULL AND mapping_tier <> '')`. All null-safe: `WHERE auto_router_decision IS NOT NULL AND jsonb_typeof(auto_router_decision) = 'object'`.

**Step 2: Handler** `GetAutoRouterDecisionStats(c)` — reuse `parseAutoRouterStatsQuery`; require router_id; default window 7d when from AND to both zero (400 only if neither window style is usable — decision: default 7d, never 400, since defaults are safer than hard failures here; cap window at 90d with 400). Route: `GET /auto-routers/:id/decision-stats`.

**Step 3: Verify** — `go build`, compile-check binary, run store tests.

**Step 4: Commit** — `feat(usage): auto-router decision-stats aggregation (jsonb)`

### Task 2: Tier↔latency/cost performance query

**Files:**
- Modify: `internal/store/pg_usage.go` (`AutoRouterTierPerformance` struct + `SelectAutoRouterTierPerformance`)
- Modify: `internal/api/handlers/management/auto_router_stats.go` (`top=performance` branch on existing endpoint)
- Test: same store test file

**Step 1: Struct + query.**
```go
type AutoRouterTierPerformance struct {
	Tier          string  `json:"tier"`
	Model         string  `json:"model"`
	RequestCount  int64   `json:"request_count"`
	P50LatencyMs  float64 `json:"p50_latency_ms"`
	P95LatencyMs  float64 `json:"p95_latency_ms"`
	AvgTTFTMs     float64 `json:"avg_ttft_ms"`
	CostUSD       float64 `json:"cost_usd"`
	ErrorCount    int64   `json:"error_count"`
	ErrorRate     float64 `json:"error_rate"` // error_count / request_count
}
```
SQL: `SELECT e.tier, e.model, COUNT(*), percentile_cont(0.5) WITHIN GROUP (ORDER BY e.latency_ms), percentile_cont(0.95) WITHIN GROUP (ORDER BY e.latency_ms), AVG(e.ttft_ms), SUM(e.cost_usd), COUNT(*) FILTER (WHERE e.failed) FROM … WHERE e.tier IS NOT NULL AND e.router_id = $1 … GROUP BY e.tier, e.model ORDER BY e.tier, SUM(e.cost_usd) DESC`. Compute ErrorRate in Go (avoid div-by-zero).

**Step 2: Handler** — extend `GetAutoRouterStats`: `top=performance` returns `{"performance": [...]}`. Keep `tier`/`model` untouched.

**Step 3: Commit** — `feat(usage): auto-router tier performance stats (p50/p95 latency, ttft, error rate)`

### Task 3: Profile simulate endpoint

**Files:**
- Modify: `internal/store/pg_usage.go` (`SelectAutoRouterDecisionEvents` — raw snapshot rows for a window, cap 10k)
- Create: `internal/api/handlers/management/auto_router_simulate.go` (handler + response types)
- Modify: `internal/api/server_management.go` (route `POST /auto-routers/:id/profile/simulate`)
- Create: `internal/autorouter/simulate.go` (pure recompute logic) + `internal/autorouter/simulate_test.go`

**Step 1: Pure recompute (TDD).** `autorouter.SimulateProfile(events []StoredDecision, candidate ProfileConfig) SimulationResult`. `StoredDecision{RequestID string; ScoreFields map[ScoreField]float64; ScoreTotal float64; ScoredTier, EffectiveTier, MappingTier, TargetModel, DecisionCause string; MatchedRules []MatchedKeywordRule; ReasoningMarkers int}`.
- Tier recompute: apply candidate weights to stored `score_fields` (same weighted-total math as `ScoreWithProfile`, including the simple-indicator negative sign and normalization), then `tierFor(total, reasoningMarkers, candidate.Thresholds)`. Keyword rules: rules whose IDs exist in the event's `matched_rules` keep their stored effect (exact for identical rules); rules whose ID is NOT in the stored set are counted per-rule as unsimulable (their tier raise is skipped but reported). `tierFor` needs an exported wrapper or the simulate code lives inside package autorouter (it does — keep it unexported-friendly).
- Output: `SimulationResult{Events int64; UnsampledEvents int64; Truncated bool; Moves []TierMove; Confusion []ConfusionCell; UnsimulableRules []string; UnsimulableCount int}` where `TierMove{From, To Tier; Count int64; SampleRequestIDs []string (≤10)}` and `ConfusionCell{From, To Tier; Count int64}`.
- Tests: recompute parity (candidate == stored profile snapshot ⇒ no moves); threshold shift moves expected events; unsimulable flag; sample ID cap.

**Step 2: Store query.** `SelectAutoRouterDecisionEvents(ctx, routerID, filter, limit)` → `[]AutoRouterDecisionEvent` (id, request_id, requested_at, scored/effective/mapping tiers, cause, profile_version/hash, snapshot bytes, plus api_key_id). Cap: `LIMIT $n` (10k), return also a total count so handler can set `truncated`.

**Step 3: Handler.** Body: `{"config": ProfileConfig, "from": ..., "to": ..., "api_key_id": ...}`. Validate+normalize candidate via `autorouter.NormalizeProfile` (400 on error, same wording as profile upsert). Window >90d → 400. Run recompute in-process. Response includes `unsampled_events` (events in window without a snapshot — from a COUNT minus returned) and `truncated`.

**Step 4: Commit** — `feat(autorouter): profile simulation over stored decisions`

### Task 4: Request replay endpoint

**Files:**
- Modify: `internal/store/pg_usage.go` (`GetAutoRouterDecisionByRequest`)
- Modify: `internal/api/handlers/management/auto_router_profile.go` (`GetAutoRouterDecision`)
- Modify: `internal/api/server_management.go` (route `GET /auto-routers/:id/decisions/:request_id`)

**Step 1: Store query.** `WHERE router_id = $1 AND request_id = $2 ORDER BY requested_at DESC LIMIT 1` — same columns as ListAutoRouterDecisions plus `latency_ms`, `ttft_ms`, `failed`, `fail_status_code`, `input/output/total_tokens`, `cost_usd`. Extend the row struct with the metrics fields (add to `AutoRouterDecisionRow` — additive, json omitempty so the list endpoint output shape stays compatible).

**Step 2: Handler.** Reuses router existence check; 404 when no row; 400 when request_id param empty. Response: `{"router_id", "request_id", "decision": <full auto_router_decision jsonb>, "event": {latency_ms, ttft_ms, failed, fail_status_code, tokens, cost_usd, requested_at, model, alias}}`.

**Step 3: Commit** — `feat(usage): single auto-router decision replay endpoint`

### Task 5: Verification

- `gofmt -w` touched dirs; `go build -o test-output ./cmd/server && rm test-output`
- `go test -count=1 ./internal/autorouter/ ./internal/store/ ./internal/api/... ./sdk/...` — green (except 3 pre-existing executor failures)
- Sweep `./internal/... ./sdk/...`; design doc Progress line; commit.

## Design decisions locked

- decision-stats defaults to a 7-day window when from/to are absent (never an unbounded scan); >90d window → 400 on stats and simulate.
- Simulate caps at 10k events (server-side), flags `truncated`; events without snapshot counted as `unsampled_events`, never silently dropped.
- New keyword rules are reported as `unsimulable`, never silently ignored; existing identical rules replay exactly from stored `matched_rules`.
- Simulate/Apply remain two separate explicit actions (simulate never writes).
- `top=performance` extends the existing stats endpoint rather than a new route.
