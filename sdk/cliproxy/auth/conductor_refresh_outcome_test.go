package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestUnknownRefreshOutcomeRequiresRecoveryWithoutDiscardingAccess(t *testing.T) {
	for _, cause := range []error{nil, context.Canceled} {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		executor := &unauthorizedRefreshExecutor{id: "codex", refreshErr: &codex.RefreshOutcomeUnknownError{Stage: "transport", Cause: cause}}
		manager.RegisterExecutor(executor)
		expiry := time.Now().Add(time.Hour).Truncate(time.Second)
		auth := &Auth{ID: "unknown-outcome", Provider: "codex", Status: StatusActive, Metadata: map[string]any{
			"access_token": "valid-access", "refresh_token": "unresolved-grant", "expired": expiry.Format(time.RFC3339),
		}}
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		manager.refreshAuth(ctx, auth.ID)
		updated, _ := manager.GetByID(auth.ID)
		if updated == nil || !updated.RequiresSignIn || updated.RefreshError == nil || updated.RefreshError.Code != "refresh_outcome_unknown" {
			t.Fatalf("unknown outcome not retained as recovery-required: %+v", updated)
		}
		if updated.Unavailable || updated.Status != StatusActive {
			t.Fatal("valid access token must remain usable")
		}
		manager.refreshAuth(ctx, auth.ID)
		if executor.RefreshCalls() != 1 {
			t.Fatalf("scheduled refresh replayed: calls=%d", executor.RefreshCalls())
		}
	}
}
