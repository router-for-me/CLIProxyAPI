package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// alwaysZeroMaxParallel returns a max-parallel lookup that treats every auth as
// uncapped. Tests that need a per-auth cap build their own lookup and pass it
// into pickFillFirst directly; this helper covers the "no cap" production path
// in which fill-first keeps filling whichever auth has the highest in-flight
// count, only ever backing off because of the cap (which never trips here).
func alwaysZeroMaxParallel(string) int { return 0 }

// newFillFirstSchedulerForTest builds a scheduler over gemini auths with the
// given in-flight counts seeded per auth. Mirrors newStrategySchedulerForTest
// but keeps the API minimal (no model registration needed for fill-first
// because the helper itself only inspects in-flight + cap, not the model shard
// state). For end-to-end scheduler pick tests we still need the registered
// model; that path uses newStrategySchedulerForTest.
func newFillFirstSchedulerForTest(t *testing.T, model string, counts map[string]int, auths ...*Auth) *authScheduler {
	t.Helper()
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		ids = append(ids, auth.ID)
	}
	registerSchedulerModels(t, "gemini", model, ids...)
	scheduler := newSchedulerForTest(&FillFirstSelector{}, auths...)
	for authID, count := range counts {
		if count != 0 {
			scheduler.adjustInFlight(authID, count)
		}
	}
	return scheduler
}

// pickFillFirstWithStrategy is a thin wrapper around pickSingleWithStrategy
// that fails the test on errors, mirroring pickWithStrategy in
// scheduler_strategy_p2c_test.go.
func pickFillFirstWithStrategy(t *testing.T, scheduler *authScheduler, model string, tried map[string]struct{}) *Auth {
	t.Helper()
	auth, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, tried, schedulerStrategyFillFirst)
	if errPick != nil {
		t.Fatalf("pickSingleWithStrategy(fill-first) error = %v", errPick)
	}
	if auth == nil {
		t.Fatalf("pickSingleWithStrategy(fill-first) auth = nil")
	}
	return auth
}

// TestSchedulerFillFirst_PicksHighestInFlightBelowCap is the canonical happy
// path: three ready auths at the same priority, in-flight counts 0, 5, 2.
// Fill-first must pick the auth with in-flight=5 (highest below cap).
func TestSchedulerFillFirst_PicksHighestInFlightBelowCap(t *testing.T) {
	t.Parallel()

	const model = "ff-highest-model"
	scheduler := newFillFirstSchedulerForTest(t, model, map[string]int{"ff-a": 0, "ff-b": 5, "ff-c": 2},
		&Auth{ID: "ff-a", Provider: "gemini"},
		&Auth{ID: "ff-b", Provider: "gemini"},
		&Auth{ID: "ff-c", Provider: "gemini"},
	)

	for i := 0; i < 10; i++ {
		auth := pickFillFirstWithStrategy(t, scheduler, model, nil)
		if auth.ID != "ff-b" {
			t.Fatalf("fill-first pick #%d = %q, want ff-b (highest in-flight below cap)", i, auth.ID)
		}
	}
}

// TestSchedulerFillFirst_RespectsMaxParallel pins the cap semantic: when the
// highest-in-flight candidate is exactly at its cap, fill-first must skip it
// and pick the next-highest in-flight candidate. The cap is supplied per test
// via the scheduler's max-parallel lookup (filled in via the production helper
// when the attribute is set on the auth).
func TestSchedulerFillFirst_RespectsMaxParallel(t *testing.T) {
	t.Parallel()

	const model = "ff-cap-model"
	authA := &Auth{ID: "ff-cap-a", Provider: "gemini", Attributes: map[string]string{"max_parallel": "3"}}
	authB := &Auth{ID: "ff-cap-b", Provider: "gemini"}
	authC := &Auth{ID: "ff-cap-c", Provider: "gemini"}
	scheduler := newFillFirstSchedulerForTest(t, model, map[string]int{"ff-cap-a": 3, "ff-cap-b": 2, "ff-cap-c": 0},
		authA, authB, authC,
	)

	// ff-cap-a is at cap (3 of 3); fill-first must skip it and pick ff-cap-b
	// (in-flight=2), not ff-cap-a.
	for i := 0; i < 10; i++ {
		auth := pickFillFirstWithStrategy(t, scheduler, model, nil)
		if auth.ID != "ff-cap-b" {
			t.Fatalf("fill-first cap pick #%d = %q, want ff-cap-b (highest below cap)", i, auth.ID)
		}
	}
}

