package auth

import (
	"context"
	"fmt"
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

func TestAutoRefreshDeferralDoesNotClearPendingBackoffToZero(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	deferredErr := fmt.Errorf("Codex renewal deferred until interactive macOS wake: %w", codex.ErrSIWCRefreshDeferred)
	executor := &unauthorizedRefreshExecutor{id: "codex", refreshErr: deferredErr}
	manager.RegisterExecutor(executor)

	now := time.Now()
	auth := &Auth{
		ID:       "wake-deferred-job",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "valid-access",
			"refresh_token": "ordinary-refresh",
			"expired":       now.Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	registered, errRegister := manager.Register(ctx, auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 1)
	job := manager.markRefreshPending(loop, registered.ID, registered.RegistrationEpoch, now)
	if job == nil {
		t.Fatal("expected pending refresh job")
	}
	if !manager.beginRefreshJob(ctx, job) {
		t.Fatal("expected refresh job to start")
	}
	_, errRefresh := manager.refreshAuthForRequestAtEpoch(ctx, job.id, "", job.registrationEpoch)
	if errRefresh == nil {
		t.Fatal("expected deferred refresh error")
	}
	manager.finishRefreshJob(job, time.Time{}, false)

	updated, ok := manager.GetByID(registered.ID)
	if !ok || updated == nil {
		t.Fatal("credential missing after deferred refresh")
	}
	if updated.RequiresSignIn || updated.RejectedRefreshToken != "" {
		t.Fatalf("wake deferral marked credential as requiring sign-in: %+v", updated)
	}
	if updated.NextRefreshAfter.IsZero() || !updated.NextRefreshAfter.After(now) {
		t.Fatalf("NextRefreshAfter = %v, want a positive backoff after %v to avoid a tight loop", updated.NextRefreshAfter, now)
	}
}
