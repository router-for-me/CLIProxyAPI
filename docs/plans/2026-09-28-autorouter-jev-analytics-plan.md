# Jev AI Usage, Analytics & Overview Implementation Plan

> **For agentic workers:** implement task by task, verifying each with the listed command before moving on.

**Goal:** Surface Jev AI classifier usage on Analysis → Auto Routers — how often the classifier was consulted, what it decided, what it cost and how long it took, and how the hybrid decision compares to the heuristic-only one.

**Architecture:** The classifier's verdict is already persisted per request inside `usage_events.auto_router_decision->'jev'` (written by `applyJevGate` → `DecisionSnapshot.Jev`). Nothing reads it. This plan adds one read-only aggregation over that JSONB plus the `decision_cause` column, exposes it through the existing `top=` switch on `GET /v0/management/auto-routers/stats`, and renders it as a new **Jev AI** tab.

**Key measured facts that shape the UI** (from `docs/plans/2026-09-28-autorouter-jev-eval-findings.md`):
- The heuristic under-routes 29/60 cases; every classifier error is over-routing. So over-routing is the number an operator must watch, and it is computable per tier from the aggregated verdict distribution.
- Confidence is stable enough to tune a floor but drifts ±0.01–0.04; a floor near 0.35 must not be tuned on thin data. The UI therefore suppresses floor-tuning affordances below a minimum sample.

**Tech Stack:** Go 1.26 (`internal/store`, `internal/api/handlers/management`), React + Vite (`web/dashboard`).

**Not in this plan (explicitly rejected):** per-request Jev data in the lazy-loaded Replay drawer. The aggregation reads only the two JSONB keys it needs (`choice`, `confidence`), never `probabilities`, so the rows stay small.

---

## Background: what already exists

- `usage_events.auto_router_decision` (JSONB) holds `DecisionSnapshot`; `DecisionCause` is *also* copied to the `decision_cause` TEXT column, so cause counting needs no JSONB at all.
- `AutoRouterDecisionStats.CauseCounts` (`internal/store/pg_usage.go:2363`) is initialized with only `literal_keyword_match` and `complexity_scorer` and its SQL sums only those two, so the three `jev_*` causes are invisible today. The two UI consumers (`OverviewTab`, `DecisionDistributionTab`) compute their denominators from that same two-element map — so they are both already correct the moment the map is complete, and both silently drop Jev decisions today.
- `JevDecision` (`internal/autorouter/profile.go:353`): `model`, `choice`, `confidence`, `probabilities`, `latency_ms`, `input_tokens`, `cache` (`hit`/`miss`), `verdict` (`accepted` / `rejected_low_confidence` / `error` / `breaker_open`).
- Gate resolution (`sdk/api/handlers/handlers_auto_router.go:106-129`): accepted → `jev_classifier`; rejected → `jev_low_confidence`; error/breaker/malformed → `jev_fallback_heuristic`.

---

### Task 1: Count the three Jev causes

**Files:** Modify `internal/store/pg_usage.go` (`SelectAutoRouterDecisionStats`, ~2363 and ~2382).

**Step 1:** Initialize `CauseCounts` with all five causes. The three Jev keys must exist as 0 even when the feature is off, so the UI can render "no classifier activity" without a missing-key check.

**Step 2:** Add three `COALESCE(SUM(CASE WHEN e.decision_cause = '…' THEN 1 ELSE 0 END), 0)` aggregates to the single-row query and scan into `causeJevClassifier`, `causeJevLowConf`, `causeJevFallback`.

Why the column and not the JSONB: `decision_cause` is a plain TEXT column with a router-scoped index and is written from the same snapshot, so counting it is cheaper and cannot disagree with the snapshot's own cause.

**Verify:** `go build -o test-output ./cmd/server && rm test-output`

---

### Task 2: Aggregate the classifier block

**Files:** Modify `internal/store/pg_usage.go` (new type + new query method).

