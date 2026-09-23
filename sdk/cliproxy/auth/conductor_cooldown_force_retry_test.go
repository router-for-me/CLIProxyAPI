package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// ErrorCodeForceCooldown must honour Result.RetryAfter for every HTTP status.
// These tests call Manager.MarkResult; they do not reimplement the deadline.

func TestMarkResult_ForceCooldownHonoursRetryAfterForEveryStatus(t *testing.T) {
	withForceCooldownDefaults(t)
	hint := 90 * time.Second
	statuses := []int{
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
		http.StatusBadRequest,
		http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
	}
	for _, status := range statuses {
		status := status
		t.Run(http.StatusText(status)+"_model", func(t *testing.T) {
			before, updated := markForceCooldown(t, "model-"+http.StatusText(status), "claude-3", status, &hint, false)
			state := existingModelState(updated, "claude-3")
			if state == nil {
				t.Fatal("model state missing")
			}
			assertDeadlineNear(t, state.NextRetryAfter, before, hint, 15*time.Second)
		})
		t.Run(http.StatusText(status)+"_auth", func(t *testing.T) {
			before, updated := markForceCooldown(t, "auth-"+http.StatusText(status), "", status, &hint, false)
			assertDeadlineNear(t, updated.NextRetryAfter, before, hint, 15*time.Second)
		})
	}
}

func TestMarkResult_ForceCooldownRetryAfterSkipsQuotaFloor(t *testing.T) {
	withForceCooldownDefaults(t)
	hint := 3 * time.Second
	before, updated := markForceCooldown(t, "floor-model", "claude-3", http.StatusTooManyRequests, &hint, false)
	_, _, until := isAuthBlockedForModel(updated, "claude-3", before)
	assertDeadlineNear(t, until, before, hint, 4*time.Second)

	beforeAuth, updatedAuth := markForceCooldown(t, "floor-auth", "", http.StatusTooManyRequests, &hint, false)
	assertDeadlineNear(t, updatedAuth.NextRetryAfter, beforeAuth, hint, 4*time.Second)
	if updatedAuth.Quota.NextRecoverAt.IsZero() {
		t.Fatal("auth quota recovery missing")
	}
	assertDeadlineNear(t, updatedAuth.Quota.NextRecoverAt, beforeAuth, hint, 4*time.Second)

	beforeScope, updatedScope := markForceCooldown(t, "floor-scope", "claude-3", http.StatusTooManyRequests, &hint, true)
	_, _, untilScope := isAuthBlockedForModel(updatedScope, "claude-3", beforeScope)
	assertDeadlineNear(t, untilScope, beforeScope, hint, 4*time.Second)
}

func TestMarkResult_NonForce429KeepsQuotaFloor(t *testing.T) {
	withForceCooldownDefaults(t)
	hint := 3 * time.Second
	m := registerForceCooldownAuth(t, "organic-429")
	before := time.Now()
	m.MarkResult(context.Background(), Result{
		AuthID: "organic-429", Provider: "claude", Model: "claude-3",
		Success: false, RetryAfter: &hint,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})
	updated, ok := m.GetByID("organic-429")
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	_, _, until := isAuthBlockedForModel(updated, "claude-3", before)
	if until.Before(before.Add(9 * time.Second)) {
		t.Fatalf("organic 429 block %v, want >= quota floor", until.Sub(before))
	}
}

func TestMarkResult_ForceCooldownHintSurvivesDisabledTransientCooldown(t *testing.T) {
	withForceCooldownDefaults(t)
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	hint := 90 * time.Second
	before, updated := markForceCooldown(t, "transient-off-model", "claude-3", http.StatusServiceUnavailable, &hint, false)
	state := existingModelState(updated, "claude-3")
	if state == nil {
		t.Fatal("model state missing")
	}
	assertDeadlineNear(t, state.NextRetryAfter, before, hint, 15*time.Second)

	beforeAuth, updatedAuth := markForceCooldown(t, "transient-off-auth", "", http.StatusServiceUnavailable, &hint, false)
	assertDeadlineNear(t, updatedAuth.NextRetryAfter, beforeAuth, hint, 15*time.Second)
}

