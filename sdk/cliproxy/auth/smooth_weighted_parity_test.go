package auth

import (
	"testing"
)

// buildParityAuths returns one auth per (id, weight) pair, in the given order.
func buildParityAuths(t *testing.T, ids []string, weights map[string]string) []*Auth {
	t.Helper()
	auths := make([]*Auth, 0, len(ids))
	for _, id := range ids {
		auths = append(auths, &Auth{
			ID:         id,
			Provider:   "parity",
			Attributes: map[string]string{AttributeWeight: weights[id]},
		})
	}
	return auths
}

// buildParityEntries wraps the same logical candidates as scheduler entries so
// pickSmoothWeightedScheduled sees the identical (id, weight) sequence that
// pickSmoothWeightedAuth sees.
func buildParityEntries(t *testing.T, ids []string, weights map[string]string) []*scheduledAuth {
	t.Helper()
	entries := make([]*scheduledAuth, 0, len(ids))
	for _, id := range ids {
		metaWeight := authWeight(&Auth{ID: id, Provider: "parity", Attributes: map[string]string{AttributeWeight: weights[id]}})
		entries = append(entries, &scheduledAuth{
			meta: &scheduledAuthMeta{
				auth: &Auth{ID: id, Provider: "parity"},
				// authWeight reads weight off Attributes; give the meta the same value.
				weight: metaWeight,
			},
			auth: &Auth{ID: id, Provider: "parity"},
		})
	}
	return entries
}

// TestSmoothWeightedParity_SamePickOrder pins audit item B4/P4 step 1: the
// selector-side (pickSmoothWeightedAuth) and scheduler-side
// (pickSmoothWeightedScheduled) smooth WRR copies must produce the same pick
// order over identical candidate sequences. Both copies exist because the
// selector sees []*Auth and the scheduler sees []*scheduledAuth; this test is
// the documented equivalence that lets them be consolidated onto one helper.
func TestSmoothWeightedParity_SamePickOrder(t *testing.T) {
	ids := []string{"a", "b", "c"}
	weights := map[string]string{"a": "5", "b": "3", "c": "2"}

	selectorCurrent := map[string]int64{}
	schedulerCurrent := map[string]int64{}

	selectorAuths := buildParityAuths(t, ids, weights)
	schedulerEntries := buildParityEntries(t, ids, weights)

	// One full WRR cycle re-distributes picks proportional to weight; run two
	// cycles so the wrap-around state is compared too.
	const picks = 20
	for i := 0; i < picks; i++ {
		pickedAuth := pickSmoothWeightedAuth(selectorAuths, selectorCurrent)
		pickedEntry := pickSmoothWeightedScheduled(schedulerEntries, schedulerCurrent, nil)
		if pickedAuth == nil || pickedEntry == nil {
			t.Fatalf("pick #%d: nil pick (auth=%v entry=%v)", i, pickedAuth, pickedEntry)
		}
		if pickedAuth.ID != pickedEntry.auth.ID {
			t.Fatalf("pick #%d: selector picked %q, scheduler picked %q — smooth WRR copies diverged",
				i, pickedAuth.ID, pickedEntry.auth.ID)
		}
	}

	// Distribution must be exactly proportional over the cycle.
	want := map[string]int{"a": 10, "b": 6, "c": 4}
	counts := map[string]int{}
	for i := 0; i < picks; i++ {
		picked := pickSmoothWeightedAuth(selectorAuths, selectorCurrent)
		counts[picked.ID]++
		_ = pickSmoothWeightedScheduled(schedulerEntries, schedulerCurrent, nil)
	}
	for id, wantCount := range want {
		if counts[id] != wantCount {
			t.Fatalf("auth %q picks = %d, want %d", id, counts[id], wantCount)
		}
	}
}

// TestSmoothWeightedParity_InactiveCleanup pins the cleanup-semantics
// equivalence: when a candidate disappears mid-stream, both implementations
// must drop its accumulated current-value and continue with the survivors —
// the selector cleans after accumulating, the scheduler cleans before, and
// for the surviving candidates the resulting pick order must still agree.
func TestSmoothWeightedParity_InactiveCleanup(t *testing.T) {
	weights := map[string]string{"a": "5", "b": "3", "c": "2"}

	// Warm up with all three candidates so each has accumulated current state.
	selectorCurrent := map[string]int64{}
	schedulerCurrent := map[string]int64{}
	selectorAuths := buildParityAuths(t, []string{"a", "b", "c"}, weights)
	schedulerEntries := buildParityEntries(t, []string{"a", "b", "c"}, weights)
	for i := 0; i < 3; i++ {
		pickSmoothWeightedAuth(selectorAuths, selectorCurrent)
		pickSmoothWeightedScheduled(schedulerEntries, schedulerCurrent, nil)
	}

	// "c" goes inactive: the selector drops it via a filtered auths slice; the
	// scheduler drops it via the predicate. Both must stop tracking "c" in
	// current and keep agreeing on picks.
	selectorAuths = buildParityAuths(t, []string{"a", "b"}, weights)
	schedulerEntries = buildParityEntries(t, []string{"a", "b", "c"}, weights)
	isC := func(entry *scheduledAuth) bool { return entry.auth.ID == "c" }
	predicate := func(entry *scheduledAuth) bool { return !isC(entry) }

	for i := 0; i < 10; i++ {
		pickedAuth := pickSmoothWeightedAuth(selectorAuths, selectorCurrent)
		pickedEntry := pickSmoothWeightedScheduled(schedulerEntries, schedulerCurrent, predicate)
		if pickedAuth == nil || pickedEntry == nil {
			t.Fatalf("pick #%d: nil pick (auth=%v entry=%v)", i, pickedAuth, pickedEntry)
		}
		if pickedAuth.ID != pickedEntry.auth.ID {
			t.Fatalf("pick #%d: selector picked %q, scheduler picked %q after inactivation",
				i, pickedAuth.ID, pickedEntry.auth.ID)
		}
	}
	if _, stillTracked := selectorCurrent["c"]; stillTracked {
		t.Fatal("selector current still tracks inactive \"c\"")
	}
	if _, stillTracked := schedulerCurrent["c"]; stillTracked {
		t.Fatal("scheduler current still tracks inactive \"c\"")
	}
}