**Step 1:** Add the type next to `AutoRouterDecisionStats`:

```go
type AutoRouterJevStats struct {
	Consulted         int64              `json:"consulted"`
	Accepted          int64              `json:"accepted"`
	LowConfidence     int64              `json:"low_confidence"`
	Errors            int64              `json:"errors"`
	BreakerOpen       int64              `json:"breaker_open"`
	CacheHits         int64              `json:"cache_hits"`
	AvgConfidence     float64            `json:"avg_confidence"`
	P95Confidence     float64            `json:"p95_confidence"`
	DecidedCount      int64              `json:"decided_count"`
	Overrouted        int64              `json:"overrouted"`
	Underrouted       int64              `json:"underrouted"`
	OverrideCount     int64              `json:"override_count"`
	ChoiceCounts      map[string]int64   `json:"choice_counts"`
	ConfidenceHistogram []AutoRouterScoreBucket `json:"confidence_histogram"`
	AvgLatencyMs      float64            `json:"avg_latency_ms"`
	AvgInputTokens    float64            `json:"avg_input_tokens"`
	InputTokens       int64              `json:"input_tokens"`
	VerdictTierCounts map[string]int64   `json:"verdict_tier_counts"`
	OverroutedByTier  map[string]int64   `json:"overrouted_by_tier"`
}
```

**Step 2:** Add `SelectAutoRouterJevStats(ctx, filter)`. One single-row aggregate:

- `COUNT(*) FILTER (WHERE e.auto_router_decision ? 'jev')` → consulted
- verdict counts, `Cache='hit'` count → from `->'jev'->>'verdict'` / `->>'cache'`
- `AVG`/`PERCENTILE_CONT(0.95)` over confidence, restricted to rows that actually carry a confidence (a breaker-open verdict has none and would bias the mean to 0)
- `AVG` latency and input tokens, `SUM` input tokens
- over/under-routing and `AVG(verdict rank) - AVG(effective rank)` — see the SQL note below
- `choice_counts` and `verdict_tier_counts` as separate `GROUP BY` queries, merged over the four canonical tiers so a tier with no data renders as 0

**SQL note — over/under-routing:** both terms live in one JSONB object, so:

```sql
SUM(CASE WHEN tierrank(e.auto_router_decision->'jev'->>'choice')
            > tierrank(e.effective_tier) THEN 1 ELSE 0 END)
```

where `tierrank` is a helper expression, not a SQL function: PostgreSQL has no `CREATE FUNCTION` in this codebase's migration style, so inline it as

```sql
CASE <expr> WHEN 'simple' THEN 0 WHEN 'medium' THEN 1 WHEN 'complex' THEN 2 WHEN 'reasoning' THEN 3 END
```

and guard with `e.auto_router_decision->'jev' ? 'choice'` where a NULL choice (breaker open) must not be counted. A verdict for a tier outside the four is not a routing decision and must be excluded from every leg.

**Step 3:** Add `%s`-interpolated table name (never a bound parameter) and reuse `autoRouterSnapshotWhere` so the router/api-key/time scoping matches every other aggregation.

**Verify:** `go build -o test-output ./cmd/server && rm test-output`

---

### Task 3: Router configuration in the payload

**Files:** Modify `internal/api/handlers/management/auto_router_stats.go`.

**Step 1:** Give the handler an auto-router store so it can read the selected router's `jev_*` knobs, and add a nil-safe `SetAutoRouterStore`.

**Step 2:** In `GetAutoRouterStats`, when the router resolves, include `"router": {"jev_enabled": …, "jev_min_confidence": …, "jev_timeout_ms": …}` in the response — on every `top=` branch, since it is cheap and every tab can use it.

**Step 3:** Add `case "jev"` serving `resp["jev_stats"]` plus the router block, and update the handler doc comment.

