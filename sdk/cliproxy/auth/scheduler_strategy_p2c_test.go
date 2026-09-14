package auth

import (
	"context"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// newStrategySchedulerForTest builds a scheduler over gemini auths with the
// given model registered for every auth ID, mirroring the scheduler_inflight
// test harness. The scheduler-level in-flight counters are seeded from counts.
func newStrategySchedulerForTest(t *testing.T, model string, counts map[string]int, auths ...*Auth) *authScheduler {
	t.Helper()
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		ids = append(ids, auth.ID)
	}
	registerSchedulerModels(t, "gemini", model, ids...)
	scheduler := newSchedulerForTest(&RoundRobinSelector{}, auths...)
	for authID, count := range counts {
		if count != 0 {
			scheduler.adjustInFlight(authID, count)
		}
	}
	return scheduler
}

func pickWithStrategy(t *testing.T, scheduler *authScheduler, strategy schedulerStrategy, model string, tried map[string]struct{}) *Auth {
	t.Helper()
	auth, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, tried, strategy)
	if errPick != nil {
		t.Fatalf("pickSingleWithStrategy(%v) error = %v", strategy, errPick)
	}
	if auth == nil {
		t.Fatalf("pickSingleWithStrategy(%v) auth = nil", strategy)
	}
	return auth
}

// TestSchedulerStrategyP2C_StaysAmongLowestInFlight drives many real p2c picks
// over four candidates with distinct in-flight counts. Classic power-of-two-
// choices guarantees the global maximum is never returned (any sampled partner
// has fewer in-flight) and that the two lowest candidates dominate the picks
// while still spreading across both (not fill-first).
func TestSchedulerStrategyP2C_StaysAmongLowestInFlight(t *testing.T) {
	t.Parallel()

	const model = "p2c-lowest-model"
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"p2c-a": 1, "p2c-b": 1, "p2c-c": 3, "p2c-d": 7},
		&Auth{ID: "p2c-a", Provider: "gemini"},
		&Auth{ID: "p2c-b", Provider: "gemini"},
		&Auth{ID: "p2c-c", Provider: "gemini"},
		&Auth{ID: "p2c-d", Provider: "gemini"},
	)

	counts := make(map[string]int)
	const iterations = 300
	for i := 0; i < iterations; i++ {
		auth := pickWithStrategy(t, scheduler, schedulerStrategyP2C, model, nil)
		counts[auth.ID]++
		if auth.ID == "p2c-d" {
			t.Fatalf("p2c pick #%d returned the highest in-flight auth p2c-d", i)
		}
	}
	if counts["p2c-a"] == 0 || counts["p2c-b"] == 0 {
		t.Fatalf("p2c picks did not spread across the two lowest: %#v", counts)
	}
	if spread := counts["p2c-a"] + counts["p2c-b"]; spread <= iterations/2 {
		t.Fatalf("p2c picks concentrated on busier candidates: %#v", counts)
	}
}

// TestSchedulerStrategyP2C_PairResolution pins the deterministic pair rule:
// fewer in-flight wins regardless of sample order, and an in-flight tie
// resolves to the earlier flat-view entry (stable, never weight-based).
func TestSchedulerStrategyP2C_PairResolution(t *testing.T) {
	t.Parallel()

	entry := func(id string) *scheduledAuth {
		return &scheduledAuth{auth: &Auth{ID: id}}
	}
	counts := func(id string) int {
		switch id {
		case "low":
			return 2
		case "high":
			return 5
		default:
			return 0
		}
	}

	if got := resolvePowerOfTwoPair(entry("low"), 0, entry("high"), 1, counts); got.auth.ID != "low" {
		t.Fatalf("resolvePowerOfTwoPair(low, high) = %q, want low", got.auth.ID)
	}
	if got := resolvePowerOfTwoPair(entry("high"), 0, entry("low"), 1, counts); got.auth.ID != "low" {
		t.Fatalf("resolvePowerOfTwoPair(high, low) = %q, want low", got.auth.ID)
	}
	tie := func(id string) int { return 1 }
	if got := resolvePowerOfTwoPair(entry("x"), 0, entry("y"), 1, tie); got.auth.ID != "x" {
		t.Fatalf("resolvePowerOfTwoPair tie (x@0, y@1) = %q, want x (earlier index)", got.auth.ID)
	}
	if got := resolvePowerOfTwoPair(entry("x"), 1, entry("y"), 0, tie); got.auth.ID != "y" {
		t.Fatalf("resolvePowerOfTwoPair tie (x@1, y@0) = %q, want y (earlier index)", got.auth.ID)
	}
}

