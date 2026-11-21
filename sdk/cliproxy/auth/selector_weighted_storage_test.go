package auth

import (
	"maps"
	"testing"
)

// These snapshots keep assertions independent of the compact production layout.
func (s *weightedPoolState) currentForTest() map[string]int64 {
	current := make(map[string]int64, len(s.members))
	for _, member := range s.members {
		current[member.id] = member.current
	}
	return current
}

func (s *weightedPoolState) pendingForTest() map[string]int64 {
	pending := make(map[string]int64)
	for _, member := range s.members {
		if member.pending != 0 {
			pending[member.id] = member.pending
		}
	}
	return pending
}

func (s *weightedPoolState) hasCurrentForTest() bool {
	for _, member := range s.members {
		if member.current != 0 {
			return true
		}
	}
	return false
}

// TestWeightedPoolDigestCollisionIsolation forces digest hits for different exact
// identities, including a matching state behind a collision-chain head.
func TestWeightedPoolDigestCollisionIsolation(t *testing.T) {
	for _, unrelated := range []*weightedPoolState{
		weightedPoolForTest("other-route", map[string]int64{"a": 1, "b": 1}, map[string]int64{"a": 7, "b": -7}),
		weightedPoolForTest("route", map[string]int64{"a": 1, "c": 1}, map[string]int64{"a": 7, "c": -7}),
		weightedPoolForTest("route", map[string]int64{"a": 1}, map[string]int64{"a": 7}),
	} {
		t.Run(unrelated.route+"/"+unrelated.members[len(unrelated.members)-1].id, func(t *testing.T) {
			weights := map[string]int64{"a": 1, "b": 1}
			key := weightedPoolStateKey("route", weights)
			selector := &WeightedRoundRobinSelector{
				states: map[[32]byte]*weightedPoolState{key: unrelated}, stateCount: 1, maxKeys: 2,
			}
			before := unrelated.currentForTest()
			picked, first := selector.reserve("route", []*Auth{{ID: "b"}, {ID: "a"}}, "b")
			if picked == nil || picked.ID != "b" || first == nil || first.state == unrelated {
				t.Fatal("digest collision reused an unrelated pool")
			}
			t.Cleanup(func() { first.finish(false) })
			matching := first.state
			// Put the nonmatching state first to exercise a full chain lookup.
			matching.next, unrelated.next, selector.states[key] = nil, matching, unrelated
			_, second := selector.reserve("route", []*Auth{{ID: "a"}, {ID: "b"}}, "a")
			if second == nil || second.state != matching || selector.stateCount != 2 {
				t.Fatal("collision lookup failed to reuse the matching pool")
			}
			t.Cleanup(func() { second.finish(false) })
			first.finish(true)
			second.finish(true)
			if !maps.Equal(before, unrelated.currentForTest()) || unrelated.inFlight != 0 || len(unrelated.pendingForTest()) != 0 {
				t.Fatal("collision accounting changed the unrelated pool")
			}
			if matching.hasCurrentForTest() || matching.inFlight != 0 || len(matching.pendingForTest()) != 0 {
				t.Fatal("matching pool did not balance")
			}
			// Capacity still counts pools, not just distinct digest buckets.
			_, third := selector.reserve("third-route", []*Auth{{ID: "a"}}, "")
			third.finish(true)
			if selector.stateCount != 1 || len(selector.states) != 1 {
				t.Fatal("colliding pools bypassed the existing capacity limit")
			}
		})
	}
}

func weightedPoolForTest(route string, weights, current map[string]int64) *weightedPoolState {
	state := &weightedPoolState{route: route}
	for _, id := range sortedWeightedIDs(weights) {
		state.members = append(state.members, weightedPoolMember{id: id, weight: weights[id], current: current[id]})
	}
	return state
}
