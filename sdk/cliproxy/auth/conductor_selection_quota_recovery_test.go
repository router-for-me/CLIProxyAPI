package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A Claude weekly-limit 429 (Retry-After ~18 days in #5770) puts the
// credential and both models into a multi-day cooldown. Once those deadlines
// pass, selection must recover without a restart: nothing may keep blocking a
// credential whose cooldown window has expired.
func TestManager_SelectionRecoversAfterExpiredQuotaCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetSelector(&FillFirstSelector{})
	ctx := context.Background()

	auth := &Auth{ID: "claude-5770", Provider: "claude"}
	if _, errRegister := m.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}

	m.RegisterExecutor(schedulerProviderTestExecutor{provider: "claude"})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-5"}, {ID: "claude-opus-5"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	retry := 1587827 * time.Second // ~18.4 days, as reported in #5770
	m.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "claude", Model: "claude-sonnet-5",
		Success: false, CredentialScope: true,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"},
		RetryAfter: &retry,
	})
	m.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "claude", Model: "claude-opus-5",
		Success: false, CredentialScope: true,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"},
		RetryAfter: &retry,
	})

	// Precondition: both models are rejected right now with model_cooldown.
	for _, model := range []string{"claude-sonnet-5", "claude-opus-5"} {
		_, errPick := m.SelectAuth(ctx, "claude", model, cliproxyexecutor.Options{})
		var cooldownErr *modelCooldownError
		if !errors.As(errPick, &cooldownErr) {
			t.Fatalf("precondition: model %s err=%v, want model_cooldown", model, errPick)
		}
	}

	// Advance the clock past every deadline stored for the credential.
	m.mu.Lock()
	stored := m.auths["claude-5770"]
	past := time.Now().Add(-time.Minute)
	stored.Quota.NextRecoverAt = past
	stored.NextRetryAfter = past
	for _, state := range stored.ModelStates {
		state.Quota.NextRecoverAt = past
		state.NextRetryAfter = past
	}
	m.mu.Unlock()

	// The expired cooldown must not block selection anymore.
	for _, model := range []string{"claude-sonnet-5", "claude-opus-5"} {
		if _, errPick := m.SelectAuth(ctx, "claude", model, cliproxyexecutor.Options{}); errPick != nil {
			t.Fatalf("model %s still blocked after its deadline passed: %v", model, errPick)
		}
	}
}
