package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// newHeadroomSchedulerForTest builds a scheduler over gemini auths with the
// given model registered for every auth ID and seeds in-flight counts via the
// scheduler's adjustInFlight helper. Mirrors newFillFirstSchedulerForTest but
// also wires a HeadroomLookup into the scheduler so the round-2 headroom
// selector can resolve per-auth remaining-quota percentages.
func newHeadroomSchedulerForTest(t *testing.T, model string, headroomLookup HeadroomLookup, counts map[string]int, auths ...*Auth) *authScheduler {
	t.Helper()
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		ids = append(ids, auth.ID)
	}
	registerSchedulerModels(t, "gemini", model, ids...)
	scheduler := newSchedulerForTest(&FillFirstSelector{}, auths...)
	if headroomLookup != nil {
		scheduler.headroomLookup = headroomLookup
	}
	for authID, count := range counts {
		if count != 0 {
			scheduler.adjustInFlight(authID, count)
		}
	}
	return scheduler
}

// pickHeadroomWithStrategy is a thin wrapper around pickSingleWithStrategy
// for the round-2 schedulerStrategyHeadroom. It mirrors pickFillFirstWithStrategy
// and pickWeightedWithStrategy. Reads the scheduler's lastHeadroomExhausted flag
// after each call so tests can assert on the fallback marker.
func pickHeadroomWithStrategy(t *testing.T, scheduler *authScheduler, model string, tried map[string]struct{}) (*Auth, bool) {
	t.Helper()
	auth, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, tried, schedulerStrategyHeadroom)
	if errPick != nil {
		t.Fatalf("pickSingleWithStrategy(headroom) error = %v", errPick)
	}
	if auth == nil {
		t.Fatalf("pickSingleWithStrategy(headroom) auth = nil")
	}
	exhausted := scheduler.lastHeadroomExhausted
	return auth, exhausted
}

// TestSchedulerHeadroom_PicksHighestRemaining is the canonical happy path:
// three ready gemini auths with headroom 10%, 80%, 50%. Headroom must pick
// the 80% one every time. lastHeadroomExhausted must stay false.
func TestSchedulerHeadroom_PicksHighestRemaining(t *testing.T) {
	t.Parallel()

	const model = "headroom-highest-model"
	lookup := StaticHeadroomLookup{
		"hr-low":  10.0,
		"hr-high": 80.0,
		"hr-mid":  50.0,
	}
	scheduler := newHeadroomSchedulerForTest(t, model, lookup, nil,
		&Auth{ID: "hr-low", Provider: "gemini"},
		&Auth{ID: "hr-high", Provider: "gemini"},
		&Auth{ID: "hr-mid", Provider: "gemini"},
	)

	for i := 0; i < 10; i++ {
		auth, exhausted := pickHeadroomWithStrategy(t, scheduler, model, nil)
		if auth.ID != "hr-high" {
			t.Fatalf("headroom pick #%d = %q, want hr-high (80%% headroom)", i, auth.ID)
		}
		if exhausted {
			t.Fatalf("headroom pick #%d exhausted = true, want false (positive headroom present)", i)
		}
	}
}

// TestSchedulerHeadroom_FallsBackToLeastUsedOnExhaustion exercises the
// fallback rule: when every candidate returns 0% headroom, headroom must fall
// back to least-used ordering (smallest in-flight count). lastHeadroomExhausted
// must be set so the X-NixLLM-Decision header (Task 6) can carry the marker.
func TestSchedulerHeadroom_FallsBackToLeastUsedOnExhaustion(t *testing.T) {
	t.Parallel()

	const model = "headroom-exhausted-model"
	lookup := StaticHeadroomLookup{
		"hr-e-a": 0.0,
		"hr-e-b": 0.0,
		"hr-e-c": 0.0,
	}
	scheduler := newHeadroomSchedulerForTest(t, model, lookup, map[string]int{
		"hr-e-a": 5,
		"hr-e-b": 1,
		"hr-e-c": 9,
	},
		&Auth{ID: "hr-e-a", Provider: "gemini"},
		&Auth{ID: "hr-e-b", Provider: "gemini"},
		&Auth{ID: "hr-e-c", Provider: "gemini"},
	)

	for i := 0; i < 10; i++ {
		auth, exhausted := pickHeadroomWithStrategy(t, scheduler, model, nil)
		if auth.ID != "hr-e-b" {
			t.Fatalf("headroom fallback pick #%d = %q, want hr-e-b (least in-flight)", i, auth.ID)
		}
		if !exhausted {
			t.Fatalf("headroom fallback pick #%d exhausted = false, want true (all candidates 0%%)", i)
		}
	}
}

// TestSchedulerHeadroom_TieBreaksByPriority pins the priority tie-break: when
// two auths share the same headroom value, the auth with the higher priority
// attribute wins. The highest-priority bucket is consulted first per the
// existing scheduler convention, so two auths at different priorities never
// collide on headroom; this test pins the within-bucket tie-break by giving
// both auths the same priority and verifying the smaller-ID tie-break still
// keeps the pick deterministic (the pure pickHeadroom helper enforces the
// ID order; the scheduler's priority gate does the cross-bucket separation).
func TestSchedulerHeadroom_TieBreaksByPriority(t *testing.T) {
	t.Parallel()

	const model = "headroom-tie-model"
	lookup := StaticHeadroomLookup{
		"hr-t-low":  42.0,
		"hr-t-high": 42.0,
	}
	// Both auths at the same priority so the within-bucket tie-break governs.
	scheduler := newHeadroomSchedulerForTest(t, model, lookup, nil,
		&Auth{ID: "hr-t-low", Provider: "gemini"},
		&Auth{ID: "hr-t-high", Provider: "gemini"},
	)

	for i := 0; i < 10; i++ {
		auth, exhausted := pickHeadroomWithStrategy(t, scheduler, model, nil)
		// Same headroom (42.0) on both — the pure helper tie-breaks by the
		// smaller auth.ID. Both share the same priority bucket so the
		// scheduler's priority gate does not pre-select. Alphabetically
		// "hr-t-high" < "hr-t-low", so the smaller-ID rule picks hr-t-high.
		if auth.ID != "hr-t-high" {
			t.Fatalf("headroom tie pick #%d = %q, want hr-t-high (smaller auth.ID)", i, auth.ID)
		}
		if exhausted {
			t.Fatalf("headroom tie pick #%d exhausted = true, want false (positive headroom present)", i)
		}
	}

	// The priority tie-break manifests at the scheduler layer: when one auth
	// has a strictly higher priority than the other, the lower-priority auth
	// is never selected. This pins that the priority gate runs ahead of the
	// headroom scan (consistent with the rest of the scheduler).
	lookupWithPriority := StaticHeadroomLookup{
		"hr-pri-low":  90.0,
		"hr-pri-high": 10.0,
	}
	authLow := &Auth{ID: "hr-pri-low", Provider: "gemini"}
	authHigh := &Auth{ID: "hr-pri-high", Provider: "gemini", Attributes: map[string]string{"priority": "10"}}
	const priorityModel = "headroom-priority-model"
	schedulerPri := newHeadroomSchedulerForTest(t, priorityModel, lookupWithPriority, nil, authLow, authHigh)

	for i := 0; i < 10; i++ {
		auth, _ := pickHeadroomWithStrategy(t, schedulerPri, priorityModel, nil)
		if auth.ID != "hr-pri-high" {
			t.Fatalf("headroom priority pick #%d = %q, want hr-pri-high (higher priority outranks 90%% headroom)", i, auth.ID)
		}
	}
}
