package auth

import (
	"testing"
	"time"
)

func TestWarningRingCoalescesConsecutiveDuplicates(t *testing.T) {
	a := &Auth{}
	base := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)

	a.recordWarning(base, "rate_limit", "", "429 too many requests", 429, "claude-3")
	a.recordWarning(base.Add(time.Second), "rate_limit", "", "429 too many requests", 429, "claude-3")
	a.recordWarning(base.Add(2*time.Second), "rate_limit", "", "429 too many requests", 429, "claude-3")

	events, total := a.WarningsSnapshot()
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (coalesced)", len(events))
	}
	if events[0].Count != 3 {
		t.Fatalf("Count = %d, want 3", events[0].Count)
	}
	if !events[0].FirstAt.Equal(base) {
		t.Fatalf("FirstAt = %v, want %v", events[0].FirstAt, base)
	}
	if !events[0].LastAt.Equal(base.Add(2 * time.Second)) {
		t.Fatalf("LastAt = %v, want %v", events[0].LastAt, base.Add(2*time.Second))
	}
}

func TestWarningRingSnapshotNewestFirst(t *testing.T) {
	a := &Auth{}
	base := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)

	a.recordWarning(base, "unauthorized", "", "401", 401, "")
	a.recordWarning(base.Add(time.Second), "rate_limit", "", "429", 429, "")
	a.recordWarning(base.Add(2*time.Second), "server_error", "", "503", 503, "")

	events, total := a.WarningsSnapshot()
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	want := []string{"server_error", "rate_limit", "unauthorized"}
	if len(events) != len(want) {
		t.Fatalf("len(events) = %d, want %d", len(events), len(want))
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("events[%d].Kind = %q, want %q", i, events[i].Kind, kind)
		}
	}
}

func TestWarningRingEvictsButKeepsTotal(t *testing.T) {
	a := &Auth{}
	base := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)

	// Record more distinct warnings than the ring capacity.
	n := warningRingCapacity + 10
	for i := 0; i < n; i++ {
		a.recordWarning(base.Add(time.Duration(i)*time.Second), "error", "", "msg", 500+i, "")
	}

	events, total := a.WarningsSnapshot()
	if total != int64(n) {
		t.Fatalf("total = %d, want %d", total, n)
	}
	if len(events) != warningRingCapacity {
		t.Fatalf("len(events) = %d, want %d (capacity)", len(events), warningRingCapacity)
	}
	// Newest entry should be the last recorded one.
	if events[0].HTTPStatus != 500+n-1 {
		t.Fatalf("newest HTTPStatus = %d, want %d", events[0].HTTPStatus, 500+n-1)
	}
}

func TestWarningRingClonedByValue(t *testing.T) {
	a := &Auth{}
	a.recordWarning(time.Now(), "error", "", "boom", 500, "")
	clone := a.Clone()
	// Mutating the clone must not affect the original ring.
	clone.recordWarning(time.Now(), "error2", "", "boom2", 500, "")

	_, origTotal := a.WarningsSnapshot()
	_, cloneTotal := clone.WarningsSnapshot()
	if origTotal != 1 {
		t.Fatalf("original total = %d, want 1 (clone is by-value)", origTotal)
	}
	if cloneTotal != 2 {
		t.Fatalf("clone total = %d, want 2", cloneTotal)
	}
}
