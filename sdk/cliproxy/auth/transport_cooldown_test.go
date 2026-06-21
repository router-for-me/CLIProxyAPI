package auth

import (
	"context"
	"testing"
	"time"
)

// TestMarkResult_TransportErrorCooldownsModel verifies that a transport-level
// failure (TLS error, connection reset) carrying no HTTP status code still
// triggers a short cooldown at the model-state level. Previously the default
// cooldown branch left NextRetryAfter zero, so the broken credential was
// immediately re-selected on every retry.
func TestMarkResult_TransportErrorCooldownsModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-transport-model", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "openai-compatibility",
		Model:    "gpt-5",
		Success:  false,
		Error: &Error{
			Message:   `Post "https://example.com/v1/chat/completions": remote error: tls: bad record MAC`,
			Retryable: true,
			// HTTPStatus intentionally zero: transport errors carry no HTTP status.
		},
	})

	got := snapshotAuthByID(manager, auth.ID)
	if got == nil {
		t.Fatalf("auth %s not found in snapshot", auth.ID)
	}
	blocked, _, next := isAuthBlockedForModel(got, "gpt-5", time.Now())
	if !blocked {
		t.Fatalf("transport error did not cool down model: isAuthBlockedForModel returned blocked=false, want true (credential should be temporarily unavailable)")
	}
	if !next.After(time.Now()) {
		t.Fatalf("NextRetryAfter %v not in the future", next)
	}
}

// TestMarkResult_TransportErrorCooldownsAuth exercises the auth-level
// applyAuthFailureState path (Model == "").
func TestMarkResult_TransportErrorCooldownsAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-transport-auth", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "openai-compatibility",
		Success:  false,
		Error: &Error{
			Message:   `dial tcp: connection reset by peer`,
			Retryable: true,
		},
	})

	got := snapshotAuthByID(manager, auth.ID)
	if got == nil {
		t.Fatalf("auth %s not found in snapshot", auth.ID)
	}
	if got.NextRetryAfter.IsZero() {
		t.Fatalf("transport error did not cool down auth: NextRetryAfter is zero, want non-zero")
	}
	if got.StatusMessage != "transport error" {
		t.Fatalf("StatusMessage = %q, want %q", got.StatusMessage, "transport error")
	}
}

// TestMarkResult_TransportErrorNoCooldownWhenDisabled ensures the new
// cooldown is skipped when cooling is globally disabled.
func TestMarkResult_TransportErrorNoCooldownWhenDisabled(t *testing.T) {
	SetQuotaCooldownDisabled(true)
	t.Cleanup(func() { SetQuotaCooldownDisabled(false) })

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-transport-disabled", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "openai-compatibility",
		Model:    "gpt-5",
		Success:  false,
		Error:    &Error{Message: `tls: bad record MAC`, Retryable: true},
	})

	got := snapshotAuthByID(manager, auth.ID)
	if got == nil {
		t.Fatalf("auth %s not found in snapshot", auth.ID)
	}
	blocked, _, _ := isAuthBlockedForModel(got, "gpt-5", time.Now())
	if blocked {
		t.Fatalf("credential was cooled down despite cooling being globally disabled")
	}
}

// snapshotAuthByID returns a clone of the auth with the given ID from the
// manager's current snapshot.
func snapshotAuthByID(m *Manager, id string) *Auth {
	for _, a := range m.snapshotAuths() {
		if a.ID == id {
			return a
		}
	}
	return nil
}
