package auth

import (
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWeightedOverloadExactCompensation(t *testing.T) {
	for _, weights := range [][]int{{1, 1}, {3, 1}, {1, 3}, {1, 5, 1}, {2, 3, 5}, {1, 2, 3, 4}} {
		for boundIndex := range weights {
			t.Run(fmt.Sprintf("weights=%v/bound=%d", weights, boundIndex), func(t *testing.T) {
				weighted := &WeightedRoundRobinSelector{}
				selector := NewSessionAffinitySelector(weighted)
				t.Cleanup(selector.Stop)
				auths := make([]*Auth, len(weights))
				total := 0
				for i, weight := range weights {
					auths[i] = &Auth{ID: fmt.Sprintf("auth-%d", i), Attributes: map[string]string{AttributeWeight: strconv.Itoa(weight)}}
					total += weight
				}
				opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"sticky"}}}
				key := "codex::" + CanonicalSessionID(opts.Headers, nil, nil) + "::model"
				selector.cache.Set(key, auths[boundIndex].ID)
				counts := map[string]int{}
				for i := range total * 4 {
					request := opts
					sticky := i < weights[boundIndex]*4
					if !sticky {
						request = cliproxyexecutor.Options{Headers: http.Header{"Session_id": {fmt.Sprintf("fresh-%d", i)}}}
					}
					picked, errPick := selector.Pick(t.Context(), "codex", "model", request, auths)
					if errPick != nil || picked == nil {
						t.Fatalf("selection failed: pick=%v err=%v", picked, errPick)
					}
					if sticky && picked.ID != auths[boundIndex].ID {
						t.Fatal("balancing moved an existing binding")
					}
					counts[picked.ID]++
				}
				for i, weight := range weights {
					if counts[auths[i].ID] != weight*4 {
						t.Errorf("total allocation = %v, want weight vector %v times 4", counts, weights)
					}
				}
				for _, state := range weighted.states {
					for id, credit := range state.currentForTest() {
						if credit != 0 {
							t.Errorf("exact compensation left %q credit=%d", id, credit)
						}
					}
					if len(state.pendingForTest()) != 0 {
						t.Errorf("pending credit leaked: %v", state.pendingForTest())
					}
				}
			})
		}
	}
}

func TestWeightedOverloadSustainedStickyTrafficDoesNotStarveOthers(t *testing.T) {
	for _, weights := range [][]int{{1, 1, 1}, {1, 3, 1}, {1, 5, 1}, {1, 1, 5}, {1, 9, 1}, {1, 1, 9}, {1, 5, 2, 1}} {
		t.Run(fmt.Sprint(weights), func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			auths := make([]*Auth, len(weights))
			for i, weight := range weights {
				auths[i] = &Auth{ID: fmt.Sprintf("auth-%d", i), Attributes: map[string]string{AttributeWeight: strconv.Itoa(weight)}}
			}
			sticky := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"busy"}}}
			key := "codex::" + CanonicalSessionID(sticky.Headers, nil, nil) + "::model"
			selector.cache.Set(key, auths[0].ID)
			counts := map[string]int{}
			for i := range 3000 {
				picked, errPick := selector.Pick(t.Context(), "codex", "model", sticky, auths)
				if errPick != nil || picked == nil || picked.ID != auths[0].ID {
					t.Fatalf("binding changed: pick=%v err=%v", picked, errPick)
				}
				fresh := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {fmt.Sprintf("fresh-%d", i)}}}
				picked, errPick = selector.Pick(t.Context(), "codex", "model", fresh, auths)
				if errPick != nil || picked == nil {
					t.Fatalf("fresh selection failed: pick=%v err=%v", picked, errPick)
				}
				if i >= 1000 {
					counts[picked.ID]++
				}
			}
			ids := make([]string, len(auths))
			for i, auth := range auths {
				ids[i] = auth.ID
			}
			state := weightedStateForTest(t, weighted, "codex:model", ids...)
			t.Logf("fresh distribution after warmup: %v; committed=%v", counts, state.currentForTest())
			// Forced traffic makes global ratios infeasible; the remaining capacity
			// should be shared by unbound peers rather than monopolized by one.
			peerWeight := 0
			for _, weight := range weights[1:] {
				peerWeight += weight
			}
			for i, auth := range auths[1:] {
				want := 2000 * weights[i+1] / peerWeight
				if counts[auth.ID] < want-2 || counts[auth.ID] > want+2 {
					t.Errorf("peer %q got %d fresh requests, want approximately %d; distribution=%v", auth.ID, counts[auth.ID], want, counts)
				}
			}
		})
	}
}

