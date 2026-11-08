package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Issue 7017: a Codex turn_rate_limit_exceeded 429 ("Codex is at capacity")
// is a transient capacity rejection, not quota exhaustion. MarkResult must
// cool only the reporting model until the upstream Retry-After, never label
// it quota, and never advance the quota exponential backoff ladder.

type issue7017TransientError struct{ msg string }

func (e issue7017TransientError) Error() string              { return e.msg }
func (e issue7017TransientError) StatusCode() int            { return http.StatusTooManyRequests }
func (e issue7017TransientError) IsTransientRateLimit() bool { return true }

func issue7017TurnBody() string {
	return `{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity, please try again later"}}`
}

func issue7017Register(t *testing.T, manager *Manager, auth *Auth) {
	t.Helper()
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
}

func TestIssue7017ResultErrorFromErrorMarksTransientRateLimit(t *testing.T) {
	resultErr := resultErrorFromError(issue7017TransientError{msg: issue7017TurnBody()})
	if resultErr == nil || resultErr.Code != ErrorCodeTransientRateLimit {
		t.Fatalf("result error code = %v, want %q", resultErr, ErrorCodeTransientRateLimit)
	}
	if resultErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("result error status = %d, want 429", resultErr.HTTPStatus)
	}
	if !isTransientRateLimitResultError(resultErr) {
		t.Fatalf("isTransientRateLimitResultError must be true for the marked result error")
	}
}

func TestIssue7017MarkResultTurnRateLimitCoolsModelWithoutQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)

	retryAfter := 256 * time.Second
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-turn-limit",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
		ModelStates: map[string]*ModelState{
			"gpt-6.1-sol": {Status: StatusActive},
			"gpt-6-astra": {Status: StatusActive},
		},
	}
	issue7017Register(t, manager, auth)

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-6.1-sol",
		Success:    false,
		RetryAfter: &retryAfter,
		Error: &Error{
			Code:       ErrorCodeTransientRateLimit,
			Message:    issue7017TurnBody(),
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatal("auth not found after MarkResult")
	}
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil {
		t.Fatal("expected model state for gpt-6.1-sol")
	}
	if !state.Unavailable {
		t.Fatal("reporting model must be unavailable during the capacity cooldown")
	}
	if state.NextRetryAfter.Before(before.Add(250*time.Second)) || state.NextRetryAfter.After(before.Add(280*time.Second)) {
		t.Fatalf("NextRetryAfter = %v, want ~256s after start (upstream Retry-After)", state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason == "quota" {
		t.Fatalf("turn rate limit must not be stored as quota, got %+v", state.Quota)
	}
	if state.Quota.BackoffLevel != 0 {
		t.Fatalf("quota backoff level must not advance, got %d", state.Quota.BackoffLevel)
	}
	if got.Quota.Exceeded || got.Quota.Reason == "quota" || got.Quota.Reason == "credential_quota" {
		t.Fatalf("credential must not be labelled quota, got %+v", got.Quota)
	}
	sibling := got.ModelStates["gpt-6-astra"]
	if sibling != nil && (sibling.Unavailable || sibling.Quota.Exceeded) {
		t.Fatalf("sibling model must stay available, got %+v", sibling)
	}

	// The cooldown projection must not present this as quota either.
	views := CooldownSnapshotForAuth(got, time.Now())
	if len(views) == 0 {
		t.Fatal("expected a cooldown view for the reporting model")
	}
	for _, view := range views {
		if view.Reason == "quota" || view.Reason == "credential_quota" {
			t.Fatalf("cooldown view must not report quota for a turn rate limit: %+v", view)
		}
		if view.BackoffLevel != nil {
			t.Fatalf("cooldown view must not expose a quota backoff level: %+v", view)
		}
	}
}

func TestIssue7017MarkResultTurnRateLimitNoHintUsesTransientCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-turn-limit-nohint", Provider: "codex", Metadata: map[string]any{"type": "codex"}}
	issue7017Register(t, manager, auth)

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-6.1-sol",
		Success:  false,
		Error: &Error{
			Code:       ErrorCodeTransientRateLimit,
			Message:    issue7017TurnBody(),
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	got, _ := manager.GetByID(auth.ID)
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil {
		t.Fatal("expected model state")
	}
	if state.Quota.Exceeded || state.Quota.Reason == "quota" || state.Quota.BackoffLevel != 0 {
		t.Fatalf("no-hint turn rate limit must not use quota backoff, got %+v", state.Quota)
	}
	if state.NextRetryAfter.Before(before.Add(30*time.Second)) || state.NextRetryAfter.After(before.Add(3*time.Minute)) {
		t.Fatalf("NextRetryAfter = %v, want bounded transient cooldown near one minute", state.NextRetryAfter)
	}
}

func TestIssue7017AuthLevelTurnRateLimitIsNotQuota(t *testing.T) {
	retryAfter := 120 * time.Second
	auth := &Auth{ID: "auth-turn-level"}
	before := time.Now()
	applyAuthFailureState(auth, &Error{
		Code:       ErrorCodeTransientRateLimit,
		Message:    issue7017TurnBody(),
		Retryable:  true,
		HTTPStatus: http.StatusTooManyRequests,
	}, &retryAfter, before, false)

	if auth.Quota.Exceeded || auth.Quota.Reason == "quota" {
		t.Fatalf("auth-level turn rate limit must not be quota, got %+v", auth.Quota)
	}
	if !auth.NextRetryAfter.Equal(before.Add(retryAfter)) {
		t.Fatalf("NextRetryAfter = %v, want upstream deadline %v", auth.NextRetryAfter, before.Add(retryAfter))
	}
}

func TestIssue7017CooldownViewTransient429IsNotQuota(t *testing.T) {
	now := time.Now()
	view := newCooldownView("model", "gpt-6.1-sol", now.Add(256*time.Second), now, QuotaState{}, "", &Error{
		Code:       ErrorCodeTransientRateLimit,
		Message:    issue7017TurnBody(),
		HTTPStatus: http.StatusTooManyRequests,
	})
	if view.Reason == "quota" || view.Reason == "credential_quota" || view.BackoffLevel != nil {
		t.Fatalf("transient 429 cooldown view must not be quota: %+v", view)
	}
}
