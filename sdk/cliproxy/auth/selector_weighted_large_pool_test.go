package auth

import (
	"fmt"
	"maps"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// largePoolTestAuths builds synthetic in-memory credentials with sequential IDs so the
// sorted order is stable and obvious. No real credentials or network access are involved.
func largePoolTestAuths(count int, weightEvery int, weight string) []*Auth {
	auths := make([]*Auth, count)
	for i := range auths {
		auths[i] = &Auth{ID: fmt.Sprintf("synth-%04d", i)}
		if weightEvery > 0 && i%weightEvery == 0 {
			auths[i].Attributes = map[string]string{AttributeWeight: weight}
		}
	}
	return auths
}

func largePoolExclude(auths []*Auth, excluded map[string]struct{}) []*Auth {
	subset := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if _, ok := excluded[auth.ID]; !ok {
			subset = append(subset, auth)
		}
	}
	return subset
}

func largePoolState(t *testing.T, selector *WeightedRoundRobinSelector, route string) *weightedPoolState {
	t.Helper()
	state := selector.largeStates[route]
	if state == nil {
		t.Fatalf("missing shared large-pool accumulator for route %q", route)
	}
	return state
}

func largePoolMemberCredit(state *weightedPoolState, id string) (int64, bool) {
	index, ok := state.findMember(id)
	if !ok {
		return 0, false
	}
	return state.members[index].current, true
}

// TestWeightedLargePoolRotatesAcrossPoolWithChurningExclusions reproduces issue #7431 on
// the legacy selection path: with more positive-weight credentials than
// maxSmoothWeightedStateEntries, transient exclusions (tried sets / cooldowns) must not
// collapse smooth weighted round-robin onto the first Auth.ID-sorted credentials.
func TestWeightedLargePoolRotatesAcrossPoolWithChurningExclusions(t *testing.T) {
	const members = 1300
	const rounds = 3
	auths := largePoolTestAuths(members, 0, "")
	selector := &WeightedRoundRobinSelector{}
	const route = "openai-compat:model"

	counts := make(map[string]int, members)
	for pick := 0; pick < members*rounds; pick++ {
		excluded := make(map[string]struct{}, 8)
		for j := 0; j < 8; j++ {
			excluded[auths[(pick*3+j)%members].ID] = struct{}{}
		}
		picked, reservation := selector.reserve(route, largePoolExclude(auths, excluded), "")
		if picked == nil {
			t.Fatalf("pick %d: no auth returned", pick)
		}
		reservation.finish(true)
		counts[picked.ID]++
	}

	distinct := len(counts)
	if distinct < members-5 {
		t.Fatalf("rotation collapsed: %d of %d credentials selected, counts=%v", distinct, members, counts)
	}
	for id, count := range counts {
		if count > rounds+2 {
			t.Fatalf("credential %q picked %d times in %d rounds", id, count, rounds)
		}
	}
	head := 0
	for i := 0; i < 10; i++ {
		head += counts[auths[i].ID]
	}
	if head > members*rounds/20 {
		t.Fatalf("first 10 IDs took %d of %d picks: %v", head, members*rounds, counts)
	}
}

// TestWeightedSchedulerLargePoolRotatesAcrossPoolWithChurningExclusions covers the
// scheduler fast path, which shares the selector's weighted accounting state.
func TestWeightedSchedulerLargePoolRotatesAcrossPoolWithChurningExclusions(t *testing.T) {
	const members = 1300
	const rounds = 3
	const provider = "large-sched"
	weighted := &WeightedRoundRobinSelector{}
	auths := largePoolTestAuths(members, 0, "")
	for i := range auths {
		auths[i].Provider = provider
	}
	scheduler := newSchedulerForTest(weighted, auths...)

	counts := make(map[string]int, members)
	for pick := 0; pick < members*rounds; pick++ {
		tried := make(map[string]struct{}, 8)
		for j := 0; j < 8; j++ {
			tried[auths[(pick*5+j)%members].ID] = struct{}{}
		}
		picked, errPick := scheduler.pickSingle(t.Context(), provider, "", cliproxyexecutor.Options{}, tried)
		if errPick != nil || picked == nil {
			t.Fatalf("pick %d: auth=%v err=%v", pick, picked, errPick)
		}
		counts[picked.ID]++
	}

	if distinct := len(counts); distinct < members-5 {
		t.Fatalf("scheduler rotation collapsed: %d of %d credentials selected", distinct, members)
	}
	head := 0
	for i := 0; i < 10; i++ {
		head += counts[auths[i].ID]
	}
	if head > members*rounds/20 {
		t.Fatalf("first 10 IDs took %d of %d picks", head, members*rounds)
	}
}

