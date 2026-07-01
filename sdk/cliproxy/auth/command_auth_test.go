package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestCommandAuthUnauthorizedInvalidatesTokenWithoutCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "command-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"source":        "config:codex[abc]",
			AttrAuthKind:    AttrAuthKindAPIKey,
			AttrAuthSource:  AttrAuthSourceCommand,
			AttrAuthCommand: "fetch-token",
		},
		Metadata: map[string]any{"access_token": "bad-token"},
		Status:   StatusActive,
	}
	auth.NextRefreshAfter = time.Now().Add(time.Hour)
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:  auth.ID,
		Model:   "gpt-5-codex",
		Success: false,
		Error:   &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})

	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth in manager")
	}
	if _, exists := current.Metadata["access_token"]; exists {
		t.Fatal("expected access_token to be cleared")
	}
	if !current.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %v, want zero", current.NextRefreshAfter)
	}
	if !current.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero", current.NextRetryAfter)
	}
	state := current.ModelStates["gpt-5-codex"]
	if state == nil {
		t.Fatal("expected model state")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("model NextRetryAfter = %v, want zero", state.NextRetryAfter)
	}
}
