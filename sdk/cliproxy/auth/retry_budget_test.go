package auth

import (
	"context"
	"testing"
	"time"
)

func TestExitReasonStringValues(t *testing.T) {
	cases := map[ExitReason]string{
		ReasonSuccess:       "success",
		ReasonNonTransient:  "non_transient",
		ReasonAttemptsOut:   "attempts_exhausted",
		ReasonBudgetOut:     "budget_exhausted",
		ReasonStreamStarted: "stream_started",
		ReasonParentCtxDone: "parent_ctx_done",
	}
	for r, want := range cases {
		if string(r) != want {
			t.Fatalf("ExitReason(%q) = %q, want %q", r, string(r), want)
		}
	}
}

func TestEntryBudgetReturnsSubContext(t *testing.T) {
	parent := context.Background()
	ctx, cancel, deadline := EntryBudget(parent, EntryBudgetOpts{MaxTimeMS: 1000})
	defer cancel()
	if ctx == nil || ctx == parent {
		t.Fatalf("expected derived context, got %v", ctx)
	}
	remaining := time.Until(deadline)
	if remaining > 1100*time.Millisecond {
		t.Fatalf("deadline too far in future: %v", remaining)
	}
	if remaining < 900*time.Millisecond {
		t.Fatalf("deadline too close: %v", remaining)
	}
}

func TestEntryBudgetUsesDefaultWhenZero(t *testing.T) {
	_, cancel, deadline := EntryBudget(context.Background(), EntryBudgetOpts{})
	defer cancel()
	remaining := time.Until(deadline)
	maxDur := time.Duration(DefaultMaxTimeMS) * time.Millisecond
	if remaining > maxDur+100*time.Millisecond {
		t.Fatalf("deadline exceeds default: %v", remaining)
	}
	if remaining < maxDur-time.Millisecond {
		t.Fatalf("deadline below default: %v", remaining)
	}
}

func TestEntryBudgetRespectsParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, c2, deadline := EntryBudget(parent, EntryBudgetOpts{MaxTimeMS: 5000})
	defer c2()
	// Deadline should be clamped to the parent's (≈30ms), not 5s.
	if time.Until(deadline) > 100*time.Millisecond {
		t.Fatalf("deadline not clamped to parent: %v", time.Until(deadline))
	}
}

func TestNextBackoffExponentialClampedToBudget(t *testing.T) {
	deadline := time.Now().Add(500 * time.Millisecond)
	// base=100ms, attempt=4 → 100*8=800ms; clamped to 500/2=250ms.
	got := NextBackoff(deadline, 100, 4)
	// Allow 1ms slack — division by 2 of a sub-millisecond duration can
	// floor below 250ms.
	if got < 249*time.Millisecond || got > 250*time.Millisecond {
		t.Fatalf("attempt 4 base 100 deadline+500ms: got %v, want ~250ms", got)
	}
}

func TestNextBackoffDefaultsWhenBaseZero(t *testing.T) {
	deadline := time.Now().Add(5 * time.Second)
	got := NextBackoff(deadline, 0, 1)
	// base=200ms, attempt=1 → 200ms; well within budget.
	want := time.Duration(DefaultBackoffMS) * time.Millisecond
	if got != want {
		t.Fatalf("default base attempt 1: got %v, want %v", got, want)
	}
}

func TestNextBackoffClampOnLargeAttempt(t *testing.T) {
	deadline := time.Now().Add(100 * time.Millisecond)
	// base=100ms, attempt=20 → cap shift at 16; b > cap so returns cap.
	got := NextBackoff(deadline, 100, 20)
	// remaining ≈ 50ms (half of 100ms); shift caps so b≈6.55s, return 50ms.
	if got > time.Until(deadline) {
		t.Fatalf("backoff exceeded deadline: %v", got)
	}
	if got <= 0 {
		t.Fatalf("backoff must be positive: %v", got)
	}
}
