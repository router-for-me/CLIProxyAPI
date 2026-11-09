package auth

import (
	"maps"
	"sort"
	"testing"
)

func TestSmoothWeightedStateChargeRebasesBeforeBounding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current map[string]int64
		weights map[string]int64
		authID  string
		want    map[string]int64
	}{
		{
			name:    "smaller pool preserves leading difference",
			current: map[string]int64{"a": -96, "b": 96, "c": 95},
			weights: map[string]int64{"b": 1, "c": 1},
			authID:  "b",
			want:    map[string]int64{"b": 63, "c": 64},
		},
		{
			name:    "negative credits shift together",
			current: map[string]int64{"a": 96, "b": -64, "c": -63},
			weights: map[string]int64{"b": 1, "c": 1},
			authID:  "b",
			want:    map[string]int64{"b": -64, "c": -61},
		},
		{
			name:    "excessive spread clips debt without tying leaders",
			current: map[string]int64{"a": -96, "b": 96, "c": 95},
			weights: map[string]int64{"a": 1, "b": 1, "c": 1},
			authID:  "a",
			want:    map[string]int64{"a": -96, "b": 96, "c": 95},
		},
		{
			name:    "bounded credits do not shift",
			current: map[string]int64{"b": 0, "c": 0},
			weights: map[string]int64{"b": 1, "c": 1},
			authID:  "b",
			want:    map[string]int64{"b": -1, "c": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := weightedPoolForTest("route", tc.weights, tc.current)
			state.charge(sort.Search(len(state.members), func(i int) bool { return state.members[i].id >= tc.authID }), false)
			if !maps.Equal(state.currentForTest(), tc.want) {
				t.Fatalf("rebased credits = %v, want %v", state.currentForTest(), tc.want)
			}
		})
	}
}

func TestWeightedReservationRollbackPreservesConcurrentCredits(t *testing.T) {
	key := "codex:model"
	weights := map[string]int64{"a": 1, "b": 1, "c": 1}
	state := weightedPoolForTest(key, weights, map[string]int64{"a": -94, "b": 94, "c": 93})
	weighted := &WeightedRoundRobinSelector{states: map[[32]byte]*weightedPoolState{weightedPoolStateKey(key, weights): state}, stateCount: 1}
	auths := []*Auth{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	_, rejected := weighted.reserve(key, auths, "a")
	t.Cleanup(func() { rejected.finish(false) })
	_, accepted := weighted.reserve(key, auths, "b")
	t.Cleanup(func() { accepted.finish(false) })
	accepted.finish(true)
	want := map[string]int64{"a": -93, "b": 92, "c": 94}
	if !maps.Equal(state.currentForTest(), want) {
		t.Fatalf("committed credits = %v, want %v", state.currentForTest(), want)
	}
	if !maps.Equal(state.pendingForTest(), map[string]int64{"a": -2, "b": 1, "c": 1}) {
		t.Fatalf("commit changed another request's reservation: %v", state.pendingForTest())
	}
	rejected.finish(false)
	rejected.finish(true)
	if !maps.Equal(state.currentForTest(), want) || len(state.pendingForTest()) != 0 {
		t.Fatalf("rollback changed committed credits: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
	}
}
