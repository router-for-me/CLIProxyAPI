package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// newWeightedSchedulerForTest builds a scheduler over gemini auths with the
// given model registered for every auth ID. Mirrors newFillFirstSchedulerForTest
// but does not seed in-flight counts (the round-2 weighted selector reads the
// per-entry weight attribute, not the in-flight counter).
func newWeightedSchedulerForTest(t *testing.T, model string, auths ...*Auth) *authScheduler {
	t.Helper()
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		ids = append(ids, auth.ID)
	}
	registerSchedulerModels(t, "gemini", model, ids...)
	return newSchedulerForTest(&FillFirstSelector{}, auths...)
}

// pickWeightedWithStrategy is a thin wrapper around pickSingleWithStrategy
// for the round-2 schedulerStrategyWeighted. It mirrors pickFillFirstWithStrategy.
func pickWeightedWithStrategy(t *testing.T, scheduler *authScheduler, model string, tried map[string]struct{}) *Auth {
	t.Helper()
	auth, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, tried, schedulerStrategyWeighted)
	if errPick != nil {
		t.Fatalf("pickSingleWithStrategy(weighted) error = %v", errPick)
	}
	if auth == nil {
		t.Fatalf("pickSingleWithStrategy(weighted) auth = nil")
	}
	return auth
}

// TestSchedulerWeighted_RespectsEntryWeight is the chi-square distribution
// check for the round-2 weighted global selector: two gemini auths at the
// same priority with per-entry weights 1 and 3 must be sampled proportionally
// across 10000 picks. The 3-weight auth should land in 70-80% of draws (the
// 5% band around 75%).
func TestSchedulerWeighted_RespectsEntryWeight(t *testing.T) {
	t.Parallel()

	const model = "weighted-dist-model"
	const iterations = 10000
	scheduler := newWeightedSchedulerForTest(t, model,
		&Auth{ID: "weighted-low", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "1"}},
		&Auth{ID: "weighted-high", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "3"}},
	)

	counts := make(map[string]int, 2)
	for i := 0; i < iterations; i++ {
		auth := pickWeightedWithStrategy(t, scheduler, model, nil)
		counts[auth.ID]++
	}

	total := counts["weighted-low"] + counts["weighted-high"]
	if total != iterations {
		t.Fatalf("weighted picks total = %d, want %d (counts=%#v)", total, iterations, counts)
	}
	low := counts["weighted-low"]
	high := counts["weighted-high"]

	// With weights 1:3 over 10000 draws, expect ~2500/7500 with a 5% band.
	if low < 2000 || low > 3000 {
		t.Fatalf("weighted-low picks = %d, want range [2000, 3000] (counts=%#v)", low, counts)
	}
	if high < 7000 || high > 8000 {
		t.Fatalf("weighted-high picks = %d, want range [7000, 8000] (counts=%#v)", high, counts)
	}
}

// TestSchedulerWeighted_FallsBackOnZeroSum exercises the defensive fallback:
// when every entry's effective weight is zero (impossible post-planner per
// Task 2's normalizeEntryWeight, but the helper must not panic), the selector
// must return a non-nil entry instead of crashing. The helper runs against
// the raw pickWeighted function, bypassing the scheduler's predicate that
// would otherwise skip zero-weight entries.
func TestSchedulerWeighted_FallsBackOnZeroSum(t *testing.T) {
	t.Parallel()

	// Build raw scheduledAuth entries with explicit meta.weight = 0. The
	// scheduler's predicate skips zero-weight entries, so we drive the
	// helper directly to confirm the zero-sum branch returns a non-nil
	// fallback entry instead of nil.
	entries := []*scheduledAuth{
		{meta: &scheduledAuthMeta{auth: &Auth{ID: "zb-a"}, weight: 0}, auth: &Auth{ID: "zb-a"}},
		{meta: &scheduledAuthMeta{auth: &Auth{ID: "zb-b"}, weight: 0}, auth: &Auth{ID: "zb-b"}},
	}

	if got := pickWeighted(entries); got == nil {
		t.Fatal("pickWeighted on zero-sum entries returned nil, want non-nil fallback")
	}

	// Empty input is also well-defined: nil.
	if got := pickWeighted(nil); got != nil {
		t.Fatalf("pickWeighted(nil) = %v, want nil", got)
	}
}

// TestSchedulerWeighted_HelperReturnsHigherWeightMoreOften is the unit-level
// distribution check on the pure helper: build a small entry list with mixed
// weights, drive pickWeighted directly, and confirm the higher-weight entry
// dominates over a large number of draws.
func TestSchedulerWeighted_HelperReturnsHigherWeightMoreOften(t *testing.T) {
	t.Parallel()

	mkEntry := func(id string, w int64) *scheduledAuth {
		return &scheduledAuth{
			meta: &scheduledAuthMeta{auth: &Auth{ID: id}, weight: w},
			auth: &Auth{ID: id},
		}
	}

	entries := []*scheduledAuth{
		mkEntry("h-low", 1),
		mkEntry("h-high", 4),
	}

	const iterations = 5000
	counts := make(map[string]int, 2)
	for i := 0; i < iterations; i++ {
		got := pickWeighted(entries)
		if got == nil || got.auth == nil {
			t.Fatalf("pickWeighted #%d returned nil", i)
		}
		counts[got.auth.ID]++
	}

	// With weights 1:4 over 5000 draws, expect ~1000/4000. Allow a 5% band.
	low := counts["h-low"]
	high := counts["h-high"]
	if low < 800 || low > 1200 {
		t.Fatalf("h-low picks = %d, want range [800, 1200] (counts=%#v)", low, counts)
	}
	if high < 3800 || high > 4200 {
		t.Fatalf("h-high picks = %d, want range [3800, 4200] (counts=%#v)", high, counts)
	}
}
