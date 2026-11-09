package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestWeightedSessionAffinityCompensatesStickyRequests(t *testing.T) {
	for _, source := range []string{"sticky", "parent", "guardian", "fork"} {
		t.Run(source, func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			auths := []*Auth{
				{ID: "a", Provider: "codex", Attributes: map[string]string{AttributeWeight: "3"}},
				{ID: "b", Provider: "codex"},
			}
			parent := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"busy"}}}
			parentID := CanonicalSessionID(parent.Headers, nil, nil)
			parentModel := "model"
			if source == "guardian" {
				parentModel = "parent-model"
			}
			key := "codex::" + parentID + "::" + parentModel
			selector.cache.setSessionAliases("b", "codex", parentID, key)
			for i := range 4 {
				opts := parent
				if source != "sticky" {
					opts = cliproxyexecutor.Options{Headers: http.Header{
						"Session_id": {fmt.Sprintf("child-%d", i)}, "X-Codex-Parent-Thread-Id": {"busy"},
					}}
					if source == "guardian" {
						opts.Headers.Set("X-Openai-Subagent", "guardian")
					}
					if source == "fork" {
						opts.OriginalRequest = []byte(`{"forked_from_thread_id":"busy"}`)
					}
				}
				picked, errPick := selector.Pick(t.Context(), "codex", "model", opts, auths)
				if errPick != nil || picked == nil || picked.ID != "b" {
					t.Fatalf("sticky request %d: pick=%v err=%v", i, picked, errPick)
				}
			}
			state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
			if state.currentForTest()["a"] != 12 || state.currentForTest()["b"] != -12 || len(state.pendingForTest()) != 0 {
				t.Fatalf("sticky credits: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
			}
			for i := range 12 {
				// Requests without an identity and cold sessions share the same credits.
				opts := cliproxyexecutor.Options{}
				if i%2 == 0 {
					opts.Headers = http.Header{"Session_id": {fmt.Sprintf("new-%d", i)}}
				}
				picked, errPick := selector.Pick(t.Context(), "codex", "model", opts, auths)
				if errPick != nil || picked == nil || picked.ID != "a" {
					t.Fatalf("compensation %d: pick=%v err=%v", i, picked, errPick)
				}
			}
			if state.currentForTest()["a"] != 0 || state.currentForTest()["b"] != 0 || len(state.pendingForTest()) != 0 {
				t.Fatalf("compensated credits: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
			}
			if binding, ok := selector.cache.Get(key); !ok || binding != "b" {
				t.Fatalf("balancing moved the parent binding: %q, found=%t", binding, ok)
			}
		})
	}
}

func TestWeightedSessionAffinityChangingPoolsDoNotStarveEqualWeights(t *testing.T) {
	for _, newSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("new-session=%t", newSession), func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			auths := []*Auth{
				{ID: "a", Attributes: map[string]string{"priority": "0"}},
				{ID: "b", Attributes: map[string]string{"priority": "1"}},
				{ID: "c", Attributes: map[string]string{"priority": "1"}},
			}
			sticky := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"lower-priority"}}}
			id := CanonicalSessionID(sticky.Headers, nil, nil)
			key := "codex::" + id + "::model"
			selector.cache.Set(key, "a")
			counts := map[string]int{}
			const warmup, requests = 100, 1000
			for i := range requests {
				picked, errPick := selector.Pick(t.Context(), "codex", "model", sticky, auths)
				if errPick != nil || picked == nil || picked.ID != "a" {
					t.Fatalf("sticky request %d: pick=%v err=%v", i, picked, errPick)
				}
				opts := cliproxyexecutor.Options{}
				if newSession {
					opts.Headers = http.Header{"Session_id": {fmt.Sprintf("fresh-%d", i)}}
				}
				picked, errPick = selector.Pick(t.Context(), "codex", "model", opts, auths)
				if errPick != nil || picked == nil || (picked.ID != "b" && picked.ID != "c") {
					t.Fatalf("fresh request %d: pick=%v err=%v", i, picked, errPick)
				}
				if i >= warmup {
					counts[picked.ID]++
				}
			}
			if counts["b"] != (requests-warmup)/2 || counts["c"] != (requests-warmup)/2 {
				t.Fatalf("changing pools starved equal-weight candidates after warmup: %v, want b=450 c=450", counts)
			}
			if binding, ok := selector.cache.Get(key); !ok || binding != "a" {
				t.Fatalf("balancing moved the sticky binding: %q, found=%t", binding, ok)
			}
			if state := weightedStateForTest(t, weighted, "codex:model", "b", "c"); len(state.pendingForTest()) != 0 {
				t.Fatalf("pending credits leaked: %v", state.pendingForTest())
			}
		})
	}
}