// TestWeightedLargePoolTransientExclusionKeepsCredit proves a credential excluded only for
// the current try set or a cooldown retains its accumulated smooth-WRR credit and does not
// win again from zero once the candidate set changes.
func TestWeightedLargePoolTransientExclusionKeepsCredit(t *testing.T) {
	const members = 1100
	auths := largePoolTestAuths(members, 0, "")
	selector := &WeightedRoundRobinSelector{}
	const route = "openai-compat:model"

	picked, reservation := selector.reserve(route, auths, "")
	if picked == nil || picked.ID != auths[0].ID {
		t.Fatalf("initial pick = %v, want first ID-sorted credential", picked)
	}
	reservation.finish(true)
	state := largePoolState(t, selector, route)
	credit, ok := largePoolMemberCredit(state, auths[0].ID)
	if !ok || credit != 1-members {
		t.Fatalf("initial credit for %q = %d (found=%t), want %d", auths[0].ID, credit, ok, 1-members)
	}

	// Exclude the leader on every pick (as a cooldown would) while another exclusion
	// churns; the leader's credit must survive all subset transitions untouched.
	for i := 1; i <= 100; i++ {
		excluded := map[string]struct{}{
			auths[0].ID:         {},
			auths[i%members].ID: {},
		}
		picked, reservation := selector.reserve(route, largePoolExclude(auths, excluded), "")
		if picked == nil {
			t.Fatalf("subset pick %d returned no auth", i)
		}
		reservation.finish(true)
		if picked.ID == auths[0].ID {
			t.Fatalf("pick %d selected an excluded credential", i)
		}
		credit, ok := largePoolMemberCredit(state, auths[0].ID)
		if !ok || credit != 1-members {
			t.Fatalf("credit for transiently excluded %q = %d (found=%t), want %d", auths[0].ID, credit, ok, 1-members)
		}
	}
	if len(state.members) != members {
		t.Fatalf("accumulator membership = %d, want %d (no transient pruning)", len(state.members), members)
	}

	// Returning to the full candidate set must resume rotation, not restart it.
	picked, reservation = selector.reserve(route, auths, "")
	if picked == nil || picked.ID == auths[0].ID {
		t.Fatalf("pick after exclusions = %v, want rotation to continue past %q", picked, auths[0].ID)
	}
	reservation.finish(true)
}

// TestWeightedLargePoolMixedWeightsKeepRatios verifies weight semantics survive the shared
// accumulator: heavier credentials must still receive proportionally more selections.
func TestWeightedLargePoolMixedWeightsKeepRatios(t *testing.T) {
	const members = 1100
	auths := largePoolTestAuths(members, 10, "4")
	selector := &WeightedRoundRobinSelector{}
	const route = "openai-compat:model"

	counts := make(map[string]int)
	const picks = members * 4
	for i := 0; i < picks; i++ {
		picked, reservation := selector.reserve(route, auths, "")
		if picked == nil {
			t.Fatalf("pick %d returned no auth", i)
		}
		reservation.finish(true)
		counts[picked.ID]++
	}
	heavy, light := counts[auths[0].ID], counts[auths[1].ID]
	if light == 0 || heavy < light*3 || heavy > light*5 {
		t.Fatalf("weight ratio lost: heavy=%d light=%d, want ~4:1", heavy, light)
	}
}

