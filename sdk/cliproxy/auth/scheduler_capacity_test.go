package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// capacityAuth builds a gemini auth with the per-entry max_parallel cap
// stamped as the auth attribute the scheduler reads (AttributeMaxParallel).
// maxParallel <= 0 leaves the attribute unset (unlimited).
func capacityAuth(id string, maxParallel int) *Auth {
	attrs := map[string]string{}
	if maxParallel > 0 {
		attrs[AttributeMaxParallel] = strconv.Itoa(maxParallel)
	}
	return &Auth{ID: id, Provider: "gemini", Attributes: attrs}
}

// capacityAuthAt is capacityAuth plus extra attributes (e.g. priority).
func capacityAuthAt(id string, maxParallel int, extra map[string]string) *Auth {
	auth := capacityAuth(id, maxParallel)
	for key, value := range extra {
		auth.Attributes[key] = value
	}
	return auth
}

// capacitySchedulerForTest builds a round-robin scheduler over gemini auths
// with the given in-flight counts seeded per auth. Mirrors
// newStrategySchedulerForTest in scheduler_strategy_p2c_test.go (same package).
func capacitySchedulerForTest(t *testing.T, model string, counts map[string]int, auths ...*Auth) *authScheduler {
	t.Helper()
	return newStrategySchedulerForTest(t, model, counts, auths...)
}

// TestSchedulerCapacity_RoundRobinSkipsOverCapEntry is the core Task-4
// behavior: two entries capped at max_parallel=1 with the first already in
// flight, round-robin must return the second. Today the round-1 pickers ignore
// in-flight counts for eligibility and would keep returning the busy first
// entry. FAILS before Task 4, passes after.
func TestSchedulerCapacity_RoundRobinSkipsOverCapEntry(t *testing.T) {
	t.Parallel()

	const model = "capacity-rr-model"
	scheduler := capacitySchedulerForTest(t, model,
		map[string]int{"cap-first": 1},
		capacityAuth("cap-first", 1),
		capacityAuth("cap-second", 1),
	)

	got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick != nil {
		t.Fatalf("pickSingleWithStrategy(round-robin) error = %v", errPick)
	}
	if got == nil || got.ID != "cap-second" {
		t.Fatalf("pick = %v, want cap-second (the entry below its concurrency cap)", got)
	}
}

// TestSchedulerCapacity_AllEntriesOverCapSignalsCapacity pins requirement #4:
// when every eligible candidate is at/over its cap, the pick must surface the
// internal entry-capacity signal (Task 5 translates it into a short wait), NOT
// a plain auth_not_found / auth_unavailable and NOT a successful pick.
func TestSchedulerCapacity_AllEntriesOverCapSignalsCapacity(t *testing.T) {
	t.Parallel()

	const model = "capacity-overcap-model"
	scheduler := capacitySchedulerForTest(t, model,
		map[string]int{"cap-a": 1, "cap-b": 1},
		capacityAuth("cap-a", 1),
		capacityAuth("cap-b", 1),
	)

	got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick == nil {
		t.Fatalf("pickSingleWithStrategy() error = nil, auth = %v; want capacity signal", got)
	}
	if got != nil {
		t.Fatalf("pickSingleWithStrategy() auth = %v, want nil", got)
	}
	if !isEntryCapacityError(errPick) {
		t.Fatalf("pickSingleWithStrategy() error = %v, want entry-capacity signal (isEntryCapacityError)", errPick)
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr == nil {
		t.Fatalf("pickSingleWithStrategy() error = %T %v, want *Error", errPick, errPick)
	}
	if authErr.Code != entryCapacityErrorCode {
		t.Fatalf("pickSingleWithStrategy() error code = %q, want %q (not a plain auth error)", authErr.Code, entryCapacityErrorCode)
	}
	if !authErr.Retryable {
		t.Fatalf("pickSingleWithStrategy() capacity signal Retryable = false, want true (Task 5 waits then re-picks)")
	}
	if authErr.HTTPStatus != 503 {
		t.Fatalf("pickSingleWithStrategy() capacity signal HTTPStatus = %d, want 503 (D6: identical to the pre-existing busy shape in the interim)", authErr.HTTPStatus)
	}
}

// TestSchedulerCapacity_FillFirstSkipsOverCapPicksBelowCap asserts no
// regression on the round-2 fill-first selector: an entry at cap plus one
// below cap keeps returning the below-cap entry (fill-first has been cap-aware
// since the round-2 work; Task 4 must not break it).
func TestSchedulerCapacity_FillFirstSkipsOverCapPicksBelowCap(t *testing.T) {
	t.Parallel()

	const model = "capacity-ff-model"
	scheduler := capacitySchedulerForTest(t, model,
		map[string]int{"cap-busy": 1},
		capacityAuth("cap-busy", 1),
		capacityAuth("cap-free", 1),
	)

	for i := 0; i < 5; i++ {
		got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyFillFirst)
		if errPick != nil {
			t.Fatalf("pickSingleWithStrategy(fill-first) #%d error = %v", i, errPick)
		}
		if got == nil || got.ID != "cap-free" {
			t.Fatalf("fill-first pick #%d = %v, want cap-free (the below-cap entry)", i, got)
		}
	}
}

