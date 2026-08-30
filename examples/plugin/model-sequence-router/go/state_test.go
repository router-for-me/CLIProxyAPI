package main

import (
	"sync"
	"testing"
	"time"
)

func testSequence() []compiledTarget {
	return []compiledTarget{
		{Provider: "codex", Model: "terra"},
		{Provider: "codex", Model: "terra"},
		{Provider: "codex", Model: "terra"},
		{Provider: "claude", Model: "opus"},
	}
}

// testSelection builds a selection that probes the whole sequence, matching the
// skip policy. Individual tests narrow the probe or attach a turn identity.
func testSelection(key cursorKey, available map[string]struct{}, ttl time.Duration, randomStart bool) selectionRequest {
	sequence := testSequence()
	return selectionRequest{
		Key:         key,
		Sequence:    sequence,
		Available:   available,
		TTL:         ttl,
		RandomStart: randomStart,
		ProbeLimit:  len(sequence),
	}
}

func TestCursorSequenceWrapAndIndependentKeys(t *testing.T) {
	now := time.Unix(100, 0)
	store := newCursorStore(func() time.Time { return now })
	available := map[string]struct{}{"codex": {}, "claude": {}}
	keyA := cursorKey{Generation: 1, Alias: "iterative", SessionID: "a"}
	keyB := cursorKey{Generation: 1, Alias: "iterative", SessionID: "b"}
	want := []string{"terra", "terra", "terra", "opus", "terra"}
	for i, model := range want {
		got := store.selectTarget(testSelection(keyA, available, time.Hour, false))
		if got.Outcome != selectionAdvanced || got.Target.Model != model {
			t.Fatalf("session A selection %d = %#v; want %q", i, got, model)
		}
	}
	gotB := store.selectTarget(testSelection(keyB, available, time.Hour, false))
	if gotB.Outcome != selectionAdvanced || gotB.Target.Model != "terra" {
		t.Fatalf("session B first selection = %#v", gotB)
	}
	otherAlias := cursorKey{Generation: 1, Alias: "other", SessionID: "a"}
	gotAlias := store.selectTarget(testSelection(otherAlias, available, time.Hour, false))
	if gotAlias.Outcome != selectionAdvanced || gotAlias.Target.Model != "terra" {
		t.Fatalf("other alias first selection = %#v", gotAlias)
	}
}

func TestCursorExpirationAndSlidingTTL(t *testing.T) {
	now := time.Unix(100, 0)
	store := newCursorStore(func() time.Time { return now })
	key := cursorKey{Generation: 1, Alias: "iterative", SessionID: "a"}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	first := store.selectTarget(testSelection(key, available, time.Minute, false))
	second := store.selectTarget(testSelection(key, available, time.Minute, false))
	if first.Index != 0 || second.Index != 1 || first.Target.Model != "terra" || second.Target.Model != "terra" {
		t.Fatalf("successive selections = %#v / %#v", first, second)
	}
	now = now.Add(30 * time.Second)
	_ = store.selectTarget(testSelection(key, available, time.Minute, false))
	now = now.Add(31 * time.Second)
	next := store.selectTarget(testSelection(key, available, time.Minute, false))
	if next.Index != 3 || next.Target.Model != "opus" {
		t.Fatalf("sliding TTL selection = %#v, want position 3", next)
	}
	now = now.Add(time.Minute)
	restarted := store.selectTarget(testSelection(key, available, time.Minute, false))
	if restarted.Index != 0 || restarted.Target.Model != "terra" {
		t.Fatalf("expired selection = %#v, want restart", restarted)
	}
}

func TestRandomStartChosenOnceThenAdvances(t *testing.T) {
	store := newCursorStore(nil)
	randomCalls := 0
	store.random = func(limit int) int {
		randomCalls++
		if limit != len(testSequence()) {
			t.Fatalf("random limit = %d", limit)
		}
		return 3
	}
	key := cursorKey{Generation: 1, Alias: "random", SessionID: "a"}
	available := map[string]struct{}{"codex": {}, "claude": {}}

	first := store.selectTarget(testSelection(key, available, time.Hour, true))
	if first.Outcome != selectionAdvanced || first.Index != 3 || first.Target.Model != "opus" {
		t.Fatalf("random start selection = %#v", first)
	}
	next := store.selectTarget(testSelection(key, available, time.Hour, true))
	if next.Outcome != selectionAdvanced || next.Index != 0 || next.Target.Model != "terra" {
		t.Fatalf("next = %#v", next)
	}
	if randomCalls != 1 {
		t.Fatalf("random calls = %d, want 1", randomCalls)
	}
}