func TestWeightedOverloadFeasibleSustainedTraffic(t *testing.T) {
	for _, weights := range [][]int{{3, 1}, {1, 5, 1}, {1, 9, 1}, {1, 5, 2, 1}} {
		t.Run(fmt.Sprint(weights), func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			auths := make([]*Auth, len(weights))
			total := 0
			for i, weight := range weights {
				auths[i] = &Auth{ID: fmt.Sprintf("auth-%d", i), Attributes: map[string]string{AttributeWeight: strconv.Itoa(weight)}}
				total += weight
			}
			sticky := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"feasible"}}}
			key := "codex::" + CanonicalSessionID(sticky.Headers, nil, nil) + "::model"
			selector.cache.Set(key, auths[0].ID)
			counts := map[string]int{}
			// Forced traffic is below A's target share, so exact aggregate ratios
			// remain feasible without moving established bindings.
			for i := range total * 2000 {
				opts := cliproxyexecutor.Options{}
				bound := i%(total*2) == 0
				if bound {
					opts = sticky
				}
				picked, errPick := selector.Pick(t.Context(), "codex", "model", opts, auths)
				if errPick != nil || picked == nil || (bound && picked.ID != auths[0].ID) {
					t.Fatalf("request %d: pick=%v err=%v", i, picked, errPick)
				}
				if i >= total*1000 {
					counts[picked.ID]++
				}
			}
			for i, weight := range weights {
				if counts[auths[i].ID] != weight*1000 {
					t.Errorf("feasible distribution drifted: counts=%v weights=%v", counts, weights)
				}
			}
			t.Logf("feasible distribution: %v", counts)
		})
	}
}

func TestWeightedOverloadRecoveryAfterStickyTrafficStops(t *testing.T) {
	for _, weights := range [][]int{{1, 5, 1}, {1, 9, 1}, {1, 5, 2, 1}} {
		t.Run(fmt.Sprint(weights), func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			auths := make([]*Auth, len(weights))
			total := 0
			for i, weight := range weights {
				auths[i] = &Auth{ID: fmt.Sprintf("auth-%d", i), Attributes: map[string]string{AttributeWeight: strconv.Itoa(weight)}}
				total += weight
			}
			sticky := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"busy"}}}
			key := "codex::" + CanonicalSessionID(sticky.Headers, nil, nil) + "::model"
			selector.cache.Set(key, auths[0].ID)
			for range 3000 {
				if picked, errPick := selector.Pick(t.Context(), "codex", "model", sticky, auths); errPick != nil || picked == nil || picked.ID != auths[0].ID {
					t.Fatalf("sticky selection failed: pick=%v err=%v", picked, errPick)
				}
				if _, errPick := selector.Pick(t.Context(), "codex", "model", cliproxyexecutor.Options{}, auths); errPick != nil {
					t.Fatal(errPick)
				}
			}
			counts := map[string]int{}
			for i := range 1000 + total*100 {
				picked, errPick := selector.Pick(t.Context(), "codex", "model", cliproxyexecutor.Options{}, auths)
				if errPick != nil || picked == nil {
					t.Fatalf("recovery selection failed: pick=%v err=%v", picked, errPick)
				}
				if i >= 1000 {
					counts[picked.ID]++
				}
			}
			for i, weight := range weights {
				if counts[auths[i].ID] != weight*100 {
					t.Errorf("allocation did not recover after sticky traffic stopped: counts=%v weights=%v", counts, weights)
				}
			}
			t.Logf("recovered distribution: %v", counts)
		})
	}
}

func TestWeightedOverloadPendingStickyReservationsDoNotStarveOthers(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a"}, {ID: "b", Attributes: map[string]string{AttributeWeight: "5"}}, {ID: "c"}}
	const key = "codex:model"
	const inFlight = 200
	reservations := make([]*weightedReservation, inFlight)
	t.Cleanup(func() {
		for _, reservation := range reservations {
			reservation.finish(false)
		}
	})
	for i := range reservations {
		picked, reservation := weighted.reserve(key, auths, "a")
		if picked == nil || picked.ID != "a" {
			t.Fatal("pending sticky request lost its binding")
		}
		reservations[i] = reservation
	}
	counts := map[string]int{}
	for i := range 3000 {
		// Maintain a fixed backlog without sleeps or timing-dependent coordination.
		reservations[i%inFlight].finish(true)
		_, reservations[i%inFlight] = weighted.reserve(key, auths, "a")
		picked, reservation := weighted.reserve(key, auths, "")
		reservation.finish(true)
		if i >= 1000 {
			counts[picked.ID]++
		}
	}
	if counts["c"] == 0 {
		t.Fatalf("speculative sticky traffic starved c: %v", counts)
	}
	for _, reservation := range reservations {
		reservation.finish(true)
	}
	state := weightedStateForTest(t, weighted, key, "a", "b", "c")
	if len(state.pendingForTest()) != 0 {
		t.Fatalf("settled reservations leaked credit: %v", state.pendingForTest())
	}
}