// TestSchedulerCapacity_UnsetCapMeansUnlimited pins requirement #4: with no
// max_parallel attribute, in-flight counts never make an entry over-cap and no
// capacity signal is ever emitted even when every candidate is in flight.
func TestSchedulerCapacity_UnsetCapMeansUnlimited(t *testing.T) {
	t.Parallel()

	const model = "capacity-unlimited-model"
	scheduler := capacitySchedulerForTest(t, model,
		map[string]int{"uncap-a": 2, "uncap-b": 2},
		&Auth{ID: "uncap-a", Provider: "gemini"},
		&Auth{ID: "uncap-b", Provider: "gemini"},
	)

	for i := 0; i < 4; i++ {
		got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
		if errPick != nil {
			t.Fatalf("pickSingleWithStrategy() #%d error = %v, want no capacity signal for uncapped auths", i, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingleWithStrategy() #%d auth = nil, want one of the uncapped auths", i)
		}
	}
}

// TestSchedulerCapacity_ReleaseFreesSlot pins requirement #5: after the
// in-flight count drops back below the cap (a release), the over-cap entry
// becomes eligible again and the pick returns to it.
func TestSchedulerCapacity_ReleaseFreesSlot(t *testing.T) {
	t.Parallel()

	const model = "capacity-release-model"
	scheduler := capacitySchedulerForTest(t, model,
		map[string]int{"cap-held": 1},
		capacityAuth("cap-held", 1),
		capacityAuth("cap-peer", 1),
	)

	// cap-held is at cap (1/1); round-robin serves the below-cap peer.
	got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick != nil || got == nil || got.ID != "cap-peer" {
		t.Fatalf("first pick = %v err = %v, want cap-peer", got, errPick)
	}

	// Now both entries are at cap → capacity signal, not a busy-but-picked auth.
	scheduler.adjustInFlight("cap-peer", 1)
	_, errPick = scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick == nil || !isEntryCapacityError(errPick) {
		t.Fatalf("pick with both entries at cap error = %v, want capacity signal", errPick)
	}

	// Release the first entry's slot → it becomes eligible again and serves.
	scheduler.adjustInFlight("cap-held", -1)
	got, errPick = scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick != nil || got == nil || got.ID != "cap-held" {
		t.Fatalf("pick after release = %v err = %v, want cap-held (re-eligible)", got, errPick)
	}
}

// TestSchedulerCapacity_PriorityFallsThroughWhenHighBucketOverCap pins
// requirement #6: a lower-priority entry serves only when the higher-priority
// bucket is fully over cap; the moment the high entry frees up it serves again
// (no regression on priority-aware selection).
func TestSchedulerCapacity_PriorityFallsThroughWhenHighBucketOverCap(t *testing.T) {
	t.Parallel()

	const model = "capacity-priority-model"
	scheduler := capacitySchedulerForTest(t, model,
		map[string]int{"cap-high": 1},
		capacityAuthAt("cap-high", 1, map[string]string{"priority": "10"}),
		capacityAuthAt("cap-low", 1, map[string]string{"priority": "0"}),
	)

	// High-priority entry is at cap → the lower-priority free entry serves.
	got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick != nil {
		t.Fatalf("pickSingleWithStrategy() error = %v", errPick)
	}
	if got == nil || got.ID != "cap-low" {
		t.Fatalf("pick with high bucket over cap = %v, want cap-low (lower priority serves)", got)
	}

	// Release the high entry → it is eligible again and its priority wins.
	scheduler.adjustInFlight("cap-high", -1)
	got, errPick = scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick != nil {
		t.Fatalf("pickSingleWithStrategy() after release error = %v", errPick)
	}
	if got == nil || got.ID != "cap-high" {
		t.Fatalf("pick after high release = %v, want cap-high (priority restored)", got)
	}
}

// TestSchedulerCapacity_P2CAndLeastUsedNeverPickOverCapEntry pins that the
// in-flight-aware strategies skip an over-cap entry even when it is less busy
// than a below-cap candidate: an entry at its cap (in-flight 1/1) must lose to
// an uncapped entry carrying more in-flight (2), because a capped entry at its
// limit is not eligible regardless of raw load.
func TestSchedulerCapacity_P2CAndLeastUsedNeverPickOverCapEntry(t *testing.T) {
	for _, strategy := range []schedulerStrategy{schedulerStrategyP2C, schedulerStrategyLeastUsed} {
		name := "p2c"
		if strategy == schedulerStrategyLeastUsed {
			name = "least-used"
		}
		t.Run(name, func(t *testing.T) {
			const model = "capacity-p2c-lu-model"
			scheduler := capacitySchedulerForTest(t, model,
				map[string]int{"cap-overloaded": 1, "uncap-loaded": 2},
				capacityAuth("cap-overloaded", 1),
				&Auth{ID: "uncap-loaded", Provider: "gemini"},
			)

			for i := 0; i < 10; i++ {
				got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, strategy)
				if errPick != nil {
					t.Fatalf("pickSingleWithStrategy(%s) #%d error = %v", name, i, errPick)
				}
				// cap-overloaded is at cap (1/1) → must be skipped even though it
				// has fewer in-flight than the uncapped entry.
				if got == nil || got.ID != "uncap-loaded" {
					t.Fatalf("%s pick #%d = %v, want uncap-loaded (over-cap entry skipped)", name, i, got)
				}
			}
		})
	}
}