// TestSchedulerStrategyP2C_SingleCandidateReturnsItself pins the one-candidate
// edge: with a single matching entry there is no pair to sample, and the entry
// is returned every time.
func TestSchedulerStrategyP2C_SingleCandidateReturnsItself(t *testing.T) {
	t.Parallel()

	const model = "p2c-single-model"
	scheduler := newStrategySchedulerForTest(t, model, nil,
		&Auth{ID: "p2c-solo", Provider: "gemini"},
	)

	for i := 0; i < 3; i++ {
		if auth := pickWithStrategy(t, scheduler, schedulerStrategyP2C, model, nil); auth.ID != "p2c-solo" {
			t.Fatalf("p2c pick #%d = %q, want p2c-solo", i, auth.ID)
		}
	}
}

// TestSchedulerStrategyP2C_NoMatchingCandidateErrors pins the zero-candidate
// edge: when the tried set excludes every ready auth, the pick fails with the
// regular unavailable error instead of returning nil.
func TestSchedulerStrategyP2C_NoMatchingCandidateErrors(t *testing.T) {
	t.Parallel()

	const model = "p2c-empty-model"
	scheduler := newStrategySchedulerForTest(t, model, nil,
		&Auth{ID: "p2c-only-a", Provider: "gemini"},
		&Auth{ID: "p2c-only-b", Provider: "gemini"},
	)

	tried := map[string]struct{}{"p2c-only-a": {}, "p2c-only-b": {}}
	auth, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, tried, schedulerStrategyP2C)
	if errPick == nil {
		t.Fatalf("p2c pick with all candidates tried error = nil, auth = %v", auth)
	}
	if auth != nil {
		t.Fatalf("p2c pick with all candidates tried auth = %v, want nil", auth)
	}
}

// TestSchedulerStrategyP2C_RespectsTriedPredicate pins that the tried set
// removes candidates before sampling: the excluded lowest-in-flight auth is
// never returned even though it would win every pair.
func TestSchedulerStrategyP2C_RespectsTriedPredicate(t *testing.T) {
	t.Parallel()

	const model = "p2c-tried-model"
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"p2c-tired": 0, "p2c-rested-b": 4, "p2c-rested-c": 6},
		&Auth{ID: "p2c-tired", Provider: "gemini"},
		&Auth{ID: "p2c-rested-b", Provider: "gemini"},
		&Auth{ID: "p2c-rested-c", Provider: "gemini"},
	)

	tried := map[string]struct{}{"p2c-tired": {}}
	for i := 0; i < 50; i++ {
		auth := pickWithStrategy(t, scheduler, schedulerStrategyP2C, model, tried)
		if auth.ID == "p2c-tired" {
			t.Fatalf("p2c pick #%d returned tried auth p2c-tired", i)
		}
	}
}

// TestSchedulerStrategyP2C_PriorityBucketWins pins that p2c never trades away
// the highest priority tier: a busy high-priority credential still beats idle
// lower-priority ones.
func TestSchedulerStrategyP2C_PriorityBucketWins(t *testing.T) {
	t.Parallel()

	const model = "p2c-priority-model"
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"p2c-busy-high": 5},
		&Auth{ID: "p2c-busy-high", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "p2c-idle-low-a", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "p2c-idle-low-b", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
	)

	for i := 0; i < 20; i++ {
		if auth := pickWithStrategy(t, scheduler, schedulerStrategyP2C, model, nil); auth.ID != "p2c-busy-high" {
			t.Fatalf("p2c pick #%d = %q, want the higher-priority p2c-busy-high", i, auth.ID)
		}
	}
}

// TestSchedulerStrategyLeastUsed_PicksMinimumInFlight pins the deterministic
// minimum: with distinct in-flight counts the least-loaded auth wins every
// pick.
func TestSchedulerStrategyLeastUsed_PicksMinimumInFlight(t *testing.T) {
	t.Parallel()

	const model = "least-used-min-model"
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"lu-a": 2, "lu-b": 1, "lu-c": 4},
		&Auth{ID: "lu-a", Provider: "gemini"},
		&Auth{ID: "lu-b", Provider: "gemini"},
		&Auth{ID: "lu-c", Provider: "gemini"},
	)

	for i := 0; i < 10; i++ {
		if auth := pickWithStrategy(t, scheduler, schedulerStrategyLeastUsed, model, nil); auth.ID != "lu-b" {
			t.Fatalf("least-used pick #%d = %q, want lu-b (minimum in-flight)", i, auth.ID)
		}
	}
}