func TestWeightedOverloadCommitAfterSpeculativeRepaymentRollsBack(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
	const key = "codex:model"
	for range 1000 {
		_, reservation := weighted.reserve(key, auths, "b")
		reservation.finish(true)
	}
	state := weightedStateForTest(t, weighted, key, "a", "b")
	before := maps.Clone(state.currentForTest())
	_, first := weighted.reserve(key, auths, "")
	_, second := weighted.reserve(key, auths, "")
	t.Cleanup(func() { first.finish(false); second.finish(false) })
	// Two pending cold selections make room for a bound delta, but neither
	// repayment is real yet. Its later commit must recheck the committed cap.
	picked, bound := weighted.reserve(key, auths, "b")
	if picked == nil || picked.ID != "b" || bound == nil {
		t.Fatal("expected bound reservation funded by pending repayments")
	}
	t.Cleanup(func() { bound.finish(false) })
	first.finish(false)
	second.finish(false)
	bound.finish(true)
	bound.finish(true)
	bound.finish(false)
	if !maps.Equal(state.currentForTest(), before) || len(state.pendingForTest()) != 0 {
		t.Fatalf("late commit crossed the cap or leaked pending credit: before=%v current=%v pending=%v", before, state.currentForTest(), state.pendingForTest())
	}
	// An over-limit bound selection succeeds without reserving any credit.
	picked, skipped := weighted.reserve(key, auths, "b")
	if picked == nil || picked.ID != "b" || skipped != nil || !maps.Equal(state.currentForTest(), before) || len(state.pendingForTest()) != 0 {
		t.Fatal("saturated bound selection should preserve binding and all credits")
	}
}

func TestWeightedOverloadMultipleBusyBindings(t *testing.T) {
	for _, busyPerFresh := range []int{1, 3, 10} {
		t.Run(fmt.Sprint(busyPerFresh), func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			auths := []*Auth{
				{ID: "a"}, {ID: "b", Attributes: map[string]string{AttributeWeight: "5"}}, {ID: "c"}, {ID: "d"},
			}
			boundOpts := make([]cliproxyexecutor.Options, 2)
			for i, id := range []string{"a", "d"} {
				boundOpts[i].Headers = http.Header{"Session_id": {"busy-" + id}}
				sessionID := CanonicalSessionID(boundOpts[i].Headers, nil, nil)
				selector.cache.Set("codex::"+sessionID+"::model", id)
			}
			windowCounts := map[string]int{}
			for i := range 1400 {
				for j := range busyPerFresh {
					index := (i + j) % len(boundOpts)
					picked, errPick := selector.Pick(t.Context(), "codex", "model", boundOpts[index], auths)
					if errPick != nil || picked == nil || picked.ID != []string{"a", "d"}[index] {
						t.Fatalf("busy binding changed: auth=%v err=%v", picked, errPick)
					}
				}
				picked, errPick := selector.Pick(t.Context(), "codex", "model", cliproxyexecutor.Options{}, auths)
				if errPick != nil || picked == nil {
					t.Fatalf("fresh pick failed: auth=%v err=%v", picked, errPick)
				}
				if i >= 1000 {
					windowCounts[picked.ID]++
					if (i+1)%100 == 0 {
						if windowCounts["b"] == 0 || windowCounts["c"] == 0 {
							t.Fatalf("busy=%d unbound peer starved in a 100-request window: %v", busyPerFresh, windowCounts)
						}
						clear(windowCounts)
					}
				}
			}
		})
	}
}

func TestWeightedOverloadSaturatedChargeLeavesPeersUnchanged(t *testing.T) {
	for _, credit := range []map[string]int64{
		{"a": -222, "b": 111, "c": 111},
		{"a": -112, "b": 222, "c": -110},
	} {
		state := weightedPoolForTest("route", map[string]int64{"a": 1, "b": 5, "c": 1}, credit)
		state.charge(0, true)
		if !maps.Equal(state.currentForTest(), credit) {
			t.Fatalf("saturated sticky charge changed peers: got=%v want=%v", state.currentForTest(), credit)
		}
	}
}

