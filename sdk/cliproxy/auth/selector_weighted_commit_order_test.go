package auth

import (
	"fmt"
	"maps"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWeightedReservationFinalRollbackBoundsSettledCredits(t *testing.T) {
	for _, rollbackFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback-first=%t", rollbackFirst), func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			auths := []*Auth{{ID: "a"}, {ID: "b"}}
			reservations := map[string][]*weightedReservation{}
			t.Cleanup(func() {
				for _, group := range reservations {
					for _, reservation := range group {
						reservation.finish(false)
					}
				}
			})
			for range 256 {
				picked, reservation := weighted.reserve("codex:model", auths, "")
				if picked == nil || reservation == nil {
					t.Fatal("missing cold reservation")
				}
				reservations[picked.ID] = append(reservations[picked.ID], reservation)
			}
			ids := []string{"a", "b"}
			if rollbackFirst {
				ids = []string{"b", "a"}
			}
			for _, id := range ids {
				for _, reservation := range reservations[id] {
					reservation.finish(id == "a")
					// Repeated finishing must not decrement the counter again.
					reservation.finish(false)
				}
			}
			state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
			limit := maxSmoothWeightedCreditRounds * 2
			if state.inFlight != 0 || len(state.pendingForTest()) != 0 || !maps.Equal(state.currentForTest(), map[string]int64{"a": -limit, "b": limit}) {
				t.Fatalf("settled credit did not reach its bound: current=%v pending=%v in-flight=%d", state.currentForTest(), state.pendingForTest(), state.inFlight)
			}
		})
	}
}

func TestWeightedReservationUnequalWeightsOutOfOrderCommits(t *testing.T) {
	for _, first := range []string{"a", "b"} {
		t.Run(first, func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
			reservations := map[string][]*weightedReservation{}
			t.Cleanup(func() {
				for _, group := range reservations {
					for _, reservation := range group {
						reservation.finish(false)
					}
				}
			})
			for range 512 {
				picked, reservation := weighted.reserve("codex:model", auths, "")
				if picked == nil || reservation == nil {
					t.Fatal("missing cold reservation")
				}
				reservations[picked.ID] = append(reservations[picked.ID], reservation)
			}
			if len(reservations["a"]) != 384 || len(reservations["b"]) != 128 {
				t.Fatalf("reservations did not follow 3:1: a=%d b=%d", len(reservations["a"]), len(reservations["b"]))
			}
			last := "b"
			if first == "b" {
				last = "a"
			}
			state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
			for _, reservation := range reservations[first] {
				reservation.finish(true)
			}
			// New arrivals must see the remaining offsets before the batch settles.
			for _, phase := range []string{"pending", "settled"} {
				if phase == "settled" {
					for _, reservation := range reservations[last] {
						reservation.finish(true)
					}
				}
				counts := map[string]int{}
				for range 64 {
					picked, errPick := weighted.Pick(t.Context(), "codex", "model", cliproxyexecutor.Options{}, auths)
					if errPick != nil || picked == nil {
						t.Fatalf("Pick failed: auth=%v err=%v", picked, errPick)
					}
					counts[picked.ID]++
				}
				if counts["a"] != 48 || counts["b"] != 16 {
					t.Fatalf("%s distribution = %v, want 48:16", phase, counts)
				}
			}
			if state.inFlight != 0 || len(state.pendingForTest()) != 0 || !maps.Equal(state.currentForTest(), map[string]int64{"a": 0, "b": 0}) {
				t.Fatalf("settled 3:1 batch drifted: %#v", state)
			}
		})
	}
}

func TestWeightedReservationResetDoesNotReleaseNewGeneration(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a"}, {ID: "b"}}
	var old []*weightedReservation
	t.Cleanup(func() {
		for _, reservation := range old {
			reservation.finish(false)
		}
	})
	for range 256 {
		_, reservation := weighted.reserve("codex:model", auths, "")
		old = append(old, reservation)
	}
	auths[0].Attributes = map[string]string{AttributeWeight: "3"}
	_, latest := weighted.reserve("codex:model", auths, "")
	if latest == nil {
		t.Fatal("missing new-generation reservation")
	}
	t.Cleanup(func() { latest.finish(false) })
	for _, reservation := range old {
		reservation.finish(true)
	}
	state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
	if state.inFlight != 1 || state.hasCurrentForTest() || !maps.Equal(state.pendingForTest(), map[string]int64{"a": -1, "b": 1}) {
		t.Fatalf("old generation altered new accounting: %#v", state)
	}
	latest.finish(true)
	if state.inFlight != 0 || len(state.pendingForTest()) != 0 || !maps.Equal(state.currentForTest(), map[string]int64{"a": -1, "b": 1}) {
		t.Fatalf("new generation failed to settle: %#v", state)
	}
}

func TestWeightedReservationOutOfOrderBalancedCommits(t *testing.T) {
	for _, bound := range []bool{false, true} {
		for _, first := range []string{"a", "b"} {
			t.Run(fmt.Sprintf("bound=%t/first=%s", bound, first), func(t *testing.T) {
				weighted := &WeightedRoundRobinSelector{}
				auths := []*Auth{{ID: "a"}, {ID: "b"}}
				reservations := map[string][]*weightedReservation{}
				t.Cleanup(func() {
					for _, group := range reservations {
						for _, reservation := range group {
							reservation.finish(false)
						}
					}
				})
				for i := range 256 {
					boundID := ""
					if bound {
						boundID = auths[i%2].ID
					}
					picked, reservation := weighted.reserve("codex:model", auths, boundID)
					if picked == nil || reservation == nil {
						t.Fatalf("reserve %d returned pick=%v reservation=%v", i, picked, reservation)
					}
					reservations[picked.ID] = append(reservations[picked.ID], reservation)
				}
				if len(reservations["a"]) != 128 || len(reservations["b"]) != 128 {
					t.Fatalf("unbalanced reservations: a=%d b=%d", len(reservations["a"]), len(reservations["b"]))
				}
				state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
				// A zero net pending vector does not mean all reservations settled.
				if state.inFlight != 256 || len(state.pendingForTest()) != 0 {
					t.Fatalf("in-flight=%d pending=%v, want 256 live balanced reservations", state.inFlight, state.pendingForTest())
				}
				last := "b"
				if first == "b" {
					last = "a"
				}
				for _, id := range []string{first, last} {
					for _, reservation := range reservations[id] {
						reservation.finish(true)
					}
				}
				if !maps.Equal(state.currentForTest(), map[string]int64{"a": 0, "b": 0}) || len(state.pendingForTest()) != 0 || state.inFlight != 0 {
					t.Errorf("commit order distorted balanced credit: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
				}
				counts := map[string]int{}
				for range 64 {
					picked, errPick := weighted.Pick(t.Context(), "codex", "model", cliproxyexecutor.Options{}, auths)
					if errPick != nil || picked == nil {
						t.Fatalf("Pick: auth=%v err=%v", picked, errPick)
					}
					counts[picked.ID]++
				}
				if counts["a"] != 32 || counts["b"] != 32 {
					t.Fatalf("post-commit distribution = %v, want a=32 b=32", counts)
				}
			})
		}
	}
}