// TestSchedulerStrategyLeastUsed_TieAlternates pins the tie rotation: two
// auths tied at the minimum alternate through the ready-view cursor instead of
// pinning on the first, and the tied pair keeps excluding the busier auth.
func TestSchedulerStrategyLeastUsed_TieAlternates(t *testing.T) {
	t.Parallel()

	const model = "least-used-tie-model"
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"lu-tie-a": 0, "lu-tie-b": 0, "lu-busy": 2},
		&Auth{ID: "lu-tie-a", Provider: "gemini"},
		&Auth{ID: "lu-tie-b", Provider: "gemini"},
		&Auth{ID: "lu-busy", Provider: "gemini"},
	)

	want := []string{"lu-tie-a", "lu-tie-b", "lu-tie-a", "lu-tie-b"}
	for i, wantID := range want {
		auth := pickWithStrategy(t, scheduler, schedulerStrategyLeastUsed, model, nil)
		if auth.ID != wantID {
			t.Fatalf("least-used tie pick #%d = %q, want %q", i, auth.ID, wantID)
		}
	}
}

// TestSchedulerStrategyLeastUsed_PriorityAndTried pins that least-used still
// respects the priority buckets and the tried predicate: a busy high-priority
// auth outranks idle lower tiers, and tried auths are excluded before the
// minimum scan.
func TestSchedulerStrategyLeastUsed_PriorityAndTried(t *testing.T) {
	t.Parallel()

	const model = "least-used-guard-model"
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"lu-busy-high": 9},
		&Auth{ID: "lu-busy-high", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "lu-idle-low", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
	)

	if auth := pickWithStrategy(t, scheduler, schedulerStrategyLeastUsed, model, nil); auth.ID != "lu-busy-high" {
		t.Fatalf("least-used pick = %q, want the higher-priority lu-busy-high", auth.ID)
	}

	const modelTried = "least-used-tried-model"
	schedulerTried := newStrategySchedulerForTest(t, modelTried, map[string]int{"lu-min-tried": 0, "lu-other-b": 3},
		&Auth{ID: "lu-min-tried", Provider: "gemini"},
		&Auth{ID: "lu-other-b", Provider: "gemini"},
	)
	tried := map[string]struct{}{"lu-min-tried": {}}
	for i := 0; i < 5; i++ {
		if auth := pickWithStrategy(t, schedulerTried, schedulerStrategyLeastUsed, modelTried, tried); auth.ID != "lu-other-b" {
			t.Fatalf("least-used tried pick #%d = %q, want lu-other-b", i, auth.ID)
		}
	}
}

// TestSchedulerStrategyLeastUsed_MixedProvidersPicksGlobalMinimum pins the
// multi-provider path: least-used compares in-flight counts across every
// candidate shard, not per provider.
func TestSchedulerStrategyLeastUsed_MixedProvidersPicksGlobalMinimum(t *testing.T) {
	t.Parallel()

	const model = "least-used-mixed-model"
	registerSchedulerModels(t, "claude", model, "lu-m-claude")
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"lu-m-gemini": 5},
		&Auth{ID: "lu-m-gemini", Provider: "gemini"},
	)
	registerSchedulerModels(t, "claude", model, "lu-m-claude")
	scheduler.upsertAuth(&Auth{ID: "lu-m-claude", Provider: "claude"})
	scheduler.adjustInFlight("lu-m-claude", 0)

	for i := 0; i < 5; i++ {
		auth, providerKey, errPick := scheduler.pickMixedWithStrategy(context.Background(), []string{"gemini", "claude"}, model, cliproxyexecutor.Options{}, nil, schedulerStrategyLeastUsed)
		if errPick != nil {
			t.Fatalf("pickMixedWithStrategy() #%d error = %v", i, errPick)
		}
		if auth == nil || auth.ID != "lu-m-claude" || providerKey != "claude" {
			t.Fatalf("pickMixedWithStrategy() #%d = %v/%q, want the idle lu-m-claude/claude", i, auth, providerKey)
		}
	}
}