The configuration is resolved with the same defaults the runtime gate uses. Those constants (`jevDefaultMinConfidence = 0.35`, `jevDefaultTimeout = 400ms`) live in `package handlers` (`sdk/api/handlers`), which this package must not import. Return the stored values as-is and let the UI apply the shared defaults; if they and the runtime constants ever drift, the *effective floor* shown in the UI is wrong while routing is not — an acceptable, documented trade for not adding an import edge.

**Verify:** `go build -o test-output ./cmd/server && rm test-output`

---

### Task 4: Store test

**Files:** Create `internal/store/pg_usage_jev_test.go`.

Seed events through `UsageStore.InsertEvent` with `AutoRouterDecision` marshaled from `autorouter.DecisionSnapshot` (the store already imports `internal/autorouter`, so no cycle), covering: an accepted classifier decision that over-routed, one that agreed, a low-confidence rejection, a breaker-open with no choice, and a non-Jev keyword decision. Assert every field including that the non-Jev row is absent from `consulted` and present in the heuristic cause count.

**Verify:** `PGSTORE_TEST_DSN=$(grep PGSTORE_DSN .env | cut -d= -f2-) go test -run TestSelectAutoRouterJevStats ./internal/store/`

---

### Task 5: API client function

**Files:** Modify `web/dashboard/src/api/client.js`.

Add `getAutoRouterJevStats(params)` mirroring `getAutoRouterDecisionStats`, returning `{ jev_stats, router }`.

---

### Task 6: Jev AI tab

**Files:** Create `web/dashboard/src/components/autorouter/JevTab.jsx`; modify `AutoRouterAnalysisTabs.jsx` (TABS + fetch + render) and `DecisionDistributionTab.jsx` (cause cards).

**Step 1 — the tab.** Cards, in order:

1. **Classifier status** — a banner answering "is this on and working right now?" from the router config: disabled / enabled-but-unconfigured (no key) / enabled with the effective floor and timeout shown. Then the engagement line: consulted N of M routed requests (share %), of which accepted / low-confidence / errors / breaker-open, cache hits.
2. **What the classifier chose** — the four choice counts as stat cards, with the share that *disagreed* with the heuristic (`override_count`) called out, since an override is the routing actually changing.
3. **Verdict vs routed tier** — table of choice × effective tier from `verdict_tier_counts`. The diagonal is agreement; **above the diagonal is over-routing** (money spent on headroom) and below is under-routing (capability risk). This is the single most useful view given that all measured classifier errors were over-routing.
4. **Confidence** — 20-bucket histogram (reuse the clamp in `DecisionDistributionTab`'s SVG approach at a smaller size) with a dashed line at the effective floor, plus mean/p95. Below a minimum decided sample, say so instead of inviting a floor change.
5. **Latency & cost** — avg classifier latency, avg/p95 confidence, avg input tokens, and the projected classifier token spend from the summed input tokens at the configured rate. The latency card must state that the classifier runs *before* the upstream connection, so it adds to end-to-end latency in full.

**Step 2 — tab registration.** Insert `{ key: 'jev', label: 'Jev AI' }` after `distribution`; add a `jev` fetch gated the same way as `decisionStats`; render in the tab switch.

**Step 3 — cause cards.** Add a "Classifier overrides" card to both cause sections, driven by `cause_counts.jev_classifier`, and add the two fallback causes to the distribution tab's per-tier picture. Also fix both existing percent hints: they divide by `keyword + scorer`, which understates every share once Jev decisions exist.

**Verify:** `cd web/dashboard && npm run build`

---

### Task 7: Verify and land

- `gofmt -w .` (then `git checkout -- internal/translator/common/finish_reason_test.go`, a pre-existing misformatting this churns)
- `go vet ./internal/store/ ./internal/api/handlers/management/`
- `go build -o test-output ./cmd/server && rm test-output`
- `go test ./internal/store/ ./internal/api/handlers/management/`
- `make dash-embed` so `/dashboard` serves the new tab
- Commit; do not tag or push.
