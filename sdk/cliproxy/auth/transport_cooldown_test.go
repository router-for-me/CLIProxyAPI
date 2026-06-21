package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestErrorToResultError_MapsTransportErrorToBadGateway verifies that a
// transport-level failure returned by http.Client.Do (a *url.Error) is mapped
// to HTTP 502 so the cooldown switch treats it as a transient upstream error.
// A bare local validation error (no status, not a *url.Error) stays at 0.
func TestErrorToResultError_MapsTransportErrorToBadGateway(t *testing.T) {
	t.Run("transport error mapped to 502", func(t *testing.T) {
		err := &url.Error{Op: "Post", URL: "https://example.com/v1/chat/completions", Err: errors.New("remote error: tls: bad record MAC")}
		got := errorToResultError(err)
		if got.HTTPStatus != http.StatusBadGateway {
			t.Fatalf("HTTPStatus = %d, want %d", got.HTTPStatus, http.StatusBadGateway)
		}
		if !got.Retryable {
			t.Fatalf("Retryable = false, want true")
		}
		if got.Message == "" {
			t.Fatalf("Message is empty")
		}
	})
	t.Run("local validation error stays status 0", func(t *testing.T) {
		got := errorToResultError(errors.New("multipart boundary is missing"))
		if got.HTTPStatus != 0 {
			t.Fatalf("HTTPStatus = %d, want 0 (local validation errors carry no HTTP status)", got.HTTPStatus)
		}
		if got.Retryable {
			t.Fatalf("Retryable = true, want false")
		}
	})
}

// TestMarkResult_TransportErrorCooldownsModel verifies that a transport-level
// failure mapped to 502 triggers a short cooldown at the model-state level.
// Previously such errors carried no HTTP status and fell into the default
// branch, leaving NextRetryAfter zero so the broken credential was
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
		// errorToResultError maps *url.Error to 502; simulate that here.
		Error: &Error{
			Message:    `Post "https://example.com/v1/chat/completions": remote error: tls: bad record MAC`,
			Retryable:  true,
			HTTPStatus: http.StatusBadGateway,
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
			Message:    `dial tcp: connection reset by peer`,
			HTTPStatus: http.StatusBadGateway,
		},
	})

	got := snapshotAuthByID(manager, auth.ID)
	if got == nil {
		t.Fatalf("auth %s not found in snapshot", auth.ID)
	}
	if got.NextRetryAfter.IsZero() {
		t.Fatalf("transport error did not cool down auth: NextRetryAfter is zero, want non-zero")
	}
}

// TestMarkResult_TransportErrorNoCooldownWhenDisabled ensures the cooldown
// is skipped when cooling is globally disabled.
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
		Error: &Error{
			Message:    `tls: bad record MAC`,
			HTTPStatus: http.StatusBadGateway,
		},
	})

	got := snapshotAuthByID(manager, auth.ID)
	if got == nil {
		t.Fatalf("auth %s not found in snapshot", auth.ID)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "gpt-5", time.Now()); blocked {
		t.Fatalf("credential was cooled down despite cooling being globally disabled")
	}
}

// TestMarkResult_LocalValidationErrorDoesNotCooldown verifies that a local
// request-validation error (no HTTP status, not a transport error) does not
// cool down a credential, since it reflects a malformed client request rather
// than an upstream outage.
func TestMarkResult_LocalValidationErrorDoesNotCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-local-validation", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "openai-compatibility",
		Model:    "gpt-5",
		Success:  false,
		Error:    &Error{Message: "multipart boundary is missing"}, // HTTPStatus 0, not a transport error
	})

	got := snapshotAuthByID(manager, auth.ID)
	if got == nil {
		t.Fatalf("auth %s not found in snapshot", auth.ID)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "gpt-5", time.Now()); blocked {
		t.Fatalf("local validation error incorrectly cooled down the credential")
	}
}

// TestWrapStreamResult_ClientCancellationDoesNotCooldown verifies that a
// stream chunk error caused by the client cancelling the request
// (context.Canceled) is filtered out before reaching MarkResult, so a healthy
// credential is not put into cooldown for an aborted SSE stream.
func TestWrapStreamResult_ClientCancellationDoesNotCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-cancel-stream", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate a client that disconnected mid-stream

	buffered := []cliproxyexecutor.StreamChunk{{Err: context.Canceled}}
	sr := manager.wrapStreamResult(ctx, auth, "openai-compatibility", "gpt-5", nil, buffered, nil)
	for range sr.Chunks {
	}

	got := snapshotAuthByID(manager, auth.ID)
	if got == nil {
		t.Fatalf("auth %s not found in snapshot", auth.ID)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "gpt-5", time.Now()); blocked {
		t.Fatalf("client cancellation of a stream incorrectly cooled down a healthy credential")
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
