package policy

import (
	"testing"
	"time"
)

func TestSlidingWindowForgetPrefix(t *testing.T) {
	w := newSlidingWindow()
	// Seed counters for two principals, each with per-model buckets, plus a
	// third principal that must stay untouched.
	w.AllowAndIncrement("k1", 100, time.Minute)
	w.AllowAndIncrement("k1|gpt-4o", 100, time.Minute)
	w.AllowAndIncrement("k1|gpt-4o-mini", 100, time.Minute)
	w.AllowAndIncrement("k2|gpt-4o", 100, time.Minute)

	w.ForgetPrefix("k1|")

	if got := w.SnapshotCurrent("k1|gpt-4o", time.Minute); got != 0 {
		t.Fatalf("k1|gpt-4o should be cleared, got %d", got)
	}
	if got := w.SnapshotCurrent("k1|gpt-4o-mini", time.Minute); got != 0 {
		t.Fatalf("k1|gpt-4o-mini should be cleared, got %d", got)
	}
	// k1's own bare counter does NOT match the "k1|" prefix and must survive
	// (it is Forget()'d separately by InvalidateKey).
	if got := w.SnapshotCurrent("k1", time.Minute); got != 1 {
		t.Fatalf("k1 bare counter must be untouched (ForgetPrefix uses the '|' suffix), got %d", got)
	}
	if got := w.SnapshotCurrent("k2|gpt-4o", time.Minute); got != 1 {
		t.Fatalf("k2|gpt-4o must be untouched, got %d", got)
	}
}