// TestWeightedLargePoolSurvivesExactPoolCapacityFlush proves churn in the bounded
// exact-pool cache (which wholesale resets states at capacity) cannot wipe the shared
// large-pool accumulator.
func TestWeightedLargePoolSurvivesExactPoolCapacityFlush(t *testing.T) {
	auths := largePoolTestAuths(1300, 0, "")
	selector := &WeightedRoundRobinSelector{maxKeys: 2}
	const bigRoute = "big:model"

	_, first := selector.reserve(bigRoute, auths, "")
	first.finish(true)
	_, second := selector.reserve(bigRoute, auths, "")
	second.finish(true)
	state := largePoolState(t, selector, bigRoute)
	before := state.currentForTest()

	small := []*Auth{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}}
	for _, subset := range [][]*Auth{small[:2], small[1:], small[0:1]} {
		_, reservation := selector.reserve("small:model", subset, "")
		reservation.finish(true)
	}
	if selector.stateCount > selector.maxKeys {
		t.Fatalf("exact pool cache did not flush: stateCount=%d", selector.stateCount)
	}
	if !maps.Equal(before, state.currentForTest()) {
		t.Fatalf("exact-pool flush wiped large-pool credits: before=%v after=%v", before, state.currentForTest())
	}
	picked, reservation := selector.reserve(bigRoute, auths, "")
	if picked == nil {
		t.Fatal("large pool stopped selecting after flush")
	}
	reservation.finish(true)
}

// TestWeightedForgetAuthPrunesRemovedCredentialLargePool proves a permanently removed
// credential can still be pruned from the shared accumulator, while a transiently
// excluded credential is untouched.
func TestWeightedForgetAuthPrunesRemovedCredentialLargePool(t *testing.T) {
	const members = 1100
	auths := largePoolTestAuths(members, 0, "")
	selector := &WeightedRoundRobinSelector{}
	const route = "openai-compat:model"

	for i := 0; i < 3; i++ {
		picked, reservation := selector.reserve(route, auths, "")
		if picked == nil {
			t.Fatalf("pick %d returned no auth", i)
		}
		reservation.finish(true)
	}
	state := largePoolState(t, selector, route)

	victim := auths[500].ID
	cooldown := auths[700].ID // excluded from the last candidate set only
	_, reservation := selector.reserve(route, largePoolExclude(auths, map[string]struct{}{cooldown: {}}), "")
	reservation.finish(true)

	selector.ForgetAuth(victim)
	if _, ok := largePoolMemberCredit(state, victim); ok {
		t.Fatalf("removed credential %q still tracked", victim)
	}
	if _, ok := largePoolMemberCredit(state, cooldown); !ok {
		t.Fatalf("transiently excluded credential %q was pruned", cooldown)
	}
	if len(state.members) != members-1 {
		t.Fatalf("membership = %d, want %d", len(state.members), members-1)
	}
}

// TestWeightedForgetAuthPrunesExactPools covers permanent-removal pruning for the bounded
// exact-membership pool cache.
func TestWeightedForgetAuthPrunesExactPools(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a"}, {ID: "b"}}
	_, both := weighted.reserve("route", auths, "")
	both.finish(true)
	_, onlyB := weighted.reserve("route", auths[1:], "")
	onlyB.finish(true)

	weighted.ForgetAuth("a")
	for _, state := range weighted.states {
		if state.containsMember("a") {
			t.Fatal("removed credential still tracked by an exact pool")
		}
	}
	if weighted.stateCount != 1 {
		t.Fatalf("stateCount = %d, want 1 surviving pool", weighted.stateCount)
	}
	weightedStateForTest(t, weighted, "route", "b")
}