// TestSchedulerStrategyP2C_MixedProvidersNeverReturnsWorst pins the multi-
// provider p2c path: the busiest credential across all candidate shards is
// never sampled as the winner.
func TestSchedulerStrategyP2C_MixedProvidersNeverReturnsWorst(t *testing.T) {
	t.Parallel()

	const model = "p2c-mixed-model"
	scheduler := newStrategySchedulerForTest(t, model, map[string]int{"p2c-m-a": 0, "p2c-m-b": 1},
		&Auth{ID: "p2c-m-a", Provider: "gemini"},
		&Auth{ID: "p2c-m-b", Provider: "gemini"},
	)
	scheduler.upsertAuth(&Auth{ID: "p2c-m-claude", Provider: "claude"})
	registerSchedulerModels(t, "claude", model, "p2c-m-claude")
	scheduler.adjustInFlight("p2c-m-claude", 9)

	for i := 0; i < 150; i++ {
		auth, _, errPick := scheduler.pickMixedWithStrategy(context.Background(), []string{"gemini", "claude"}, model, cliproxyexecutor.Options{}, nil, schedulerStrategyP2C)
		if errPick != nil {
			t.Fatalf("pickMixedWithStrategy() #%d error = %v", i, errPick)
		}
		if auth == nil || auth.ID == "p2c-m-claude" {
			t.Fatalf("pickMixedWithStrategy() #%d = %v, want never the busiest p2c-m-claude", i, auth)
		}
	}
}

// TestSchedulerStrategyPicks_ConcurrentPicksAreRaceSafe drives both strategies
// through concurrent picks and in-flight accounting; run under -race this pins
// that pick dispatch and counter updates stay race-free under the scheduler
// mutex.
func TestSchedulerStrategyPicks_ConcurrentPicksAreRaceSafe(t *testing.T) {
	t.Parallel()

	const model = "strategy-race-model"
	scheduler := newStrategySchedulerForTest(t, model, nil,
		&Auth{ID: "race-a", Provider: "gemini"},
		&Auth{ID: "race-b", Provider: "gemini"},
		&Auth{ID: "race-c", Provider: "gemini"},
		&Auth{ID: "race-d", Provider: "gemini"},
	)

	known := map[string]bool{"race-a": true, "race-b": true, "race-c": true, "race-d": true}
	strategies := []schedulerStrategy{schedulerStrategyP2C, schedulerStrategyLeastUsed}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			strategy := strategies[worker%len(strategies)]
			for i := 0; i < 50; i++ {
				auth, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, strategy)
				if errPick != nil || auth == nil {
					t.Errorf("pick #%d/%d error = %v, auth = %v", worker, i, errPick, auth)
					return
				}
				if !known[auth.ID] {
					t.Errorf("pick #%d/%d returned unknown auth %q", worker, i, auth.ID)
					return
				}
				scheduler.adjustInFlight(auth.ID, 1)
				scheduler.adjustInFlight(auth.ID, -1)
			}
		}(worker)
	}
	wg.Wait()
}

// TestSchedulerStrategyMapping pins the strategy mapping sites: the new
// selector types map to dedicated scheduler strategies (so the global
// routing.strategy fast path dispatches them), both count as builtin selectors
// (so useSchedulerFastPath stays on), and the canonical metadata spellings
// from pinned pool rows map to the same strategies.
func TestSchedulerStrategyMapping(t *testing.T) {
	t.Parallel()

	if got := selectorStrategy(&P2CSelector{}); got != schedulerStrategyP2C {
		t.Fatalf("selectorStrategy(P2CSelector) = %v, want schedulerStrategyP2C", got)
	}
	if got := selectorStrategy(&LeastUsedSelector{}); got != schedulerStrategyLeastUsed {
		t.Fatalf("selectorStrategy(LeastUsedSelector) = %v, want schedulerStrategyLeastUsed", got)
	}
	if !isBuiltInSelector(&P2CSelector{}) || !isBuiltInSelector(&LeastUsedSelector{}) {
		t.Fatal("isBuiltInSelector rejected the p2c/least-used selectors; the scheduler fast path would be bypassed")
	}

	meta := func(strategy string) map[string]any {
		return map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: strategy}
	}
	if got := routeStrategyFromMetadata(meta("power-of-two-choices")); got != schedulerStrategyP2C {
		t.Fatalf("routeStrategyFromMetadata(power-of-two-choices) = %v, want schedulerStrategyP2C", got)
	}
	if got := routeStrategyFromMetadata(meta("least-used")); got != schedulerStrategyLeastUsed {
		t.Fatalf("routeStrategyFromMetadata(least-used) = %v, want schedulerStrategyLeastUsed", got)
	}
	if got := routeStrategyFromMetadata(meta("P2C")); got != schedulerStrategyP2C {
		t.Fatalf("routeStrategyFromMetadata(P2C) = %v, want schedulerStrategyP2C (case-insensitive)", got)
	}
}