// TestSchedulerCapacity_WeightedSkipsOverCapEntry pins that both weighted
// selectors skip an over-cap entry even when it carries the dominant weight:
// a high-weight entry at its cap must not win over a free low-weight entry.
func TestSchedulerCapacity_WeightedSkipsOverCapEntry(t *testing.T) {
	for _, strategy := range []schedulerStrategy{schedulerStrategyWeighted, schedulerStrategyWeightedRoundRobin} {
		name := "weighted"
		if strategy == schedulerStrategyWeightedRoundRobin {
			name = "weighted-round-robin"
		}
		t.Run(name, func(t *testing.T) {
			const model = "capacity-weighted-model"
			scheduler := capacitySchedulerForTest(t, model,
				map[string]int{"cap-heavy": 1},
				capacityAuthAt("cap-heavy", 1, map[string]string{AttributeWeight: "100"}),
				capacityAuthAt("cap-light", 1, map[string]string{AttributeWeight: "1"}),
			)

			for i := 0; i < 10; i++ {
				got, errPick := scheduler.pickSingleWithStrategy(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil, strategy)
				if errPick != nil {
					t.Fatalf("pickSingleWithStrategy(%s) #%d error = %v", name, i, errPick)
				}
				if got == nil || got.ID != "cap-light" {
					t.Fatalf("%s pick #%d = %v, want cap-light (over-cap heavy entry skipped)", name, i, got)
				}
			}
		})
	}
}

// TestSchedulerCapacity_MixedProvidersServesBelowCapProvider pins the mixed-
// provider path: a provider whose only candidate is over cap is excluded from
// the provider rotation so the other provider's free entry serves, and no
// capacity signal leaks while a below-cap candidate exists somewhere.
func TestSchedulerCapacity_MixedProvidersServesBelowCapProvider(t *testing.T) {
	t.Parallel()

	const model = "capacity-mixed-model"
	registerSchedulerModels(t, "gemini", model, "m-gemini")
	registerSchedulerModels(t, "claude", model, "m-claude")
	scheduler := newSchedulerForTest(&RoundRobinSelector{},
		capacityAuth("m-gemini", 1),
		&Auth{ID: "m-claude", Provider: "claude"},
	)
	scheduler.adjustInFlight("m-gemini", 1)

	for i := 0; i < 5; i++ {
		got, providerKey, errPick := scheduler.pickMixedWithStrategy(context.Background(), []string{"gemini", "claude"}, model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
		if errPick != nil {
			t.Fatalf("pickMixedWithStrategy() #%d error = %v", i, errPick)
		}
		if got == nil || got.ID != "m-claude" || providerKey != "claude" {
			t.Fatalf("pickMixedWithStrategy() #%d = %v/%q, want m-claude/claude (over-cap gemini excluded)", i, got, providerKey)
		}
	}
}

// TestSchedulerCapacity_MixedProvidersAllOverCapSignalsCapacity pins that the
// capacity signal also surfaces on the mixed-provider path when every provider
// has only over-cap ready candidates.
func TestSchedulerCapacity_MixedProvidersAllOverCapSignalsCapacity(t *testing.T) {
	t.Parallel()

	const model = "capacity-mixed-overcap-model"
	registerSchedulerModels(t, "gemini", model, "m-gemini")
	registerSchedulerModels(t, "claude", model, "m-claude")
	scheduler := newSchedulerForTest(&RoundRobinSelector{},
		capacityAuth("m-gemini", 1),
		capacityAuth("m-claude", 1),
	)
	scheduler.adjustInFlight("m-gemini", 1)
	scheduler.adjustInFlight("m-claude", 1)

	got, providerKey, errPick := scheduler.pickMixedWithStrategy(context.Background(), []string{"gemini", "claude"}, model, cliproxyexecutor.Options{}, nil, schedulerStrategyRoundRobin)
	if errPick == nil {
		t.Fatalf("pickMixedWithStrategy() error = nil, auth = %v/%q; want capacity signal", got, providerKey)
	}
	if got != nil || providerKey != "" {
		t.Fatalf("pickMixedWithStrategy() auth = %v/%q, want nil/empty", got, providerKey)
	}
	if !isEntryCapacityError(errPick) {
		t.Fatalf("pickMixedWithStrategy() error = %v, want capacity signal", errPick)
	}
}
