package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

type testRefreshEvaluator struct{}

func (testRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

type pacedRefreshExecutor struct {
	schedulerProviderTestExecutor
	refreshed chan string
}

func (e pacedRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshed <- auth.ID
	return auth, nil
}

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

func TestRefreshRateInterval(t *testing.T) {
	testCases := []struct {
		name         string
		maxPerMinute int
		want         time.Duration
	}{
		{name: "disabled", maxPerMinute: 0, want: 0},
		{name: "negative", maxPerMinute: -1, want: 0},
		{name: "one per minute", maxPerMinute: 1, want: time.Minute},
		{name: "one per second", maxPerMinute: 60, want: time.Second},
		{name: "two per second", maxPerMinute: 120, want: 500 * time.Millisecond},
		{name: "minimum spacing", maxPerMinute: 120000, want: time.Millisecond},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if got := refreshRateInterval(testCase.maxPerMinute); got != testCase.want {
				t.Fatalf("refreshRateInterval(%d) = %s, want %s", testCase.maxPerMinute, got, testCase.want)
			}
		})
	}
}

func TestAuthAutoRefreshWorker_SharedRateGatePacesWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	refreshed := make(chan string, 2)
	manager.RegisterExecutor(pacedRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "paced-refresh"},
		refreshed:                     refreshed,
	})
	for _, id := range []string{"auth-a", "auth-b"} {
		if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: "paced-refresh"}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}

	loop := newAuthAutoRefreshLoop(manager, time.Second, 2)
	rateCh := make(chan time.Time)
	go loop.worker(ctx, rateCh)
	go loop.worker(ctx, rateCh)
	loop.jobs <- "auth-a"
	loop.jobs <- "auth-b"

	select {
	case id := <-refreshed:
		t.Fatalf("refresh %q ran before a rate gate token", id)
	case <-time.After(20 * time.Millisecond):
	}

	rateCh <- time.Now()
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not run after a rate gate token")
	}
	select {
	case id := <-refreshed:
		t.Fatalf("second refresh %q ran without another rate gate token", id)
	case <-time.After(20 * time.Millisecond):
	}

	rateCh <- time.Now()
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not run after the second rate gate token")
	}
}

func TestDeterministicRefreshJitter(t *testing.T) {
	maxJitter := 10 * time.Minute
	first := deterministicRefreshJitter("auth-a", maxJitter)
	if first < 0 || first > maxJitter {
		t.Fatalf("deterministicRefreshJitter() = %s, want within [0, %s]", first, maxJitter)
	}
	if got := deterministicRefreshJitter("auth-a", maxJitter); got != first {
		t.Fatalf("deterministicRefreshJitter() changed from %s to %s", first, got)
	}
	if got := deterministicRefreshJitter("auth-b", maxJitter); got == first {
		t.Fatalf("deterministicRefreshJitter() returned the same offset %s for distinct auth IDs", got)
	}
	if got := deterministicRefreshJitter("", maxJitter); got != 0 {
		t.Fatalf("deterministicRefreshJitter(empty) = %s, want 0", got)
	}
}

func TestNextRefreshCheckAtWithJitter_SpreadsScheduledRefreshEarlier(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	maxJitter := 5 * time.Minute
	setRefreshLeadFactory(t, "jittered-provider", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "jittered-auth",
		Provider: "jittered-provider",
		Metadata: map[string]any{
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	got, ok := nextRefreshCheckAtWithJitter(now, auth, 15*time.Minute, maxJitter)
	if !ok {
		t.Fatal("nextRefreshCheckAtWithJitter() ok = false, want true")
	}
	want := expiry.Add(-lead).Add(-deterministicRefreshJitter(auth.ID, maxJitter))
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAtWithJitter() = %s, want %s", got, want)
	}

	jitteredNow := want
	if !refreshDueWithJitter(jitteredNow, auth, 15*time.Minute, maxJitter) {
		t.Fatal("refreshDueWithJitter() = false at jittered schedule, want true")
	}
}

func TestNextRefreshCheckAtWithJitter_DoesNotAdvanceBackoff(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	nextAfter := now.Add(5 * time.Minute)
	auth := &Auth{
		ID:               "backoff-auth",
		Provider:         "test",
		NextRefreshAfter: nextAfter,
		Metadata:         map[string]any{"email": "x@example.com"},
	}

	got, ok := nextRefreshCheckAtWithJitter(now, auth, time.Minute, 10*time.Minute)
	if !ok {
		t.Fatal("nextRefreshCheckAtWithJitter() ok = false, want true")
	}
	if !got.Equal(nextAfter) {
		t.Fatalf("nextRefreshCheckAtWithJitter() = %s, want backoff %s", got, nextAfter)
	}
}

func TestNextRefreshCheckAtWithJitter_DisabledKeepsLegacySchedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "legacy-jitter-provider", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "legacy-auth",
		Provider: "legacy-jitter-provider",
		Metadata: map[string]any{"expires_at": expiry.Format(time.RFC3339)},
	}

	got, ok := nextRefreshCheckAtWithJitter(now, auth, 15*time.Minute, 0)
	if !ok {
		t.Fatal("nextRefreshCheckAtWithJitter() ok = false, want true")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAtWithJitter() = %s, want legacy schedule %s", got, want)
	}
	if refreshDueWithJitter(want.Add(-time.Second), auth, 15*time.Minute, 0) {
		t.Fatal("refreshDueWithJitter() = true with jitter disabled")
	}
}
