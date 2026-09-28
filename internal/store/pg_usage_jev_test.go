package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

// jevSnapshot marshals a decision snapshot carrying a classifier block, which
// is what SelectAutoRouterJevStats reads. The store persists the snapshot
// verbatim, so building it through the real type keeps the test honest about
// the production wire shape.
func jevSnapshot(t *testing.T, jev *autorouter.JevDecision, scored, effective autorouter.Tier, cause string) []byte {
	t.Helper()
	raw, err := json.Marshal(autorouter.DecisionSnapshot{
		ScoredTier:    scored,
		EffectiveTier: effective,
		DecisionCause: cause,
		Jev:           jev,
	})
	if err != nil {
		t.Fatalf("marshal decision snapshot: %v", err)
	}
	return raw
}

// TestSelectAutoRouterJevStatsRollsUpClassifierActivity seeds one event per
// classifier outcome and asserts the rollup distinguishes them, attributes
// over-routing to the scored tier, and excludes non-classifier decisions.
func TestSelectAutoRouterJevStatsRollsUpClassifierActivity(t *testing.T) {
	store := newTestPostgresStore(t, "usage_jev_stats")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	now := time.Now().UTC()

	const routerID = "router:jev-stats"

	// Accepted verdict that over-routed: classifier said reasoning, the
	// scorer said simple.
	overRoute := jevSnapshot(t, &autorouter.JevDecision{
		Model: "jev-1.13.0", Choice: "reasoning", Confidence: 0.94,
		LatencyMs: 300, InputTokens: 520, Cache: "miss", Verdict: "accepted",
	}, autorouter.TierSimple, autorouter.TierReasoning, "jev_classifier")

	// Accepted verdict that agreed with the scorer.
	agree := jevSnapshot(t, &autorouter.JevDecision{
		Model: "jev-1.13.0", Choice: "complex", Confidence: 0.72,
		LatencyMs: 280, InputTokens: 500, Cache: "miss", Verdict: "accepted",
	}, autorouter.TierComplex, autorouter.TierComplex, "jev_classifier")

	// A cached repeat of the agreed verdict: it carries the original call's
	// latency and tokens, so it must not inflate the per-call averages.
	cached := jevSnapshot(t, &autorouter.JevDecision{
		Model: "jev-1.13.0", Choice: "complex", Confidence: 0.72,
		LatencyMs: 280, InputTokens: 500, Cache: "hit", Verdict: "accepted",
	}, autorouter.TierComplex, autorouter.TierComplex, "jev_classifier")

	// Below the floor: the heuristic tier stands.
	lowConf := jevSnapshot(t, &autorouter.JevDecision{
		Model: "jev-1.13.0", Choice: "medium", Confidence: 0.21,
		LatencyMs: 260, InputTokens: 510, Cache: "miss", Verdict: "rejected_low_confidence",
	}, autorouter.TierSimple, autorouter.TierSimple, "jev_low_confidence")

	// Breaker open: no choice and no confidence at all.
	breaker := jevSnapshot(t, &autorouter.JevDecision{
		Model: "jev-1.13.0", Verdict: "breaker_open",
	}, autorouter.TierMedium, autorouter.TierMedium, "jev_fallback_heuristic")

	// A classifier timeout, which does carry a choice-less verdict.
	timedOut := jevSnapshot(t, &autorouter.JevDecision{
		Model: "jev-1.13.0", Cache: "miss", Verdict: "error",
	}, autorouter.TierSimple, autorouter.TierSimple, "jev_fallback_heuristic")

	// A plain heuristic decision: no classifier block at all.
	heuristic := jevSnapshot(t, nil, autorouter.TierComplex, autorouter.TierComplex, "complexity_scorer")

	events := []UsageEvent{
		{Provider: "p", Model: "target-a", Tier: "reasoning", RouterID: routerID, ScoredTier: "simple", EffectiveTier: "reasoning", DecisionCause: "jev_classifier", AutoRouterDecision: overRoute, RequestedAt: now},
		{Provider: "p", Model: "target-b", Tier: "complex", RouterID: routerID, ScoredTier: "complex", EffectiveTier: "complex", DecisionCause: "jev_classifier", AutoRouterDecision: agree, RequestedAt: now},
		{Provider: "p", Model: "target-b", Tier: "complex", RouterID: routerID, ScoredTier: "complex", EffectiveTier: "complex", DecisionCause: "jev_classifier", AutoRouterDecision: cached, RequestedAt: now},
		{Provider: "p", Model: "target-c", Tier: "simple", RouterID: routerID, ScoredTier: "simple", EffectiveTier: "simple", DecisionCause: "jev_low_confidence", AutoRouterDecision: lowConf, RequestedAt: now},
		{Provider: "p", Model: "target-d", Tier: "medium", RouterID: routerID, ScoredTier: "medium", EffectiveTier: "medium", DecisionCause: "jev_fallback_heuristic", AutoRouterDecision: breaker, RequestedAt: now},
		{Provider: "p", Model: "target-c", Tier: "simple", RouterID: routerID, ScoredTier: "simple", EffectiveTier: "simple", DecisionCause: "jev_fallback_heuristic", AutoRouterDecision: timedOut, RequestedAt: now},
		{Provider: "p", Model: "target-e", Tier: "complex", RouterID: routerID, ScoredTier: "complex", EffectiveTier: "complex", DecisionCause: "complexity_scorer", AutoRouterDecision: heuristic, RequestedAt: now},
		// A different router must not leak into the rollup.
		{Provider: "p", Model: "target-z", Tier: "simple", RouterID: "router:other", ScoredTier: "simple", EffectiveTier: "simple", DecisionCause: "jev_classifier", AutoRouterDecision: agree, RequestedAt: now},
	}
	for _, e := range events {
		if err := us.InsertEvent(ctx, e); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	got, err := us.SelectAutoRouterJevStats(ctx, UsageFilter{RouterID: routerID})
	if err != nil {
		t.Fatalf("SelectAutoRouterJevStats: %v", err)
	}

	if got.Consulted != 6 {
		t.Errorf("consulted = %d, want 6 (the heuristic-only router and the other router excluded)", got.Consulted)
	}
	if got.Accepted != 3 || got.LowConfidence != 1 || got.Errors != 1 || got.BreakerOpen != 1 {
		t.Errorf("verdict split = accepted %d / low %d / error %d / breaker %d; want 3/1/1/1",
			got.Accepted, got.LowConfidence, got.Errors, got.BreakerOpen)
	}
	if got.CacheHits != 1 {
		t.Errorf("cache hits = %d, want 1", got.CacheHits)
	}
	// Four verdicts carry a confidence: the two accepted ones, their cached
	// repeat, and the low-confidence rejection. The breaker-open and the error
	// verdicts never got an answer, and the persisted JSON still says 0 for
	// both (the field has no omitempty), so counting them would drag the mean
	// down and report an unsure classifier where there was none.
	if got.DecidedCount != 4 {
		t.Errorf("decided = %d, want 4 (error and breaker-open verdicts have no confidence)", got.DecidedCount)
	}
	// (0.94 + 0.72 + 0.72 + 0.21) / 4 = 0.6475
	if diff := got.AvgConfidence - 0.6475; diff > 0.001 || diff < -0.001 {
		t.Errorf("avg confidence = %v, want ~0.6475", got.AvgConfidence)
	}
	if got.Overrouted != 1 || got.Underrouted != 0 {
		t.Errorf("over/under = %d/%d, want 1/0", got.Overrouted, got.Underrouted)
	}
	if got.OverrideCount != 1 {
		t.Errorf("override count = %d, want 1 (only the over-route disagreed with the scorer)", got.OverrideCount)
	}
	if got.OverroutedByTier["simple"] != 1 {
		t.Errorf("over-routing by tier = %v, want simple:1", got.OverroutedByTier)
	}
	// The low-confidence verdict still names a tier, so it counts as "wanted"
	// but not as "applied". The breaker-open and error verdicts name nothing.
	if got.ChoiceCounts["complex"] != 2 || got.ChoiceCounts["reasoning"] != 1 ||
		got.ChoiceCounts["medium"] != 1 || got.ChoiceCounts["simple"] != 0 {
		t.Errorf("choice counts = %v, want complex 2 / reasoning 1 / medium 1 / simple 0", got.ChoiceCounts)
	}
	if got.AppliedChoiceCounts["reasoning"] != 1 || got.AppliedChoiceCounts["complex"] != 2 ||
		got.AppliedChoiceCounts["medium"] != 0 {
		t.Errorf("applied choice counts = %v, want reasoning 1 / complex 2 / medium 0", got.AppliedChoiceCounts)
	}
	if got.ChoiceVsScored["reasoning|simple"] != 1 || got.ChoiceVsScored["complex|complex"] != 2 {
		t.Errorf("choice × scored = %v, want reasoning|simple 1 and complex|complex 2", got.ChoiceVsScored)
	}
	// Only the four calls that reached the API report tokens: 520 + 500 + 500
	// + 510. The error and breaker-open verdicts never got a usage block.
	if got.InputTokens != 2030 {
		t.Errorf("input tokens = %d, want 2030", got.InputTokens)
	}
	// The histogram must sum to the decided count so a bar chart totals.
	var histTotal int64
	for _, bucket := range got.ConfidenceHistogram {
		histTotal += bucket.Count
	}
	if histTotal != got.DecidedCount {
		t.Errorf("histogram total = %d, want %d", histTotal, got.DecidedCount)
	}
	if len(got.ConfidenceHistogram) != 20 {
		t.Fatalf("histogram buckets = %d, want 20", len(got.ConfidenceHistogram))
	}
	// 0.94 lands in bucket 18 (0.90-0.95); 0.72 and 0.21 in 14 and 4.
	if got.ConfidenceHistogram[18].Count != 1 || got.ConfidenceHistogram[14].Count != 2 || got.ConfidenceHistogram[4].Count != 1 {
		t.Errorf("histogram = %d/%d/%d at 18/14/4; want 1/2/1",
			got.ConfidenceHistogram[18].Count, got.ConfidenceHistogram[14].Count, got.ConfidenceHistogram[4].Count)
	}
}

// TestSelectAutoRouterJevStatsEmptyWhenFeatureOff verifies that a router whose
// decisions all came from the heuristic reports zero consulted rather than an
// error or a nil rollup — the UI renders "no classifier activity" from this.
func TestSelectAutoRouterJevStatsEmptyWhenFeatureOff(t *testing.T) {
	store := newTestPostgresStore(t, "usage_jev_off")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	snap := jevSnapshot(t, nil, autorouter.TierSimple, autorouter.TierSimple, "literal_keyword_match")
	if err := us.InsertEvent(ctx, UsageEvent{
		Provider: "p", Model: "m", Tier: "simple", RouterID: "router:off",
		ScoredTier: "simple", EffectiveTier: "simple", DecisionCause: "literal_keyword_match",
		AutoRouterDecision: snap, RequestedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	got, err := us.SelectAutoRouterJevStats(ctx, UsageFilter{RouterID: "router:off"})
	if err != nil {
		t.Fatalf("SelectAutoRouterJevStats: %v", err)
	}
	if got.Consulted != 0 || got.Accepted != 0 || got.OverrideCount != 0 {
		t.Errorf("rollup = %+v; want all zero", got)
	}
	if len(got.ChoiceCounts) != 4 {
		t.Errorf("choice counts = %v; want the 4 canonical tiers seeded at 0", got.ChoiceCounts)
	}
}

// TestSelectAutoRouterDecisionStatsCountsJevCauses guards the cause map that
// both analysis tabs use as their denominator: without the jev_* keys the
// classifier's decisions are invisible and every share is overstated.
func TestSelectAutoRouterDecisionStatsCountsJevCauses(t *testing.T) {
	store := newTestPostgresStore(t, "usage_jev_causes")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	now := time.Now().UTC()

	for i, cause := range []string{
		"literal_keyword_match", "complexity_scorer",
		"jev_classifier", "jev_low_confidence", "jev_fallback_heuristic",
	} {
		if err := us.InsertEvent(ctx, UsageEvent{
			Provider: "p", Model: "m", Tier: "simple", RouterID: "router:causes",
			ScoredTier: "simple", EffectiveTier: "simple", DecisionCause: cause,
			AutoRouterDecision: jevSnapshot(t, nil, autorouter.TierSimple, autorouter.TierSimple, cause),
			RequestedAt:        now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	got, err := us.SelectAutoRouterDecisionStats(ctx, UsageFilter{RouterID: "router:causes"})
	if err != nil {
		t.Fatalf("SelectAutoRouterDecisionStats: %v", err)
	}
	for _, cause := range []string{
		"literal_keyword_match", "complexity_scorer",
		"jev_classifier", "jev_low_confidence", "jev_fallback_heuristic",
	} {
		if got.CauseCounts[cause] != 1 {
			t.Errorf("cause %q = %d, want 1 (map = %v)", cause, got.CauseCounts[cause], got.CauseCounts)
		}
	}
}
