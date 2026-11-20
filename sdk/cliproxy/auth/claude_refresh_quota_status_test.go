package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// renewingRefreshExecutor fails renewals while failErr is set and otherwise
// returns a fresh access token with a future expiry.
type renewingRefreshExecutor struct {
	*unauthorizedRefreshExecutor
	failErr error
}

func (e *renewingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if e.failErr != nil {
		return nil, e.failErr
	}
	auth.Metadata["access_token"] = "renewed-access"
	auth.Metadata["expired"] = time.Now().Add(8 * time.Hour).Format(time.RFC3339)
	return auth, nil
}

// A credential that is out of weekly quota and whose renewal once failed must not
// keep reporting "token expired" after a later renewal succeeds: the login is fine,
// only the quota cooldown remains.
func TestClaudeSuccessfulRefreshClearsStaleTokenExpiredDuringQuotaCooldown(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	executor := &renewingRefreshExecutor{unauthorizedRefreshExecutor: &unauthorizedRefreshExecutor{id: "claude"}}
	manager.RegisterExecutor(executor)

	model := "claude-opus-5-5"
	auth := &Auth{
		ID:       uuid.NewString() + "-claude-quota",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-access",
			"refresh_token": "refresh-grant",
			"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatal(errRegister)
	}

	weekly := 36 * time.Hour
	manager.MarkResult(ctx, Result{
		AuthID:     auth.ID,
		Provider:   "claude",
		Model:      model,
		RetryAfter: &weekly,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate_limit_error"},
	})

	executor.failErr = errors.New("token refresh request failed: write: broken pipe")
	manager.refreshAuth(ctx, auth.ID)
	failed, _ := manager.GetByID(auth.ID)
	if failed == nil || failed.StatusMessage != "token expired" {
		t.Fatalf("precondition: failed renewal of an expired token should report token expired, got %+v", failed)
	}

	executor.failErr = nil
	if _, errRefresh := manager.refreshAuthForRequestAtEpoch(ctx, auth.ID, "", 0); errRefresh != nil {
		t.Fatalf("renewal failed: %v", errRefresh)
	}
	renewed, _ := manager.GetByID(auth.ID)
	if renewed == nil {
		t.Fatal("credential missing")
	}
	if renewed.StatusMessage == "token expired" || renewed.RefreshError != nil {
		t.Fatalf("renewed credential still reports a renewal failure: status=%q message=%q refreshErr=%v lastErr=%v",
			renewed.Status, renewed.StatusMessage, renewed.RefreshError, renewed.LastError)
	}
	if renewed.LastError != nil && renewed.LastError.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("renewed credential kept a non-quota error: %v", renewed.LastError)
	}
	// The quota cooldown itself must survive the renewal.
	blocked, reason, next := isAuthBlockedForModel(renewed, model, time.Now())
	if !blocked || reason != blockReasonCooldown || next.Before(time.Now().Add(weekly-time.Minute)) {
		t.Fatalf("quota cooldown lost after renewal: blocked=%v reason=%v next=%v", blocked, reason, next)
	}
}
