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
	// At the deadline: the read relabels OPEN→HALF_OPEN and does not block.
	expiry := base.Add(poolBreakerResetBase)
	if _, blocked := b.blockDeadline("pool-a", expiry); blocked {
		t.Fatal("expected expired OPEN relabeled to HALF_OPEN without blocking")
	}
	// A SECOND read must also not block: reads are side-effect-free with
	// respect to the probe slot, so selection/model-filter reads can never
	// consume the probe before dispatch (regression pin).
	if _, blocked := b.blockDeadline("pool-a", expiry.Add(time.Second)); blocked {
		t.Fatal("expected repeated reads to stay unblocked in HALF_OPEN without a probe")
	}
	// Admission is explicit: the first commit takes the probe, the second does not.
	if !b.admitProbe("pool-a", expiry.Add(2*time.Second)) {
		t.Fatal("expected first admitProbe to take the probe")
	}
	if b.admitProbe("pool-a", expiry.Add(3*time.Second)) {
		t.Fatal("expected second admitProbe denied while probe in flight")
	}
	// While the probe is in flight: reads blocked with the deadline anchored
	// at probe admission time.
	probe2Time := base.Add(poolBreakerResetBase + 4*time.Second)
	deadline, blocked = b.blockDeadline("pool-a", probe2Time)
	if !blocked {
		t.Fatal("expected reads blocked while probe unresolved")
	}
	want := expiry.Add(2 * time.Second).Add(poolBreakerResetBase)
	if !deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", deadline, want)
	}
}

func TestPoolBreakerProbeFailureDoublesResetPeriod(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base)
	}
	// Expire (read relabels OPEN→HALF_OPEN), then commit takes the probe.
	if _, blocked := b.blockDeadline("pool-a", base.Add(poolBreakerResetBase)); blocked {
		t.Fatal("expected expired OPEN relabeled without blocking")
	}
	if !b.admitProbe("pool-a", base.Add(poolBreakerResetBase+time.Second)) {
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
		// Expire with a long jump so even the max period elapses, then take
		// the probe via admitProbe.
		now := base.Add(10 * time.Minute)
		if _, blocked := b.blockDeadline("pool-a", now); blocked {
			t.Fatalf("cycle %d: expected expired OPEN relabeled without blocking", cycle)
		}
		if !b.admitProbe("pool-a", now) {
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
		t.Fatal("expected expired OPEN relabeled without blocking")
	}
	if !b.admitProbe("pool-a", now.Add(time.Second)) {
		t.Fatal("expected probe admitted")
	}
	b.recordSuccess("pool-a", now.Add(2*time.Second))
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

func TestPoolBreakerHalfOpenBlockedDeadlineStable(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base)
	}
	// Expire (relabel) and take the probe at base+31s via admitProbe.
	expiry := base.Add(poolBreakerResetBase)
	if _, blocked := b.blockDeadline("pool-a", expiry); blocked {
		t.Fatal("expected expired OPEN relabeled without blocking")
	}
	probeAdmittedAt := expiry.Add(time.Second)
	if !b.admitProbe("pool-a", probeAdmittedAt) {
		t.Fatal("expected probe admitted")
	}
	// Two blocked calls at different nows (both inside the probe window)
	// must see the SAME deadline, anchored at probe admission, not drifting.
	call1 := base.Add(2*poolBreakerResetBase - 10*time.Second)
	call2 := call1.Add(5 * time.Second)
	d1, blocked1 := b.blockDeadline("pool-a", call1)
	d2, blocked2 := b.blockDeadline("pool-a", call2)
	if !blocked1 || !blocked2 {
		t.Fatal("expected both calls blocked while probe unresolved")
	}
	if !d1.Equal(d2) {
		t.Fatalf("unstable deadline: first call %v, second call %v", d1, d2)
	}
	if !d1.Equal(probeAdmittedAt.Add(poolBreakerResetBase)) {
		t.Fatalf("deadline = %v, want anchored %v", d1, probeAdmittedAt.Add(poolBreakerResetBase))
	}
}

func TestPoolBreakerHalfOpenFreshProbeAfterWindowExpiry(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base)
	}
	// Expire (relabel) and take the first probe.
	expiry := base.Add(poolBreakerResetBase)
	if _, blocked := b.blockDeadline("pool-a", expiry); blocked {
		t.Fatal("expected expired OPEN relabeled without blocking")
	}
	probeAdmittedAt := expiry.Add(time.Second)
	if !b.admitProbe("pool-a", probeAdmittedAt) {
		t.Fatal("expected first probe admitted")
	}
	// While the probe window is live: blocked.
	if _, blocked := b.blockDeadline("pool-a", probeAdmittedAt.Add(poolBreakerResetBase/2)); !blocked {
		t.Fatal("expected blocked while probe window live")
	}
	// The in-flight probe's window elapses without a verdict: reads re-arm
	// (still unblocked) and a fresh probe is admitted at commit.
	afterWindow := probeAdmittedAt.Add(poolBreakerResetBase)
	if _, blocked := b.blockDeadline("pool-a", afterWindow); blocked {
		t.Fatal("expected reads unblocked after probe window expiry")
	}
	if !b.admitProbe("pool-a", afterWindow.Add(time.Second)) {
		t.Fatal("expected fresh probe admitted after probe window expiry")
	}
	// The fresh probe again blocks other callers, anchored at the re-arm time.
	deadline, blocked := b.blockDeadline("pool-a", afterWindow.Add(2*time.Second))
	if !blocked {
		t.Fatal("expected blocked after fresh probe admitted")
	}
	if !deadline.Equal(afterWindow.Add(time.Second).Add(poolBreakerResetBase)) {
		t.Fatalf("deadline = %v, want %v", deadline, afterWindow.Add(time.Second).Add(poolBreakerResetBase))
	}
}

func TestPoolBreakerFailureWhileOpenKeepsOpenedAt(t *testing.T) {
	t.Parallel()
	b := &poolBreaker{pools: make(map[string]*poolBreakerEntry)}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		b.recordFailure("pool-a", base)
	}
	b.mu.Lock()
	openedAt := b.pools["pool-a"].openedAt
	b.mu.Unlock()
	// A failure while OPEN must be ignored: openedAt untouched.
	b.recordFailure("pool-a", base.Add(10*time.Second))
	b.mu.Lock()
	after := b.pools["pool-a"].openedAt
	b.mu.Unlock()
	if !after.Equal(openedAt) {
		t.Fatalf("openedAt changed while OPEN: %v -> %v", openedAt, after)
	}
	// Deadline still anchored at the original opening.
	deadline, blocked := b.blockDeadline("pool-a", base.Add(time.Second))
	if !blocked || !deadline.Equal(openedAt.Add(poolBreakerResetBase)) {
		t.Fatalf("deadline = %v blocked=%v, want %v blocked", deadline, blocked, openedAt.Add(poolBreakerResetBase))
	}
}