func TestWeightedSessionAffinityColdSelectionsCountOnce(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	selector := NewSessionAffinitySelector(weighted)
	t.Cleanup(selector.Stop)
	auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
	counts := map[string]int{}
	for i := range 40 {
		picked, errPick := selector.Pick(t.Context(), "codex", "model", cliproxyexecutor.Options{
			Headers: http.Header{"Session_id": {fmt.Sprintf("session-%d", i)}},
		}, auths)
		if errPick != nil || picked == nil {
			t.Fatalf("pick=%v err=%v", picked, errPick)
		}
		state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
		if i == 0 && (state.currentForTest()["a"] != -1 || state.currentForTest()["b"] != 1) {
			t.Fatalf("first selection charged more than once: %v", state.currentForTest())
		}
		counts[picked.ID]++
	}
	if counts["a"] != 30 || counts["b"] != 10 {
		t.Fatalf("cold distribution = %v, want 30:10", counts)
	}
}

func TestWeightedSessionAffinityBoundsCompensation(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	selector := NewSessionAffinitySelector(weighted)
	t.Cleanup(selector.Stop)
	auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"long"}}}
	id := CanonicalSessionID(opts.Headers, nil, nil)
	selector.cache.Set("codex::"+id+"::model", "b")
	for range 1000 {
		picked, errPick := selector.Pick(t.Context(), "codex", "model", opts, auths)
		if errPick != nil || picked == nil || picked.ID != "b" {
			t.Fatalf("long session: pick=%v err=%v", picked, errPick)
		}
	}
	state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
	limit := maxSmoothWeightedCreditRounds * 4
	// A bound charge is admitted as a whole, stopping within one delta of the cap.
	wantCredit := limit / 3 * 3
	if state.currentForTest()["a"] != wantCredit || state.currentForTest()["b"] != -wantCredit {
		t.Fatalf("bounded credits = %v, want +/- %d", state.currentForTest(), wantCredit)
	}
	for range wantCredit {
		if _, errPick := weighted.Pick(t.Context(), "codex", "model", cliproxyexecutor.Options{}, auths); errPick != nil {
			t.Fatal(errPick)
		}
	}
	if state.currentForTest()["a"] != 0 || state.currentForTest()["b"] != 0 {
		t.Fatalf("compensation did not finish: %v", state.currentForTest())
	}
}

func TestWeightedSessionAffinityLCPCredits(t *testing.T) {
	for _, source := range []string{"hit", "fork", "compaction"} {
		t.Run(source, func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
			root := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAI,
				OriginalRequest: []byte(`{"messages":[{"role":"user","content":"step 1"},` +
					`{"role":"assistant","content":"ack 1"},{"role":"user","content":"step 2"},` +
					`{"role":"assistant","content":"ack 2"},{"role":"user","content":"step 3"}]}`),
				Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller"},
			}
			// Seed the matcher without consuming weighted credits.
			turns := cliproxysession.ExtractCanonicalTurns(root.SourceFormat, root.OriginalRequest)
			selector.matcher.Bind(lcpAffinityNamespace("openai", "model", root.Metadata), turns, "b")
			opts := root
			if source == "fork" {
				opts.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"step 1"},` +
					`{"role":"assistant","content":"ack 1"},{"role":"user","content":"branch"}]}`)
			} else if source == "compaction" {
				opts.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"<summary>Steps 1 and 2 completed</summary>"},` +
					`{"role":"assistant","content":"ack 2"},{"role":"user","content":"step 3"},` +
					`{"role":"user","content":"step 4"}]}`)
			}
			picked, errPick := selector.Pick(t.Context(), "openai", "model", opts, auths)
			if errPick != nil || picked == nil || picked.ID != "b" {
				t.Fatalf("LCP %s: pick=%v err=%v", source, picked, errPick)
			}
			if source == "fork" && opts.Metadata[cliproxyexecutor.IsForkMetadataKey] != true {
				t.Fatal("expected LCP fork")
			}
			if source == "compaction" && opts.Metadata[cliproxyexecutor.IsCompactionMetadataKey] != true {
				t.Fatal("expected LCP compaction")
			}
			state := weightedStateForTest(t, weighted, "openai:model", "a", "b")
			if state.currentForTest()["a"] != 3 || state.currentForTest()["b"] != -3 || len(state.pendingForTest()) != 0 {
				t.Fatalf("LCP credits: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
			}
		})
	}
}

