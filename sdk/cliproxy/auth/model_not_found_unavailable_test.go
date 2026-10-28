package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func newDisableCoolingManager(t *testing.T) (*Manager, *Auth) {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-model-not-found",
		Provider: "codex",
		Metadata: map[string]any{"disable_cooling": true},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return m, auth
}

func markModelNotFound(t *testing.T, m *Manager, authID, provider, model string) {
	t.Helper()
	m.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{Code: "model_not_found", Message: "The model " + model + " does not exist.", HTTPStatus: http.StatusNotFound},
	})
}

func TestModelNotFoundDisablesModelWhenCoolingDisabled(t *testing.T) {
	m, auth := newDisableCoolingManager(t)
	markModelNotFound(t, m, auth.ID, auth.Provider, "gpt-5.5")

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	state := updated.ModelStates["gpt-5.5"]
	if state == nil || state.Status != StatusDisabled {
		t.Fatalf("expected the model to be disabled, got %#v", state)
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected no deadline, got %v", state.NextRetryAfter)
	}

	blocked, reason, _ := isAuthBlockedForModel(updated, "gpt-5.5", time.Now())
	if !blocked || reason != blockReasonDisabled {
		t.Fatalf("expected a permanent block, got blocked=%v reason=%v", blocked, reason)
	}
	// Other models on the same credential stay usable.
	if blocked, _, _ := isAuthBlockedForModel(updated, "gpt-5.5-mini", time.Now()); blocked {
		t.Fatal("disabling one model must not block the credential")
	}
}

func TestModelNotFoundGenericSupportErrorStaysTransient(t *testing.T) {
	m, auth := newDisableCoolingManager(t)
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5.5",
		Success:  false,
		Error:    &Error{Code: "invalid_request_error", Message: "requested model is unavailable", HTTPStatus: http.StatusBadRequest},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	if state := updated.ModelStates["gpt-5.5"]; state != nil && state.Status == StatusDisabled {
		t.Fatalf("a generic model-support error must not disable the model: %#v", state)
	}
}

func TestModelNotFoundBlockClearedOnCredentialUpdate(t *testing.T) {
	m, auth := newDisableCoolingManager(t)
	markModelNotFound(t, m, auth.ID, auth.Provider, "gpt-5.5")

	live, ok := m.GetByID(auth.ID)
	if !ok || live == nil {
		t.Fatal("auth missing")
	}
	if state := live.ModelStates["gpt-5.5"]; state == nil || state.Status != StatusDisabled {
		t.Fatalf("expected disabled model before the update, got %#v", state)
	}

	// A config/credential update may have added the model back.
	if _, errUpdate := m.Update(context.Background(), live.Clone()); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	after, ok := m.GetByID(auth.ID)
	if !ok || after == nil {
		t.Fatal("auth missing after update")
	}
	if state := after.ModelStates["gpt-5.5"]; state != nil && state.Status == StatusDisabled {
		t.Fatalf("credential update did not clear the block: %#v", state)
	}
	if blocked, _, _ := isAuthBlockedForModel(after, "gpt-5.5", time.Now()); blocked {
		t.Fatal("model still blocked after the credential update")
	}
	if after.Status == StatusError {
		t.Fatalf("parent auth still reports an error: %#v", after.StatusMessage)
	}
	if after.LastError != nil && isExplicitModelNotFoundError(after.LastError, "") {
		t.Fatalf("parent auth kept the missing-model error: %#v", after.LastError)
	}
}

func TestExplicitlyDisabledModelSurvivesCredentialUpdate(t *testing.T) {
	m, auth := newDisableCoolingManager(t)

	live, ok := m.GetByID(auth.ID)
	if !ok || live == nil {
		t.Fatal("auth missing")
	}
	live.ModelStates = map[string]*ModelState{
		"user-disabled": {
			Status:         StatusDisabled,
			StatusMessage:  "disabled by operator",
			UpdatedAt:      time.Now(),
			NextRetryAfter: time.Time{},
		},
	}
	if _, errUpdate := m.Update(context.Background(), live.Clone()); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	after, ok := m.GetByID(auth.ID)
	if !ok || after == nil {
		t.Fatal("auth missing after update")
	}
	state := after.ModelStates["user-disabled"]
	if state == nil || state.Status != StatusDisabled {
		t.Fatalf("credential update re-enabled an explicitly disabled model: %#v", state)
	}
}

func TestModelNotFoundWithoutCodeDisablesModelWhenCoolingDisabled(t *testing.T) {
	m, auth := newDisableCoolingManager(t)
	model := "claude-missing"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			Message:    `{"error":{"type":"not_found_error","message":"model: claude-missing"}}`,
			HTTPStatus: http.StatusNotFound,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	state := updated.ModelStates[model]
	if state == nil || state.Status != StatusDisabled {
		t.Fatalf("expected the model to be disabled, got %#v", state)
	}
}
