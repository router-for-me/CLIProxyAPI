package main

import (
	"testing"
	"time"
)

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
		t.Fatalf("recovered selection = %#v, want kept position 0", recovered)
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
