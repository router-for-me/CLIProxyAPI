package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func withMaxRetryAfterSeconds(t *testing.T, seconds int) {
	t.Helper()
	prev := maxRetryAfterSeconds.Load()
	SetMaxRetryAfterSeconds(seconds)
	t.Cleanup(func() { maxRetryAfterSeconds.Store(prev) })
}

func markLongQuota429(t *testing.T, authID, model string, retryAfter time.Duration, errCode string) *ModelState {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: authID, Provider: "antigravity"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "antigravity",
		Model:      model,
		Success:    false,
		RetryAfter: &retryAfter,
		Error: &Error{
			Code:       errCode,
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "QUOTA_EXHAUSTED: Individual quota reached. Resets in 132h37m4s.",
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}
	return state
}

func TestMarkResult_429LongRetryAfter_CappedByMaxRetryAfterSeconds(t *testing.T) {
	withQuotaCooldownEnabled(t)
	withMaxRetryAfterSeconds(t, 3600)

	now := time.Now()
	state := markLongQuota429(t, "auth-long-reset-capped", "gemini-long-reset", 132*time.Hour, "")

	expected := now.Add(time.Hour)
	for name, got := range map[string]time.Time{"NextRetryAfter": state.NextRetryAfter, "Quota.NextRecoverAt": state.Quota.NextRecoverAt} {
		if got.Before(expected.Add(-5*time.Second)) || got.After(expected.Add(5*time.Second)) {
			t.Fatalf("expected %s ~ %v (capped to 1h), got %v", name, expected, got)
		}
	}
	if !state.Quota.Exceeded {
		t.Fatal("expected quota to remain marked exceeded during the capped cooldown")
	}
}

func TestMarkResult_429LongRetryAfter_UncappedByDefault(t *testing.T) {
	withQuotaCooldownEnabled(t)
	withMaxRetryAfterSeconds(t, 0)

	now := time.Now()
	state := markLongQuota429(t, "auth-long-reset-default", "gemini-long-reset", 132*time.Hour, "")

	expected := now.Add(132 * time.Hour)
	if state.NextRetryAfter.Before(expected.Add(-5 * time.Second)) {
		t.Fatalf("expected legacy full cooldown ~ %v, got %v", expected, state.NextRetryAfter)
	}
}

func TestMarkResult_429ShortRetryAfter_NotExtendedByCap(t *testing.T) {
	withQuotaCooldownEnabled(t)
	withMaxRetryAfterSeconds(t, 3600)

	now := time.Now()
	state := markLongQuota429(t, "auth-short-reset", "gemini-short-reset", 5*time.Minute, "")

	expected := now.Add(5 * time.Minute)
	if state.NextRetryAfter.Before(expected.Add(-5*time.Second)) || state.NextRetryAfter.After(expected.Add(5*time.Second)) {
		t.Fatalf("expected NextRetryAfter ~ %v, got %v", expected, state.NextRetryAfter)
	}
}

func TestCapResultRetryAfter_LeavesForceCooldownUntouched(t *testing.T) {
	withMaxRetryAfterSeconds(t, 60)

	long := 2 * time.Hour
	result := capResultRetryAfter(Result{RetryAfter: &long, Error: &Error{Code: ErrorCodeForceCooldown}})
	if result.RetryAfter == nil || *result.RetryAfter != long {
		t.Fatalf("expected force cooldown retry hint to stay %v, got %v", long, result.RetryAfter)
	}

	result = capResultRetryAfter(Result{RetryAfter: &long, Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
	if result.RetryAfter == nil || *result.RetryAfter != time.Minute {
		t.Fatalf("expected capped retry hint of 1m, got %v", result.RetryAfter)
	}
	if long != 2*time.Hour {
		t.Fatal("capResultRetryAfter must not mutate the caller's duration")
	}
}
