package auth

import (
	"strings"
	"testing"
	"time"
)

type testRefreshEvaluator struct{}

func (testRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

func setRefreshLeadFactory(t *testing.T, provider string, factory func() *time.Duration) {
	t.Helper()
	key := strings.ToLower(strings.TrimSpace(provider))
	refreshLeadMu.Lock()
	prev, hadPrev := refreshLeadFactories[key]
	if factory == nil {
		delete(refreshLeadFactories, key)
	} else {
		refreshLeadFactories[key] = factory
	}
	refreshLeadMu.Unlock()
	t.Cleanup(func() {
		refreshLeadMu.Lock()
		if hadPrev {
			refreshLeadFactories[key] = prev
		} else {
			delete(refreshLeadFactories, key)
		}
		refreshLeadMu.Unlock()
	})
}

func TestNextRefreshCheckAt_DisabledUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "disabled-schedule", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "a1",
		Provider: "disabled-schedule",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_APIKeyUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a1", Provider: "test", Attributes: map[string]string{"api_key": "k"}}
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false")
	}
}

func TestNextRefreshCheckAt_NextRefreshAfterGate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	nextAfter := now.Add(30 * time.Minute)
	auth := &Auth{
		ID:               "a1",
		Provider:         "test",
		NextRefreshAfter: nextAfter,
		Metadata:         map[string]any{"email": "x@example.com"},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	if !got.Equal(nextAfter) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, nextAfter)
	}
}

func TestNextRefreshCheckAt_PreferredInterval_PicksEarliestCandidate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(20 * time.Minute)
	auth := &Auth{
		ID:              "a1",
		Provider:        "test",
		LastRefreshedAt: now,
		Metadata: map[string]any{
			"email":                    "x@example.com",
			"expires_at":               expiry.Format(time.RFC3339),
			"refresh_interval_seconds": 900, // 15m
		},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-15 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_ProviderLead_Expiry(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "provider-lead-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "a1",
		Provider: "provider-lead-expiry",
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_RelativeExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	issuedAt := now.Add(-15 * time.Minute)
	lead := 30 * time.Minute
	setRefreshLeadFactory(t, "relative-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "relative-expiry-auth",
		Provider: "relative-expiry",
		Metadata: map[string]any{
			"access_token": "test-access",
			"expires_in":   3600,
			"timestamp":    int(issuedAt.UnixMilli()),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	want := issuedAt.Add(time.Hour - lead)
	if !ok || !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = (%s, %t), want (%s, true)", got, ok, want)
	}
}

func TestNextRefreshCheckAt_RefreshEvaluatorFallback(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Metadata: map[string]any{"email": "x@example.com"},
		Runtime:  testRefreshEvaluator{},
	}
	got, ok := nextRefreshCheckAt(now, auth, interval)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := now.Add(interval)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestAuthAutoRefreshLoop_TimerWaitClampedForLongNextExpiry(t *testing.T) {
	loop := newAuthAutoRefreshLoop(nil, 5*time.Second, 1)
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	loop.upsert("a1", now.Add(time.Hour))

	got, ok := loop.nextWait(now)
	if !ok {
		t.Fatalf("nextWait() ok = false, want true")
	}
	if got > maxRefreshTimerWait {
		t.Fatalf("nextWait() = %v, want <= %v", got, maxRefreshTimerWait)
	}
	if got != maxRefreshTimerWait {
		t.Fatalf("nextWait() = %v, want exactly %v", got, maxRefreshTimerWait)
	}

	// Short wait should not be clamped
	shortNext := now.Add(10 * time.Second)
	loop.upsert("a2", shortNext)
	gotShort, okShort := loop.nextWait(now)
	if !okShort || gotShort != 10*time.Second {
		t.Fatalf("nextWait() short = (%v, %t), want (10s, true)", gotShort, okShort)
	}
}

func TestAuthAutoRefreshLoop_PopDue_AfterSystemSuspendResume(t *testing.T) {
	loop := newAuthAutoRefreshLoop(nil, 5*time.Second, 1)
	beforeSleep := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)

	// Credential scheduled to refresh 50 minutes later
	scheduledRefresh := beforeSleep.Add(50 * time.Minute)
	loop.upsert("gemini-oauth", scheduledRefresh)

	// Before sleep, wait is clamped to maxRefreshTimerWait
	wait, ok := loop.nextWait(beforeSleep)
	if !ok || wait != maxRefreshTimerWait {
		t.Fatalf("nextWait() before sleep = (%v, %t), want (%v, true)", wait, ok, maxRefreshTimerWait)
	}

	// Not due yet before sleep
	dueBefore := loop.popDue(beforeSleep)
	if len(dueBefore) != 0 {
		t.Fatalf("popDue() before sleep = %v, want empty", dueBefore)
	}

	// System resumes 2 hours later (monotonic clock froze, but wall clock jumped)
	afterResume := beforeSleep.Add(2 * time.Hour)

	// Upon wake, the clamped timer fires within maxRefreshTimerWait and checks with current wall clock
	waitAfter, okAfter := loop.nextWait(afterResume)
	if !okAfter || waitAfter != 0 {
		t.Fatalf("nextWait() after resume = (%v, %t), want (0, true)", waitAfter, okAfter)
	}

	dueAfter := loop.popDue(afterResume)
	if len(dueAfter) != 1 || dueAfter[0] != "gemini-oauth" {
		t.Fatalf("popDue() after resume = %v, want [gemini-oauth]", dueAfter)
	}
}