func TestRandomStartIsIndependentPerConversation(t *testing.T) {
	store := newCursorStore(nil)
	starts := []int{2, 3}
	store.random = func(int) int {
		start := starts[0]
		starts = starts[1:]
		return start
	}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	first := store.selectTarget(testSelection(cursorKey{Generation: 1, Alias: "random", SessionID: "a"}, available, time.Hour, true))
	second := store.selectTarget(testSelection(cursorKey{Generation: 1, Alias: "random", SessionID: "b"}, available, time.Hour, true))
	if first.Index != 2 || second.Index != 3 {
		t.Fatalf("random starts = %d/%d", first.Index, second.Index)
	}
}

func TestRandomStartIsChosenAgainAfterExpiration(t *testing.T) {
	now := time.Unix(100, 0)
	store := newCursorStore(func() time.Time { return now })
	starts := []int{1, 3}
	store.random = func(int) int {
		start := starts[0]
		starts = starts[1:]
		return start
	}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	key := cursorKey{Generation: 1, Alias: "random", SessionID: "a"}

	first := store.selectTarget(testSelection(key, available, time.Minute, true))
	now = now.Add(time.Minute)
	second := store.selectTarget(testSelection(key, available, time.Minute, true))
	if first.Index != 1 || second.Index != 3 {
		t.Fatalf("random starts before/after expiration = %d/%d", first.Index, second.Index)
	}
}

func TestProviderSkippingAndUnavailableDoesNotAdvance(t *testing.T) {
	store := newCursorStore(nil)
	store.random = func(int) int { return 0 }
	key := cursorKey{Generation: 1, Alias: "iterative", SessionID: "a"}
	claudeOnly := map[string]struct{}{"claude": {}}
	got := store.selectTarget(testSelection(key, claudeOnly, time.Hour, true))
	if got.Outcome != selectionAdvanced || got.Index != 3 || got.Target.Model != "opus" {
		t.Fatalf("skip selection = %#v", got)
	}
	if len(got.Skipped) != 3 {
		t.Fatalf("skipped positions = %#v, want three", got.Skipped)
	}
	none := store.selectTarget(testSelection(key, map[string]struct{}{}, time.Hour, true))
	if none.Outcome != selectionExhausted {
		t.Fatalf("all-unavailable selection = %#v", none)
	}
	all := map[string]struct{}{"codex": {}, "claude": {}}
	got = store.selectTarget(testSelection(key, all, time.Hour, true))
	if got.Outcome != selectionAdvanced || got.Index != 0 || got.Target.Model != "terra" {
		t.Fatalf("cursor changed after unavailable selection: %#v", got)
	}
}

func TestTurnIdentityReplaysPositionAndHoldsCursor(t *testing.T) {
	store := newCursorStore(nil)
	key := cursorKey{Generation: 1, Alias: "replay", SessionID: "a"}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	repeated := &turnIdentity{fingerprint: "state-one"}

	request := testSelection(key, available, time.Hour, false)
	request.Turn = repeated
	first := store.selectTarget(request)
	if first.Outcome != selectionAdvanced || first.Index != 0 {
		t.Fatalf("first selection = %#v", first)
	}
	second := store.selectTarget(request)
	if second.Outcome != selectionReplayed || second.Index != 0 {
		t.Fatalf("repeated state = %#v, want replay of position 0", second)
	}
	changed := testSelection(key, available, time.Hour, false)
	changed.Turn = &turnIdentity{fingerprint: "state-two"}
	third := store.selectTarget(changed)
	if third.Outcome != selectionAdvanced || third.Index != 1 {
		t.Fatalf("changed state = %#v, want advance to position 1", third)
	}
}

func TestAbsentTurnIdentityAdvancesAndStoresNoMemo(t *testing.T) {
	store := newCursorStore(nil)
	key := cursorKey{Generation: 1, Alias: "absent", SessionID: "a"}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	request := testSelection(key, available, time.Hour, false)

	first := store.selectTarget(request)
	second := store.selectTarget(request)
	if first.Outcome != selectionAdvanced || second.Outcome != selectionAdvanced {
		t.Fatalf("absent identity outcomes = %v / %v", first.Outcome, second.Outcome)
	}
	if first.Index != 0 || second.Index != 1 {
		t.Fatalf("absent identity indexes = %d / %d", first.Index, second.Index)
	}
}

