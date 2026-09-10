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
