package auth

import (
	"testing"
	"time"
)

func TestPoolBreakerThresholdTrip(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base.Add(time.Duration(i)*time.Second))
	}
	// The 8th failure (threshold hit) is at base+7s.
	lastFailure := base.Add(7 * time.Second)
	deadline, blocked := b.blockDeadline("pool-a", base)
	if !blocked {
		t.Fatal("expected pool blocked after 8 failures within window")
	}
	want := lastFailure.Add(poolBreakerResetBase)
	if !deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", deadline, want)
	}
}

func TestPoolBreakerBelowThresholdNotBlocked(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		b.recordFailure("pool-a", base.Add(time.Duration(i)*time.Second))
	}
	if _, blocked := b.blockDeadline("pool-a", base); blocked {
		t.Fatal("expected pool not blocked after 7 failures")
	}
}

func TestPoolBreakerFixedWindowReset(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	// 8 failures spread beyond one 60s window: the window resets after 60s.
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base.Add(time.Duration(i)*61*time.Second))
	}
	if _, blocked := b.blockDeadline("pool-a", base); blocked {
		t.Fatal("expected pool not blocked when failures span beyond one window")
	}
}

func TestPoolBreakerOpenBlocksThenAdmitsOneProbe(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base)
	}
	// Still inside the reset period: blocked.
	probeTime := base.Add(10 * time.Second)
	deadline, blocked := b.blockDeadline("pool-a", probeTime)
	if !blocked {
		t.Fatal("expected pool blocked before reset deadline")
	}
	if !deadline.Equal(base.Add(poolBreakerResetBase)) {
		t.Fatalf("deadline = %v, want %v", deadline, base.Add(poolBreakerResetBase))
	}
	// At the deadline: lazy transition admits exactly one probe.
	_, blocked = b.blockDeadline("pool-a", base.Add(poolBreakerResetBase))
	if blocked {
		t.Fatal("expected first post-expiry call to admit the probe (not blocked)")
	}
	// Second call while probe unresolved: blocked again.
	probe2Time := base.Add(poolBreakerResetBase + time.Second)
	deadline, blocked = b.blockDeadline("pool-a", probe2Time)
	if !blocked {
		t.Fatal("expected second post-expiry call blocked while probe unresolved")
	}
	if !deadline.Equal(probe2Time.Add(poolBreakerResetBase)) {
		t.Fatalf("deadline = %v, want %v", deadline, probe2Time.Add(poolBreakerResetBase))
	}
}

func TestPoolBreakerProbeFailureDoublesResetPeriod(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base)
	}
	// Expire and admit the probe.
	_, blocked := b.blockDeadline("pool-a", base.Add(poolBreakerResetBase))
	if blocked {
		t.Fatal("expected probe admitted")
	}
	// Probe failure re-opens with doubled reset period.
	openedAt := base.Add(poolBreakerResetBase + 5*time.Second)
	b.recordFailure("pool-a", openedAt)
	deadline, blocked := b.blockDeadline("pool-a", openedAt)
	if !blocked {
		t.Fatal("expected pool blocked after probe failure")
	}
	want := openedAt.Add(2 * poolBreakerResetBase)
	if !deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v (doubled reset period)", deadline, want)
	}
}

func TestPoolBreakerResetPeriodCappedAtMax(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	// Cycle open/probe-fail repeatedly; period doubles 30s->60s->120s->240s->480s capped 300s.
	for cycle := 0; cycle < 5; cycle++ {
		for i := 0; i < 8; i++ {
			b.recordFailure("pool-a", base)
		}
		// Expire with a long jump so even the max period elapses.
		now := base.Add(10 * time.Minute)
		if _, blocked := b.blockDeadline("pool-a", now); blocked {
			t.Fatalf("cycle %d: expected probe admitted", cycle)
		}
		openedAt := now.Add(time.Second)
		b.recordFailure("pool-a", openedAt)
		deadline, blocked := b.blockDeadline("pool-a", openedAt)
		if !blocked {
			t.Fatalf("cycle %d: expected blocked after probe failure", cycle)
		}
		want := openedAt.Add(poolBreakerResetMax)
		if cycle < 3 {
			// 30s, 60s, 120s, 240s are all below the cap.
			want = openedAt.Add(poolBreakerResetBase << uint(cycle+1))
		}
		if !deadline.Equal(want) {
			t.Fatalf("cycle %d: deadline = %v, want %v", cycle, deadline, want)
		}
		base = openedAt
	}
}

func TestPoolBreakerProbeSuccessCloses(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base)
	}
	now := base.Add(poolBreakerResetBase)
	if _, blocked := b.blockDeadline("pool-a", now); blocked {
		t.Fatal("expected probe admitted")
	}
	b.recordSuccess("pool-a", now.Add(time.Second))
	if _, blocked := b.blockDeadline("pool-a", now.Add(2*time.Second)); blocked {
		t.Fatal("expected pool closed after probe success")
	}
	b.mu.Lock()
	_, exists := b.pools["pool-a"]
	b.mu.Unlock()
	if exists {
		t.Fatal("expected entry removed after success")
	}
}

func TestPoolBreakerSuccessInClosedClearsWindow(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		b.recordFailure("pool-a", base)
	}
	b.recordSuccess("pool-a", base.Add(time.Second))
	// Only 4 more failures within a fresh window: below threshold, not blocked.
	for i := 0; i < 4; i++ {
		b.recordFailure("pool-a", base.Add(2*time.Second))
	}
	if _, blocked := b.blockDeadline("pool-a", base.Add(3*time.Second)); blocked {
		t.Fatal("expected success to clear the failure window")
	}
}

func TestPoolBreakerUnknownKey(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	if _, blocked := b.blockDeadline("missing", now); blocked {
		t.Fatal("expected unknown key not blocked")
	}
	b.recordSuccess("missing", now) // must not panic
}

func TestPoolBreakerEmptyKeyNoOp(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	b.recordFailure("", now)
	b.recordSuccess("", now)
	if _, blocked := b.blockDeadline("", now); blocked {
		t.Fatal("expected empty key not blocked")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pools) != 0 {
		t.Fatalf("expected no entries for empty key, got %d", len(b.pools))
	}
}

func TestGlobalPoolBreakerInitialized(t *testing.T) {
	t.Parallel()
	if globalPoolBreaker == nil {
		t.Fatal("globalPoolBreaker must be initialized")
	}
	if globalPoolBreaker.pools == nil {
		t.Fatal("globalPoolBreaker.pools must be initialized")
	}
}