func TestMarkResult_ForceCooldownZeroDeadlineFallbackWithoutHint(t *testing.T) {
	withForceCooldownDefaults(t)
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	before, updated := markForceCooldown(t, "fallback-model", "claude-3", http.StatusServiceUnavailable, nil, false)
	state := existingModelState(updated, "claude-3")
	if state == nil {
		t.Fatal("model state missing")
	}
	assertDeadlineNear(t, state.NextRetryAfter, before, transientErrorCooldown, 15*time.Second)

	beforeAuth, updatedAuth := markForceCooldown(t, "fallback-auth", "", http.StatusServiceUnavailable, nil, false)
	assertDeadlineNear(t, updatedAuth.NextRetryAfter, beforeAuth, transientErrorCooldown, 15*time.Second)
}

func TestMarkResult_NonForceUnauthorizedIgnoresRetryAfter(t *testing.T) {
	withForceCooldownDefaults(t)
	hint := 90 * time.Second
	m := registerForceCooldownAuth(t, "organic-401")
	before := time.Now()
	m.MarkResult(context.Background(), Result{
		AuthID: "organic-401", Provider: "claude", Model: "claude-3",
		Success: false, RetryAfter: &hint,
		Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})
	updated, ok := m.GetByID("organic-401")
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	state := existingModelState(updated, "claude-3")
	if state == nil {
		t.Fatal("model state missing")
	}
	assertDeadlineNear(t, state.NextRetryAfter, before, 30*time.Minute, time.Minute)
}

func TestMarkResult_ForceCooldownDoesNotShortenLiveDeadline(t *testing.T) {
	withForceCooldownDefaults(t)
	m := registerForceCooldownAuth(t, "live-model")
	m.MarkResult(context.Background(), Result{
		AuthID: "live-model", Provider: "claude", Model: "claude-3",
		Success: false,
		Error:   &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})
	seeded, _ := m.GetByID("live-model")
	seed := existingModelState(seeded, "claude-3")
	if seed == nil || seed.NextRetryAfter.IsZero() {
		t.Fatal("seed deadline missing")
	}
	hint := 90 * time.Second
	m.MarkResult(context.Background(), Result{
		AuthID: "live-model", Provider: "claude", Model: "claude-3",
		Success: false, RetryAfter: &hint,
		Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusUnauthorized, Message: "sentence"},
	})
	updated, _ := m.GetByID("live-model")
	state := existingModelState(updated, "claude-3")
	if state == nil || state.NextRetryAfter.Before(seed.NextRetryAfter) {
		t.Fatalf("force hint shortened live deadline to %v, seed %v", state.NextRetryAfter, seed.NextRetryAfter)
	}

	authMgr := registerForceCooldownAuth(t, "live-auth")
	authMgr.MarkResult(context.Background(), Result{
		AuthID: "live-auth", Provider: "claude",
		Success: false,
		Error:   &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})
	seededAuth, _ := authMgr.GetByID("live-auth")
	if seededAuth.NextRetryAfter.IsZero() {
		t.Fatal("auth seed deadline missing")
	}
	authMgr.MarkResult(context.Background(), Result{
		AuthID: "live-auth", Provider: "claude",
		Success: false, RetryAfter: &hint,
		Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusUnauthorized, Message: "sentence"},
	})
	updatedAuth, _ := authMgr.GetByID("live-auth")
	if updatedAuth.NextRetryAfter.Before(seededAuth.NextRetryAfter) {
		t.Fatalf("auth force hint shortened live deadline to %v, seed %v", updatedAuth.NextRetryAfter, seededAuth.NextRetryAfter)
	}
}

func withForceCooldownDefaults(t *testing.T) {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
}

func registerForceCooldownAuth(t *testing.T, id string) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	if _, errRegister := m.Register(context.Background(), &Auth{
		ID: id, Provider: "claude", Status: StatusActive,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return m
}

func markForceCooldown(t *testing.T, id, model string, status int, hint *time.Duration, credentialScope bool) (time.Time, *Auth) {
	t.Helper()
	m := registerForceCooldownAuth(t, id)
	before := time.Now()
	m.MarkResult(context.Background(), Result{
		AuthID: id, Provider: "claude", Model: model,
		Success: false, RetryAfter: hint, CredentialScope: credentialScope,
		Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: status, Message: "sentence"},
	})
	updated, ok := m.GetByID(id)
	if !ok || updated == nil {
		t.Fatalf("auth %s missing", id)
	}
	return before, updated
}

func assertDeadlineNear(t *testing.T, got, before time.Time, want, slack time.Duration) {
	t.Helper()
	if got.IsZero() {
		t.Fatal("deadline is zero")
	}
	delta := got.Sub(before)
	if delta < want-time.Second || delta > want+slack {
		t.Fatalf("deadline delta %v, want %v (slack %v)", delta, want, slack)
	}
}