// TestSmoothWeightedStateLargePoolKeepsTransientlyExcludedCredits pins the map-based
// smooth accumulator (used by the scheduler ready views) to the same invariant: pruning
// scales with the largest pool ever prepared, so a large pool's transient exclusions keep
// their credits while long-deregistered credentials are still eventually pruned.
func TestSmoothWeightedStateLargePoolKeepsTransientlyExcludedCredits(t *testing.T) {
	const members = 2200
	state := &smoothWeightedState{}
	full := make(map[string]int64, members)
	for i := range members {
		full[fmt.Sprintf("w%04d", i)] = 1
	}
	state.prepare(full)
	state.current["w0000"] = -1500

	subset := make(map[string]int64, members/2)
	for i := members / 2; i < members; i++ {
		subset[fmt.Sprintf("w%04d", i)] = 1
	}
	state.prepare(subset)
	if state.current["w0000"] != -1500 {
		t.Fatalf("transiently excluded credit = %d, want -1500", state.current["w0000"])
	}

	// Deregistered credentials disappear permanently; once stale entries outnumber the
	// peak pool, pruning must bound the state again.
	for i := 0; i < 2*members+100; i++ {
		state.prepare(map[string]int64{fmt.Sprintf("churn-%d", i): 1})
	}
	if len(state.current) > state.pruneLimit() || len(state.weights) > state.pruneLimit()+1 {
		t.Fatalf("state grew unbounded: current=%d weights=%d limit=%d", len(state.current), len(state.weights), state.pruneLimit())
	}
	if _, ok := state.current["w0000"]; ok {
		t.Fatal("long-deregistered credential was never pruned")
	}
}

// TestManagerRemoveForgetsLargePoolCredit wires permanent removal end to end: the manager
// must notify the weighted selector so the deregistered credential's credit is pruned.
func TestManagerRemoveForgetsLargePoolCredit(t *testing.T) {
	const members = 1100
	const provider = "large-forget"
	weighted := &WeightedRoundRobinSelector{}
	manager := NewManager(nil, weighted, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	auths := largePoolTestAuths(members, 0, "")
	for _, auth := range auths {
		auth.Provider = provider
		if _, errRegister := manager.Register(WithSkipPersist(t.Context()), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	for i := 0; i < 3; i++ {
		picked, _, errPick := manager.pickNext(t.Context(), provider, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil {
			t.Fatalf("pick %d: auth=%v err=%v", i, picked, errPick)
		}
	}
	state := largePoolState(t, weighted, provider+":")

	victim := auths[members/2].ID
	manager.Remove(t.Context(), victim)
	if _, ok := largePoolMemberCredit(state, victim); ok {
		t.Fatalf("removed credential %q still tracked by the manager's selector", victim)
	}
	if len(state.members) != members-1 {
		t.Fatalf("membership = %d, want %d", len(state.members), members-1)
	}
}

// TestWeightedLargePoolWeightChangeResetsCredits keeps exact-pool semantics for genuine
// configuration changes on the shared accumulator: a configured weight change resets the
// ledger and redistribution follows the new weights.
func TestWeightedLargePoolWeightChangeResetsCredits(t *testing.T) {
	const members = 1100
	auths := largePoolTestAuths(members, 0, "")
	selector := &WeightedRoundRobinSelector{}
	const route = "openai-compat:model"

	picked, reservation := selector.reserve(route, auths, "")
	reservation.finish(true)
	if picked == nil {
		t.Fatal("initial pick returned no auth")
	}
	state := largePoolState(t, selector, route)
	if credit, _ := largePoolMemberCredit(state, auths[1].ID); credit != 1 {
		t.Fatalf("initial peer credit = %d, want 1", credit)
	}

	auths[0].Attributes = map[string]string{AttributeWeight: "5"}
	_, reservation = selector.reserve(route, auths, "")
	reservation.finish(true)
	// A reset ledger charges the fresh pick once: weight-1 peers end at exactly 1.
	// Without the reset they would carry over 1 + 1 = 2.
	credit, ok := largePoolMemberCredit(state, auths[1].ID)
	if !ok || credit != 1 {
		t.Fatalf("peer credit after weight change = %d (found=%t), want 1 from a reset ledger", credit, ok)
	}
	if index, _ := state.findMember(auths[0].ID); state.members[index].weight != 5 {
		t.Fatalf("configured weight was not applied: %d", state.members[index].weight)
	}
}