func TestWeightedReservationRollbackPreservesConcurrentCharges(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
	key := "codex:model"
	for range 1000 {
		_, reservation := weighted.reserve(key, auths, "b")
		reservation.finish(true)
	}
	state := weightedStateForTest(t, weighted, key, "a", "b")
	_, rejected := weighted.reserve(key, auths, "b")
	_, accepted := weighted.reserve(key, auths, "a")
	accepted.finish(true)
	rejected.finish(false)
	rejected.finish(true)
	accepted.finish(false)
	limit := maxSmoothWeightedCreditRounds * 4 / 3 * 3
	if state.currentForTest()["a"] != limit-1 || state.currentForTest()["b"] != -limit+1 || len(state.pendingForTest()) != 0 {
		t.Fatalf("rollback lost another charge: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
	}
}

func TestWeightedReservationResetInvalidatesOldGeneration(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
	key := "codex:model"
	_, old := weighted.reserve(key, auths, "")
	auths[0].Attributes[AttributeWeight] = "1"
	_, latest := weighted.reserve(key, auths, "")
	old.finish(true)
	state := weightedStateForTest(t, weighted, key, "a", "b")
	if state.currentForTest()["a"] != 0 || state.currentForTest()["b"] != 0 || state.pendingForTest()["a"] != -1 || state.pendingForTest()["b"] != 1 {
		t.Fatalf("stale reservation changed new generation: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
	}
	latest.finish(true)
	if state.currentForTest()["a"] != -1 || state.currentForTest()["b"] != 1 || len(state.pendingForTest()) != 0 {
		t.Fatalf("new generation accounting: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
	}
}

func TestWeightedReservationConcurrentSelectionsSeePendingCredits(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}}
	const requests = 40
	selected := make(chan *Auth, requests)
	reservations := make(chan *weightedReservation, requests)
	var ready sync.WaitGroup
	ready.Add(requests)
	for range requests {
		go func() {
			defer ready.Done()
			picked, reservation := weighted.reserve("codex:model", auths, "")
			selected <- picked
			reservations <- reservation
		}()
	}
	ready.Wait()
	close(selected)
	close(reservations)
	counts := map[string]int{}
	for auth := range selected {
		if auth == nil {
			t.Fatal("concurrent reservation returned no auth")
		}
		counts[auth.ID]++
	}
	if counts["a"] != 30 || counts["b"] != 10 {
		t.Fatalf("pending allocations ignored: %v", counts)
	}
	for reservation := range reservations {
		reservation.finish(true)
	}
	state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
	if state.currentForTest()["a"] != 0 || state.currentForTest()["b"] != 0 || len(state.pendingForTest()) != 0 {
		t.Fatalf("concurrent accounting: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
	}
}

func TestWeightedSessionAffinityFailureDoesNotCharge(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: weighted, RebindOn: SessionAffinityRebindOnNever,
	})
	t.Cleanup(selector.Stop)
	opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"bound"}}}
	id := CanonicalSessionID(opts.Headers, nil, nil)
	selector.cache.Set("codex::"+id+"::model", "missing")
	if auth, errPick := selector.Pick(t.Context(), "codex", "model", opts, []*Auth{{ID: "a"}, {ID: "b"}}); errPick == nil || auth != nil {
		t.Fatalf("expected unavailable binding: auth=%v err=%v", auth, errPick)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if auth, errPick := selector.Pick(ctx, "codex", "model", opts, []*Auth{{ID: "a"}, {ID: "b"}}); !errors.Is(errPick, context.Canceled) || auth != nil {
		t.Fatalf("expected cancellation: auth=%v err=%v", auth, errPick)
	}
	if len(weighted.states) != 0 {
		t.Fatalf("failed selection changed weighted state: %v", weighted.states)
	}
}