func TestManagerWeightedOverloadStickyAndPinnedSessions(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, pinned := range []bool{false, true} {
			t.Run(fmt.Sprintf("mixed=%t/pinned=%t", mixed, pinned), func(t *testing.T) {
				weighted := &WeightedRoundRobinSelector{}
				selector := NewSessionAffinitySelector(weighted)
				t.Cleanup(selector.Stop)
				manager := NewManager(nil, selector, nil)
				const provider, model = "sticky-overload", "sticky-overload-model"
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
				aID, bID, cID := t.Name()+"-a", t.Name()+"-b", t.Name()+"-c"
				for _, auth := range []*Auth{
					{ID: aID, Provider: provider, Status: StatusActive},
					{ID: bID, Provider: provider, Status: StatusActive, Attributes: map[string]string{AttributeWeight: "5"}},
					{ID: cID, Provider: provider, Status: StatusActive},
				} {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(WithSkipPersist(t.Context()), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				namespace := provider
				if mixed {
					namespace = "mixed"
				}
				sticky := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"busy"}}, Metadata: map[string]any{}}
				key := namespace + "::" + CanonicalSessionID(sticky.Headers, nil, nil) + "::" + model
				selector.cache.Set(key, aID)
				if pinned {
					sticky.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = aID
				}
				counts := map[string]int{}
				for i := range 1600 {
					for _, bound := range []bool{true, false} {
						opts := sticky
						if !bound {
							opts = cliproxyexecutor.Options{Headers: http.Header{"Session_id": {fmt.Sprintf("new-%d", i)}}}
						}
						var auth *Auth
						var errPick error
						if mixed {
							auth, _, _, errPick = manager.pickNextMixedLegacy(t.Context(), []string{provider}, model, opts, nil)
						} else {
							auth, _, errPick = manager.pickNextLegacy(t.Context(), provider, model, opts, nil)
						}
						if errPick != nil || auth == nil || (bound && auth.ID != aID) {
							t.Fatalf("request %d bound=%t: auth=%v err=%v", i, bound, auth, errPick)
						}
						if !bound && i >= 1000 {
							counts[auth.ID]++
						}
					}
				}
				if counts[bID] != 500 || counts[cID] != 100 {
					t.Fatalf("manager overload allocation = %v, want b=500 c=100", counts)
				}
				if binding, ok := selector.cache.Get(key); !ok || binding != aID {
					t.Fatalf("manager moved busy binding: %q, found=%t", binding, ok)
				}
				state := weightedStateForTest(t, weighted, namespace+":"+model, aID, bID, cID)
				if len(state.pendingForTest()) != 0 {
					t.Fatalf("manager left speculative credit: %v", state.pendingForTest())
				}
			})
		}
	}
}

func TestWeightedOverloadConcurrentStickyAccounting(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	selector := NewSessionAffinitySelector(weighted)
	t.Cleanup(selector.Stop)
	auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
	headers := http.Header{"Session_id": {"sticky-concurrent"}}
	key := "codex::" + CanonicalSessionID(headers, nil, nil) + "::model"
	selector.cache.Set(key, "b")
	type selection struct {
		id     string
		err    error
		sticky bool
	}
	results := make(chan selection, 64)
	start := make(chan struct{})
	var done sync.WaitGroup
	for i := range 64 {
		done.Add(1)
		go func() {
			defer done.Done()
			isSticky := i < 8
			opts := cliproxyexecutor.Options{Headers: headers}
			if !isSticky {
				opts.Headers = http.Header{"Session_id": {fmt.Sprintf("new-%d", i)}}
			}
			<-start
			picked, errPick := selector.Pick(t.Context(), "codex", "model", opts, auths)
			id := ""
			if picked != nil {
				id = picked.ID
			}
			results <- selection{id: id, err: errPick, sticky: isSticky}
		}()
	}
	close(start)
	done.Wait()
	close(results)
	counts := map[string]int{}
	for result := range results {
		if result.err != nil || result.id == "" || (result.sticky && result.id != "b") {
			t.Fatalf("invalid concurrent result: %+v", result)
		}
		counts[result.id]++
	}
	state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
	// All 64 calls share one pool. Accounting must match actual accepted picks,
	// regardless of whether the final arrivals happened to be sticky requests.
	if state.currentForTest()["a"] != int64(64*3-counts["a"]*4) || state.currentForTest()["b"] != int64(64-counts["b"]*4) || len(state.pendingForTest()) != 0 {
		t.Fatalf("concurrent accounting drifted: counts=%v committed=%v pending=%v", counts, state.currentForTest(), state.pendingForTest())
	}
	if bound, ok := selector.cache.Get(key); !ok || bound != "b" {
		t.Fatalf("concurrency moved sticky binding: %q, found=%t", bound, ok)
	}
}