// TestSchedulerFillFirst_TieBreaksByAuthID pins the deterministic tie-break:
// two auths share the same in-flight count and both are below cap; the auth
// with the smaller ID wins every pick (consistent with the round-1
// stable-by-ID ordering used elsewhere in the scheduler).
func TestSchedulerFillFirst_TieBreaksByAuthID(t *testing.T) {
	t.Parallel()

	const model = "ff-tie-model"
	scheduler := newFillFirstSchedulerForTest(t, model, map[string]int{"ff-tie-a": 4, "ff-tie-b": 4, "ff-tie-c": 1},
		&Auth{ID: "ff-tie-a", Provider: "gemini"},
		&Auth{ID: "ff-tie-b", Provider: "gemini"},
		&Auth{ID: "ff-tie-c", Provider: "gemini"},
	)

	for i := 0; i < 10; i++ {
		auth := pickFillFirstWithStrategy(t, scheduler, model, nil)
		if auth.ID != "ff-tie-a" {
			t.Fatalf("fill-first tie pick #%d = %q, want ff-tie-a (smaller auth.ID)", i, auth.ID)
		}
	}
}

// TestSchedulerFillFirst_PurePickHelper pins the unit-level behavior of
// pickFillFirst with explicit entry slices. Mirrors the resolvePowerOfTwoPair
// style in scheduler_strategy_p2c_test.go: bypass the scheduler pick path and
// drive the helper directly so the cap/skip/tie-break rules are readable in
// isolation.
func TestSchedulerFillFirst_PurePickHelper(t *testing.T) {
	t.Parallel()

	mkEntry := func(id string) *scheduledAuth {
		return &scheduledAuth{auth: &Auth{ID: id}}
	}
	inFlight := func(id string) int {
		switch id {
		case "high":
			return 5
		case "mid":
			return 2
		case "low":
			return 0
		}
		return 0
	}
	maxParallel := func(string) int { return 0 } // uncapped

	// Highest in-flight wins when every entry is below cap.
	entries := []*scheduledAuth{mkEntry("low"), mkEntry("high"), mkEntry("mid")}
	if got := pickFillFirst(entries, inFlight, maxParallel); got.auth.ID != "high" {
		t.Fatalf("pickFillFirst lowest-first input = %q, want high", got.auth.ID)
	}

	// Cap trips high; mid takes over.
	withCap := func(id string) int {
		switch id {
		case "high":
			return 5
		case "mid":
			return 5
		}
		return 10
	}
	if got := pickFillFirst(entries, inFlight, withCap); got.auth.ID != "mid" {
		t.Fatalf("pickFillFirst with cap on high = %q, want mid", got.auth.ID)
	}

	// Equal in-flight falls back to the smaller auth.ID.
	tied := []*scheduledAuth{mkEntry("zz"), mkEntry("aa"), mkEntry("mm")}
	tiedInFlight := func(id string) int {
		switch id {
		case "zz", "aa", "mm":
			return 7
		}
		return 0
	}
	if got := pickFillFirst(tied, tiedInFlight, maxParallel); got.auth.ID != "aa" {
		t.Fatalf("pickFillFirst tied = %q, want aa (smaller auth.ID)", got.auth.ID)
	}

	// Empty entries return nil without panicking.
	if got := pickFillFirst(nil, inFlight, maxParallel); got != nil {
		t.Fatalf("pickFillFirst(nil) = %v, want nil", got)
	}
}
