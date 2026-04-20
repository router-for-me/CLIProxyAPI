package auth

import (
	"context"
	"testing"
	"time"
)

func TestManager_MarkResult_524SetsModelCooldown(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-524", Provider: "codex"}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5.4",
		Success:  false,
		Error:    &Error{HTTPStatus: 524, Message: "gateway timeout"},
	})
	after := time.Now()

	manager.mu.RLock()
	updated := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if updated == nil {
		t.Fatal("expected registered auth to remain present")
	}
	state := updated.ModelStates["gpt-5.4"]
	if state == nil {
		t.Fatal("expected model state for gpt-5.4")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("expected 524 to set model cooldown")
	}
	minExpected := before.Add(55 * time.Second)
	maxExpected := after.Add(65 * time.Second)
	if state.NextRetryAfter.Before(minExpected) || state.NextRetryAfter.After(maxExpected) {
		t.Fatalf("model cooldown = %v, want within [%v, %v]", state.NextRetryAfter, minExpected, maxExpected)
	}
}

func TestApplyAuthFailureState_524SetsAuthCooldown(t *testing.T) {
	t.Parallel()

	auth := &Auth{ID: "auth-524", Provider: "codex"}
	before := time.Now()
	applyAuthFailureState(auth, &Error{HTTPStatus: 524, Message: "gateway timeout"}, nil, before)

	if auth.StatusMessage != "transient upstream error" {
		t.Fatalf("status message = %q, want %q", auth.StatusMessage, "transient upstream error")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatal("expected 524 to set auth cooldown")
	}
	minExpected := before.Add(55 * time.Second)
	maxExpected := before.Add(65 * time.Second)
	if auth.NextRetryAfter.Before(minExpected) || auth.NextRetryAfter.After(maxExpected) {
		t.Fatalf("auth cooldown = %v, want within [%v, %v]", auth.NextRetryAfter, minExpected, maxExpected)
	}
}