func TestRepeatedTurnReprobesItsOwnPosition(t *testing.T) {
	store := newCursorStore(nil)
	key := cursorKey{Generation: 1, Alias: "reprobe", SessionID: "a"}
	all := map[string]struct{}{"codex": {}, "claude": {}}
	repeated := &turnIdentity{fingerprint: "state-one"}

	reserve := testSelection(key, all, time.Hour, false)
	reserve.Turn = repeated
	if first := store.selectTarget(reserve); first.Index != 0 {
		t.Fatalf("reserved position = %#v", first)
	}

	claudeOnly := map[string]struct{}{"claude": {}}
	held := testSelection(key, claudeOnly, time.Hour, false)
	held.Turn = repeated
	held.ProbeLimit = 1
	exhausted := store.selectTarget(held)
	if exhausted.Outcome != selectionExhausted {
		t.Fatalf("single probe outcome = %#v", exhausted)
	}
	if len(exhausted.Skipped) != 1 || exhausted.Skipped[0].Index != 0 || exhausted.Skipped[0].Provider != "codex" {
		t.Fatalf("single probe skipped = %#v, want its own position", exhausted.Skipped)
	}

	walk := testSelection(key, claudeOnly, time.Hour, false)
	walk.Turn = repeated
	reassigned := store.selectTarget(walk)
	if reassigned.Outcome != selectionAdvanced || reassigned.Index != 3 {
		t.Fatalf("forward walk = %#v, want position 3", reassigned)
	}
	if len(reassigned.Skipped) != 3 {
		t.Fatalf("forward walk skipped = %#v, want three notices", reassigned.Skipped)
	}
}

func TestSingleProbeHoldsCursorForNextRequest(t *testing.T) {
	store := newCursorStore(nil)
	key := cursorKey{Generation: 1, Alias: "held", SessionID: "a"}
	claudeOnly := map[string]struct{}{"claude": {}}
	held := testSelection(key, claudeOnly, time.Hour, false)
	held.ProbeLimit = 1
	if got := store.selectTarget(held); got.Outcome != selectionExhausted || len(got.Skipped) != 1 {
		t.Fatalf("held selection = %#v", got)
	}
	all := map[string]struct{}{"codex": {}, "claude": {}}
	recovered := store.selectTarget(testSelection(key, all, time.Hour, false))
	if recovered.Outcome != selectionAdvanced || recovered.Index != 0 {
		t.Fatalf("recovered selection = %#v, want held position 0", recovered)
	}
}

func TestExhaustedProbeHoldsRandomStartPosition(t *testing.T) {
	store := newCursorStore(nil)
	starts := []int{3, 1}
	store.random = func(int) int {
		start := starts[0]
		starts = starts[1:]
		return start
	}
	key := cursorKey{Generation: 1, Alias: "held", SessionID: "a"}
	held := testSelection(key, map[string]struct{}{}, time.Hour, true)
	held.ProbeLimit = 1
	if got := store.selectTarget(held); got.Outcome != selectionExhausted {
		t.Fatalf("exhausted selection = %#v", got)
	}
	all := map[string]struct{}{"codex": {}, "claude": {}}
	recovered := store.selectTarget(testSelection(key, all, time.Hour, true))
	if recovered.Outcome != selectionAdvanced || recovered.Index != 3 {
		t.Fatalf("recovered selection = %#v, want the drawn position 3", recovered)
	}
	if len(starts) != 1 {
		t.Fatalf("random draws = %d, want one", 2-len(starts))
	}
}

func TestStatelessAlwaysUsesFirstAvailable(t *testing.T) {
	available := map[string]struct{}{"claude": {}}
	for range 3 {
		got := selectStateless(testSequence(), available)
		if got.Outcome != selectionStateless || got.Index != 3 || got.Target.Model != "opus" {
			t.Fatalf("stateless selection = %#v", got)
		}
	}
	none := selectStateless(testSequence(), map[string]struct{}{})
	if none.Outcome != selectionExhausted {
		t.Fatalf("stateless exhaustion = %#v", none)
	}
}

func TestCursorConcurrentReservationsPreserveCounts(t *testing.T) {
	store := newCursorStore(nil)
	key := cursorKey{Generation: 1, Alias: "iterative", SessionID: "same"}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	const requests = 1003
	counts := make(map[string]int)
	var countsMu sync.Mutex
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := store.selectTarget(testSelection(key, available, time.Hour, false))
			if result.Outcome != selectionAdvanced {
				t.Error("selection failed")
				return
			}
			countsMu.Lock()
			counts[result.Target.Provider]++
			countsMu.Unlock()
		}()
	}
	wg.Wait()
	if counts["codex"] != 753 || counts["claude"] != 250 {
		t.Fatalf("counts = %#v, want codex=753 claude=250", counts)
	}
}

func TestConfigurationGenerationIsolated(t *testing.T) {
	store := newCursorStore(nil)
	available := map[string]struct{}{"codex": {}, "claude": {}}
	oldKey := cursorKey{Generation: 1, Alias: "iterative", SessionID: "a"}
	newKey := cursorKey{Generation: 2, Alias: "iterative", SessionID: "a"}
	_ = store.selectTarget(testSelection(oldKey, available, time.Hour, false))
	got := store.selectTarget(testSelection(newKey, available, time.Hour, false))
	if got.Index != 0 || got.Target.Model != "terra" {
		t.Fatalf("new generation selection = %#v", got)
	}
}