func TestWeightedSessionAffinityEligiblePoolAndModelIsolation(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	selector := NewSessionAffinitySelector(weighted)
	t.Cleanup(selector.Stop)
	auths := []*Auth{
		{ID: "a", Attributes: map[string]string{AttributeWeight: "3", "priority": "1"}},
		{ID: "b", Attributes: map[string]string{"priority": "1"}},
		{ID: "lower", Attributes: map[string]string{AttributeWeight: "100"}},
		{ID: "zero", Attributes: map[string]string{AttributeWeight: "0", "priority": "2"}},
		{ID: "cooldown", Attributes: map[string]string{"priority": "2"}, ModelStates: map[string]*ModelState{
			"upstream": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)},
		}},
	}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"alias"}}}
	id := CanonicalSessionID(opts.Headers, nil, nil)
	selector.cache.Set("codex::"+id+"::upstream", "b")
	ctx := withWeightedSelectorStateModel(t.Context(), weighted, "requested(high)")
	picked, errPick := selector.Pick(ctx, "codex", "upstream", opts, auths)
	if errPick != nil || picked == nil || picked.ID != "b" {
		t.Fatalf("sticky pick=%v err=%v", picked, errPick)
	}
	state := weightedStateForTest(t, weighted, "codex:requested", "a", "b")
	if state == nil || len(weighted.states) != 1 || len(state.currentForTest()) != 2 || state.currentForTest()["a"] != 3 || state.currentForTest()["b"] != -3 {
		t.Fatalf("wrong model key or eligible pool: %v", weighted.states)
	}
	picked, errPick = weighted.Pick(ctx, "codex", "", cliproxyexecutor.Options{}, auths[:2])
	if errPick != nil || picked == nil || picked.ID != "a" || state.currentForTest()["a"] != 2 || state.currentForTest()["b"] != -2 {
		t.Fatalf("fallback did not share credits: pick=%v err=%v credits=%v", picked, errPick, state.currentForTest())
	}
	for _, pair := range [][2]string{{"codex", "other-model"}, {"other-provider", "requested"}} {
		if _, errOther := selector.Pick(t.Context(), pair[0], pair[1], cliproxyexecutor.Options{}, auths[:2]); errOther != nil {
			t.Fatal(errOther)
		}
	}
	if state.currentForTest()["a"] != 2 || state.currentForTest()["b"] != -2 {
		t.Fatal("another provider or model changed credits")
	}
}

func TestWeightedSessionAffinityReselectionCountsOnce(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	selector := NewSessionAffinitySelector(weighted)
	t.Cleanup(selector.Stop)
	opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {"reselect"}}}
	id := CanonicalSessionID(opts.Headers, nil, nil)
	key := "codex::" + id + "::model"
	selector.cache.Set(key, "removed")
	picked, errPick := selector.Pick(t.Context(), "codex", "model", opts,
		[]*Auth{{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}}, {ID: "b"}})
	if errPick != nil || picked == nil || picked.ID != "a" {
		t.Fatalf("reselection: pick=%v err=%v", picked, errPick)
	}
	state := weightedStateForTest(t, weighted, "codex:model", "a", "b")
	if state.currentForTest()["a"] != -1 || state.currentForTest()["b"] != 1 || len(state.pendingForTest()) != 0 {
		t.Fatalf("reselection counted incorrectly: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
	}
	if binding, ok := selector.cache.Get(key); !ok || binding != "a" {
		t.Fatalf("reselection did not bind: %q, found=%t", binding, ok)
	}
}

func TestManagerWeightedSessionAffinityCompensation(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixed=%t", mixed), func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			selector := NewSessionAffinitySelector(weighted)
			t.Cleanup(selector.Stop)
			manager := NewManager(nil, selector, nil)
			provider, model := "weighted-affinity", "weighted-model"
			manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
			aID, bID := t.Name()+"-a", t.Name()+"-b"
			for _, auth := range []*Auth{
				{ID: aID, Provider: provider, Status: StatusActive, Attributes: map[string]string{AttributeWeight: "3"}},
				{ID: bID, Provider: provider, Status: StatusActive},
			} {
				if _, errRegister := manager.Register(WithSkipPersist(t.Context()), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			namespace := provider
			if mixed {
				namespace = "mixed"
			}
			parentHeaders := http.Header{"Session_id": {"manager-sticky"}}
			id := CanonicalSessionID(parentHeaders, nil, nil)
			selector.cache.Set(namespace+"::"+id+"::"+model, bID)
			for i := range 16 {
				opts := cliproxyexecutor.Options{Headers: parentHeaders}
				want := bID
				if i >= 4 {
					opts.Headers = http.Header{"Session_id": {fmt.Sprintf("new-%d", i)}}
					want = aID
				}
				var auth *Auth
				var errPick error
				if mixed {
					auth, _, _, errPick = manager.pickNextMixed(t.Context(), []string{provider}, model, opts, nil)
				} else {
					auth, _, errPick = manager.pickNext(t.Context(), provider, model, opts, nil)
				}
				if errPick != nil || auth == nil || auth.ID != want {
					t.Fatalf("request %d: auth=%v err=%v, want %q", i, auth, errPick, want)
				}
			}
			state := weightedStateForTest(t, weighted, namespace+":"+model, aID, bID)
			if state == nil || state.currentForTest()[aID] != 0 || state.currentForTest()[bID] != 0 || len(state.pendingForTest()) != 0 {
				t.Fatalf("manager credits did not balance: %#v", state)
			}
		})
	}
}
