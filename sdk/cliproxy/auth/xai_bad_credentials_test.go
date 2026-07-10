package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestIsXaiBadCredentialsError(t *testing.T) {
	badCreds := &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"}
	notValidated := &Error{HTTPStatus: http.StatusForbidden, Message: "The OAuth2 access token could not be validated."}

	if !isXaiBadCredentialsError("xai", badCreds) {
		t.Fatalf("expected true for xai 403 bad-credentials")
	}
	if !isXaiBadCredentialsError("XAI", notValidated) {
		t.Fatalf("expected true for xai 403 could-not-be-validated (case-insensitive provider)")
	}
	if !isXaiBadCredentialsError(" xai ", badCreds) {
		t.Fatalf("expected true for whitespace-padded xai provider")
	}
	if isXaiBadCredentialsError("claude", badCreds) {
		t.Fatalf("expected false for non-xai provider")
	}
	if isXaiBadCredentialsError("xai", &Error{HTTPStatus: http.StatusPaymentRequired, Message: "unauthenticated:bad-credentials"}) {
		t.Fatalf("expected false for xai with non-403 status")
	}
	if isXaiBadCredentialsError("xai", &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"}) {
		t.Fatalf("expected false for xai 403 with unrelated message")
	}
	if isXaiBadCredentialsError("xai", nil) {
		t.Fatalf("expected false for nil error")
	}
}

func TestIsXaiBadCredentialsResultError(t *testing.T) {
	if !isXaiBadCredentialsResultError("xai", &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"}) {
		t.Fatalf("expected true for xai 403 bad-credentials")
	}
	if !isXaiBadCredentialsResultError("xai", &Error{HTTPStatus: http.StatusForbidden, Code: "unauthenticated:bad-credentials"}) {
		t.Fatalf("expected true when the code carries the marker")
	}
	if !isXaiBadCredentialsResultError(" xai ", &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"}) {
		t.Fatalf("expected true for whitespace-padded xai provider")
	}
	if !isXaiBadCredentialsResultError("xai", &Error{HTTPStatus: http.StatusForbidden, Message: "The OAuth2 access token could not be validated."}) {
		t.Fatalf("expected true for xai 403 could-not-be-validated")
	}
	if isXaiBadCredentialsResultError("claude", &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"}) {
		t.Fatalf("expected false for non-xai provider")
	}
	if isXaiBadCredentialsResultError("xai", &Error{HTTPStatus: http.StatusPaymentRequired, Message: "unauthenticated:bad-credentials"}) {
		t.Fatalf("expected false for xai with non-403 status")
	}
	if isXaiBadCredentialsResultError("xai", &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"}) {
		t.Fatalf("expected false for xai 403 with unrelated message")
	}
	if isXaiBadCredentialsResultError("xai", nil) {
		t.Fatalf("expected false for nil error")
	}
}

func TestManager_MarkResult_XaiBadCredentials_On403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-xai-403",
		Provider: "xai",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-xai-403"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "xai",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.Unavailable {
		t.Fatalf("expected model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be set for xai bad-credentials")
	}
	diff := time.Until(state.NextRetryAfter)
	if diff < 29*time.Minute || diff > 31*time.Minute {
		t.Fatalf("expected ~30 minute cooldown for xai bad-credentials, got %v", diff)
	}

	// The unauthorized classification suspends the model in the registry, so no
	// clients remain available for it (unlike a transient cooldown).
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("expected model to be suspended (count 0), got %d", count)
	}
	if state.LastError == nil || state.LastError.Code != "unauthorized" {
		t.Fatalf("expected model LastError.Code = unauthorized, got %+v", state.LastError)
	}
}

func TestManager_MarkResult_XaiBadCredentials_AuthLevelStatusUnauthorized(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-xai-403-authlevel",
		Provider: "xai",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "xai",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "The OAuth2 access token could not be validated."},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.StatusMessage != "unauthorized" {
		t.Fatalf("expected StatusMessage 'unauthorized' (not payment_required), got %q", updated.StatusMessage)
	}
	if updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be set for xai bad-credentials")
	}
	diff := time.Until(updated.NextRetryAfter)
	if diff < 29*time.Minute || diff > 31*time.Minute {
		t.Fatalf("expected ~30 minute cooldown for xai bad-credentials, got %v", diff)
	}
	if updated.LastError == nil || updated.LastError.Code != "unauthorized" {
		t.Fatalf("expected auth LastError.Code = unauthorized, got %+v", updated.LastError)
	}
}
