package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type quarantinePickSelector func(context.Context, string, string, cliproxyexecutor.Options, []*Auth) (*Auth, error)

func (pick quarantinePickSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return pick(ctx, provider, model, opts, auths)
}

func quarantineOptions(sessionID string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{sessionID}},
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    ".gemini-flash",
		},
	}
}

func newQuarantineSelector() *SessionAffinitySelector {
	return NewSessionAffinitySelector(quarantinePickSelector(func(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
		return auths[0], nil
	}))
}

func quarantine429(selector *SessionAffinitySelector, opts cliproxyexecutor.Options, authID string, delay time.Duration) {
	selector.OnResult(Result{
		AuthID:     authID,
		Provider:   "gemini",
		Model:      "gemini-3.6-flash",
		Success:    false,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests},
		RetryAfter: &delay,
		Options:    opts,
	})
}

func TestSessionAffinityQuarantineRetryAfterIsSessionLocal(t *testing.T) {
	selector := newQuarantineSelector()
	defer selector.Stop()
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	opts := quarantineOptions("session-one")

	quarantine429(selector, opts, authA.ID, 53*time.Second)
	got, err := selector.Pick(context.Background(), "mixed", ".gemini-flash", opts, []*Auth{authA, authB})
	if err != nil || got.ID != authB.ID {
		t.Fatalf("same-session Pick = %v/%v, want auth-b", got, err)
	}

	other, err := selector.Pick(context.Background(), "mixed", ".gemini-flash", quarantineOptions("session-two"), []*Auth{authA, authB})
	if err != nil || other.ID != authA.ID {
		t.Fatalf("other-session Pick = %v/%v, want auth-a", other, err)
	}
}

func TestSessionAffinityQuarantineTracksMultipleFailures(t *testing.T) {
	selector := newQuarantineSelector()
	defer selector.Stop()
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	authC := &Auth{ID: "auth-c", Provider: "gemini"}
	opts := quarantineOptions("session-multiple")
	delay := 53 * time.Second

	quarantine429(selector, opts, authA.ID, delay)
	quarantine429(selector, opts, authB.ID, delay)
	got, err := selector.Pick(context.Background(), "mixed", ".gemini-flash", opts, []*Auth{authA, authB, authC})
	if err != nil || got.ID != authC.ID {
		t.Fatalf("Pick = %v/%v, want auth-c", got, err)
	}
}

func TestSessionAffinityQuarantineExpires(t *testing.T) {
	selector := newQuarantineSelector()
	defer selector.Stop()
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	opts := quarantineOptions("session-expiry")

	quarantine429(selector, opts, authA.ID, 20*time.Millisecond)
	before, _ := selector.Pick(context.Background(), "mixed", ".gemini-flash", opts, []*Auth{authA, authB})
	if before.ID != authB.ID {
		t.Fatalf("Pick before expiry = %q, want auth-b", before.ID)
	}
	selector.OnResult(Result{AuthID: authB.ID, Provider: "gemini", Model: ".gemini-flash", Success: false, Error: &Error{HTTPStatus: http.StatusBadGateway}, Options: opts})
	time.Sleep(30 * time.Millisecond)
	after, _ := selector.Pick(context.Background(), "mixed", ".gemini-flash", opts, []*Auth{authA, authB})
	if after.ID != authA.ID {
		t.Fatalf("Pick after expiry = %q, want auth-a", after.ID)
	}
}

func TestSessionAffinityQuarantineSurvivesStaleSuccess(t *testing.T) {
	selector := newQuarantineSelector()
	defer selector.Stop()
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	opts := quarantineOptions("session-stale-success")

	quarantine429(selector, opts, authA.ID, 53*time.Second)
	selector.OnResult(Result{AuthID: authA.ID, Provider: "gemini", Model: "gemini-3.6-flash", Success: true, Options: opts})
	got, err := selector.Pick(context.Background(), "mixed", ".gemini-flash", opts, []*Auth{authA, authB})
	if err != nil || got.ID != authB.ID {
		t.Fatalf("Pick after stale success = %v/%v, want auth-b", got, err)
	}
}

func TestSessionAffinityQuarantineSkipsRequestScopedErrors(t *testing.T) {
	selector := newQuarantineSelector()
	defer selector.Stop()
	authA := &Auth{ID: "auth-a", Provider: "gemini"}
	authB := &Auth{ID: "auth-b", Provider: "gemini"}
	opts := quarantineOptions("session-client-error")

	selector.OnResult(Result{
		AuthID:   authA.ID,
		Provider: "gemini",
		Model:    "gemini-3.6-flash",
		Success:  false,
		Error:    &Error{Code: requestScopedErrorCode, HTTPStatus: http.StatusBadRequest},
		Options:  opts,
	})
	got, err := selector.Pick(context.Background(), "mixed", ".gemini-flash", opts, []*Auth{authA, authB})
	if err != nil || got.ID != authA.ID {
		t.Fatalf("Pick after request-scoped error = %v/%v, want auth-a", got, err)
	}
}
