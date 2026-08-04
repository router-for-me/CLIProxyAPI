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

func TestCursorSequenceWrapAndIndependentKeys(t *testing.T) {
	now := time.Unix(100, 0)
	store := newCursorStore(func() time.Time { return now })
	available := map[string]struct{}{"codex": {}, "claude": {}}
	keyA := cursorKey{Generation: 1, Alias: "iterative", SessionID: "a"}
	keyB := cursorKey{Generation: 1, Alias: "iterative", SessionID: "b"}
	want := []string{"terra", "terra", "terra", "opus", "terra"}
	for i, model := range want {
		got, _, ok := store.selectTarget(keyA, testSequence(), available, time.Hour, true)
		if !ok || got.Model != model {
			t.Fatalf("session A selection %d = %#v, %v; want %q", i, got, ok, model)
		}
	}
	gotB, _, okB := store.selectTarget(keyB, testSequence(), available, time.Hour, true)
	if !okB || gotB.Model != "terra" {
		t.Fatalf("session B first selection = %#v, %v", gotB, okB)
	}
	otherAlias := cursorKey{Generation: 1, Alias: "other", SessionID: "a"}
	gotAlias, _, okAlias := store.selectTarget(otherAlias, testSequence(), available, time.Hour, true)
	if !okAlias || gotAlias.Model != "terra" {
		t.Fatalf("other alias first selection = %#v, %v", gotAlias, okAlias)
	}
}

func TestCursorPeekExpirationAndSlidingTTL(t *testing.T) {
	now := time.Unix(100, 0)
	store := newCursorStore(func() time.Time { return now })
	key := cursorKey{Generation: 1, Alias: "iterative", SessionID: "a"}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	first, _, _ := store.selectTarget(key, testSequence(), available, time.Minute, false)
	generated, _, _ := store.selectTarget(key, testSequence(), available, time.Minute, true)
	if first != generated {
		t.Fatalf("peek = %#v, generation = %#v", first, generated)
	}
	now = now.Add(30 * time.Second)
	_, _, _ = store.selectTarget(key, testSequence(), available, time.Minute, false)
	now = now.Add(31 * time.Second)
	next, index, _ := store.selectTarget(key, testSequence(), available, time.Minute, true)
	if index != 1 || next.Model != "terra" {
		t.Fatalf("sliding TTL selection = %#v at %d, want position 1", next, index)
	}
	now = now.Add(time.Minute)
	restarted, index, _ := store.selectTarget(key, testSequence(), available, time.Minute, true)
	if index != 0 || restarted.Model != "terra" {
		t.Fatalf("expired selection = %#v at %d, want restart", restarted, index)
	}
}

func TestRandomStartChosenOnceAndPeekedWithoutAdvancing(t *testing.T) {
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

	peeked, peekIndex, okPeek := store.selectTarget(key, testSequence(), available, time.Hour, false, true)
	generated, generatedIndex, okGenerated := store.selectTarget(key, testSequence(), available, time.Hour, true, true)
	if !okPeek || !okGenerated || peekIndex != 3 || generatedIndex != 3 || peeked.Model != "opus" || generated != peeked {
		t.Fatalf("peek/generation = %#v@%d / %#v@%d", peeked, peekIndex, generated, generatedIndex)
	}
	next, nextIndex, okNext := store.selectTarget(key, testSequence(), available, time.Hour, true, true)
	if !okNext || nextIndex != 0 || next.Model != "terra" {
		t.Fatalf("next = %#v@%d", next, nextIndex)
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
	_, firstIndex, _ := store.selectTarget(cursorKey{Generation: 1, Alias: "random", SessionID: "a"}, testSequence(), available, time.Hour, true, true)
	_, secondIndex, _ := store.selectTarget(cursorKey{Generation: 1, Alias: "random", SessionID: "b"}, testSequence(), available, time.Hour, true, true)
	if firstIndex != 2 || secondIndex != 3 {
		t.Fatalf("random starts = %d/%d", firstIndex, secondIndex)
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

	_, firstIndex, _ := store.selectTarget(key, testSequence(), available, time.Minute, true, true)
	now = now.Add(time.Minute)
	_, secondIndex, _ := store.selectTarget(key, testSequence(), available, time.Minute, true, true)
	if firstIndex != 1 || secondIndex != 3 {
		t.Fatalf("random starts before/after expiration = %d/%d", firstIndex, secondIndex)
	}
}

func TestProviderSkippingAndUnavailableDoesNotAdvance(t *testing.T) {
	store := newCursorStore(nil)
	key := cursorKey{Generation: 1, Alias: "iterative", SessionID: "a"}
	claudeOnly := map[string]struct{}{"claude": {}}
	got, index, ok := store.selectTarget(key, testSequence(), claudeOnly, time.Hour, true)
	if !ok || index != 3 || got.Model != "opus" {
		t.Fatalf("skip selection = %#v at %d, %v", got, index, ok)
	}
	if _, _, okNone := store.selectTarget(key, testSequence(), map[string]struct{}{}, time.Hour, true); okNone {
		t.Fatal("all-unavailable selection succeeded")
	}
	all := map[string]struct{}{"codex": {}, "claude": {}}
	got, index, ok = store.selectTarget(key, testSequence(), all, time.Hour, true)
	if !ok || index != 0 || got.Model != "terra" {
		t.Fatalf("cursor changed after unavailable selection: %#v at %d", got, index)
	}
}

func TestStatelessAlwaysUsesFirstAvailable(t *testing.T) {
	available := map[string]struct{}{"claude": {}}
	for range 3 {
		got, index, ok := selectStateless(testSequence(), available)
		if !ok || index != 3 || got.Model != "opus" {
			t.Fatalf("stateless selection = %#v at %d, %v", got, index, ok)
		}
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
			target, _, ok := store.selectTarget(key, testSequence(), available, time.Hour, true)
			if !ok {
				t.Error("selection failed")
				return
			}
			countsMu.Lock()
			counts[target.Provider]++
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
	_, _, _ = store.selectTarget(oldKey, testSequence(), available, time.Hour, true)
	target, index, _ := store.selectTarget(newKey, testSequence(), available, time.Hour, true)
	if index != 0 || target.Model != "terra" {
		t.Fatalf("new generation selection = %#v at %d", target, index)
	}
}
