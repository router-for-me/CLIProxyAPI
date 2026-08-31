package main

import (
	"testing"
	"time"
)

// TestLaneObservationSweepReleasesOnlyExpiredLanes verifies that the shared sweep
// releases an expired diagnostic lane while preserving a live lane's continuity.
func TestLaneObservationSweepReleasesOnlyExpiredLanes(t *testing.T) {
	now := time.Unix(100, 0)
	store := newLaneObservationStore(func() time.Time { return now })
	ttl := time.Minute
	departedLane := laneObservationKey{Generation: 1, Alias: "ruby", SessionID: "conv:departed", Provider: "codex", Model: "gpt-5.6-sol"}
	activeLane := laneObservationKey{Generation: 1, Alias: "ruby", SessionID: "conv:active", Provider: "claude", Model: "claude-opus-5"}
	departedRequest := requestObservation{SystemFingerprint: "system-departed", HistoryItems: []string{"departed-opening"}}
	activeRequest := requestObservation{SystemFingerprint: "system-active", HistoryItems: []string{"active-opening"}}

	store.observe(departedLane, departedRequest, ttl)
	now = now.Add(2 * time.Minute)
	store.observe(activeLane, activeRequest, ttl)

	cleanupExpiredStores(store)

	if size := store.size(); size != 1 {
		t.Fatalf("lanes held after sweep = %d, want 1", size)
	}
	if continuity := store.observe(activeLane, activeRequest, ttl)["lane_continuity"]; continuity != "warm_prefix_candidate" {
		t.Fatalf("live lane continuity = %v, want warm_prefix_candidate", continuity)
	}
	if continuity := store.observe(departedLane, departedRequest, ttl)["lane_continuity"]; continuity != "first_observation" {
		t.Fatalf("swept lane continuity = %v, want first_observation", continuity)
	}
}

// TestCursorSweepReleasesOnlyExpiredConversations verifies that the shared sweep
// releases an expired cursor while preserving a live cursor's next position.
func TestCursorSweepReleasesOnlyExpiredConversations(t *testing.T) {
	now := time.Unix(100, 0)
	store := newCursorStore(func() time.Time { return now })
	sequence := []compiledTarget{
		{Provider: "codex", Model: "gpt-5.6-sol"},
		{Provider: "claude", Model: "claude-opus-5"},
	}
	available := map[string]struct{}{"codex": {}, "claude": {}}
	departed := cursorKey{Generation: 1, Alias: "ruby", SessionID: "conv:departed"}
	active := cursorKey{Generation: 1, Alias: "ruby", SessionID: "conv:active"}
	reserve := func(key cursorKey) selectionResult {
		return store.selectTarget(selectionRequest{
			Key:        key,
			Sequence:   sequence,
			Available:  available,
			TTL:        time.Minute,
			ProbeLimit: len(sequence),
		})
	}

	reserve(departed)
	now = now.Add(2 * time.Minute)
	reserve(active)

	cleanupExpiredStores(store)

	if size := store.size(); size != 1 {
		t.Fatalf("cursors held after sweep = %d, want 1", size)
	}
	if index := reserve(departed).Index; index != 0 {
		t.Fatalf("swept conversation resumed at position %d, want a fresh position 0", index)
	}
	if index := reserve(active).Index; index != 1 {
		t.Fatalf("live conversation resumed at position %d, want its reserved position 1", index)
	}
}
