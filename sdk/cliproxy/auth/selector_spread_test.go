package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// spreadDraws returns an intn stub that yields vals in turn.
func spreadDraws(vals ...int) func(int) int {
	var mu sync.Mutex
	return func(int) int {
		mu.Lock()
		defer mu.Unlock()
		v := vals[0]
		vals = vals[1:]
		return v
	}
}

// resetRecentStarts empties the package-level counters for one test.
func resetRecentStarts(t *testing.T) {
	t.Helper()
	recentStartsMu.Lock()
	recentStartsByKey = map[string]*[recentStartBuckets]recentStartBucket{}
	recentStartsMu.Unlock()
}

func spreadAuths(ids ...string) []*Auth {
	out := make([]*Auth, 0, len(ids))
	for _, id := range ids {
		out = append(out, &Auth{ID: id, Provider: "codex"})
	}
	return out
}

func startsFor(model string, counts map[string]int) {
	for id, n := range counts {
		for range n {
			RecordStart(id, model)
		}
	}
}

func pickID(t *testing.T, s Selector, provider, model string, auths []*Auth) string {
	t.Helper()
	got, err := s.Pick(context.Background(), provider, model, cliproxyexecutor.Options{}, auths)
	if err != nil || got == nil {
		t.Fatalf("Pick = %v, %v", got, err)
	}
	return got.ID
}

func TestRecentStartsExpireAfterWindow(t *testing.T) {
	resetRecentStarts(t)
	t0 := time.Unix(1_700_000_000, 0)
	recordStartAt("a", "m(high)", t0)
	recordStartAt("a", "m", t0)
	if got := recentStarts("a", "m", t0.Add(59*time.Second)); got != 2 {
		t.Errorf("at t0+59s = %d, want 2 (thinking suffix shares the model)", got)
	}
	if got := recentStarts("a", "m", t0.Add(61*time.Second)); got != 0 {
		t.Errorf("at t0+61s = %d, want 0", got)
	}
	if got := recentStarts("a", "other", t0); got != 0 {
		t.Errorf("other model = %d, want 0", got)
	}
}

func TestRandomSelectorUsesTopTierOnly(t *testing.T) {
	auths := []*Auth{
		{ID: "a", Attributes: map[string]string{"priority": "1"}},
		{ID: "b", Attributes: map[string]string{"priority": "1"}},
		{ID: "c", Attributes: map[string]string{"priority": "0"}},
	}
	seen := map[string]int{}
	n := 0
	s := &RandomSelector{intn: func(k int) int { n++; return n % k }}
	for range 200 {
		seen[pickID(t, s, "codex", "m", auths)]++
	}
	if seen["c"] != 0 || seen["a"] == 0 || seen["b"] == 0 {
		t.Errorf("picks = %v, want only a and b, both seen", seen)
	}
}

func TestLeastUsedSelectorFewestRecentStarts(t *testing.T) {
	resetRecentStarts(t)
	s := &LeastUsedSelector{}
	auths := spreadAuths("a", "b", "c")
	if got := pickID(t, s, "codex", "m", auths); got != "a" {
		t.Errorf("all equal = %q, want a", got)
	}
	startsFor("m", map[string]int{"a": 2, "c": 1})
	if got := pickID(t, s, "codex", "m", auths); got != "b" {
		t.Errorf("a=2 b=0 c=1 = %q, want b", got)
	}
}

func TestP2CSelectorLowerOfTwo(t *testing.T) {
	resetRecentStarts(t)
	auths := spreadAuths("a", "b", "c")
	startsFor("m", map[string]int{"a": 5, "b": 1})
	if got := pickID(t, &P2CSelector{intn: spreadDraws(0, 1)}, "codex", "m", auths); got != "b" {
		t.Errorf("draws (0,1) = %q, want b", got)
	}
	if got := pickID(t, &P2CSelector{intn: spreadDraws(0, 0, 2)}, "codex", "m", auths); got != "c" {
		t.Errorf("draws (0,0,2) = %q, want c", got)
	}
	resetRecentStarts(t)
	if got := pickID(t, &P2CSelector{intn: spreadDraws(1, 0)}, "codex", "m", auths); got != "a" {
		t.Errorf("tie drawn (1,0) = %q, want a", got)
	}
}

func TestSpreadSelectorsSkipCoolingAuths(t *testing.T) {
	resetRecentStarts(t)
	now := time.Now()
	cooling := &Auth{ID: "a", Provider: "codex", ModelStates: map[string]*ModelState{
		"m": {Status: StatusActive, Unavailable: true, NextRetryAfter: now.Add(time.Hour), Quota: QuotaState{Exceeded: true}},
	}}
	auths := []*Auth{cooling, {ID: "b", Provider: "codex"}, {ID: "c", Provider: "codex"}}
	startsFor("m", map[string]int{"b": 3, "c": 3})
	for _, s := range []Selector{&RandomSelector{}, &LeastUsedSelector{}, &P2CSelector{}} {
		for range 50 {
			if got := pickID(t, s, "codex", "m", auths); got == "a" {
				t.Fatalf("%T picked the cooling auth", s)
			}
		}
	}
}

func TestProviderStrategySelectorDispatch(t *testing.T) {
	resetRecentStarts(t)
	s := &ProviderStrategySelector{Default: &RoundRobinSelector{}, ByProvider: map[string]Selector{"codex": &LeastUsedSelector{}}}
	codex := spreadAuths("a", "b")
	startsFor("m", map[string]int{"a": 1})
	for range 3 {
		if got := pickID(t, s, "codex", "m", codex); got != "b" {
			t.Fatalf("codex pick = %q, want b (fewest starts)", got)
		}
	}
	// The engine picks with provider "mixed"; the candidates' one provider decides.
	if got := pickID(t, s, "mixed", "m", codex); got != "b" {
		t.Errorf("mixed codex pick = %q, want b", got)
	}
	xai := []*Auth{{ID: "x1", Provider: "xai"}, {ID: "x2", Provider: "xai"}}
	if a, b := pickID(t, s, "mixed", "m", xai), pickID(t, s, "mixed", "m", xai); a == b {
		t.Errorf("xai picks %q, %q, want a rotation", a, b)
	}
}

func TestSessionAffinityWinsOverSpreadSelectors(t *testing.T) {
	resetRecentStarts(t)
	n := 0
	selector := NewSessionAffinitySelector(&P2CSelector{intn: func(k int) int { n++; return n % k }})
	defer selector.Stop()
	auths := spreadAuths("a", "b")
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"session-spread"}}}
	first, err := selector.Pick(context.Background(), "codex", "m", opts, auths)
	if err != nil || first == nil {
		t.Fatalf("first Pick = %v, %v", first, err)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "codex", Model: "m", Success: true, Options: opts})
	startsFor("m", map[string]int{first.ID: 10})
	for range 5 {
		got, err := selector.Pick(context.Background(), "codex", "m", opts, auths)
		if err != nil || got == nil || got.ID != first.ID {
			t.Fatalf("bound session picked %v, %v; want %s", got, err, first.ID)
		}
	}
}
