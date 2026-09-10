package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestMarkResult_DisableCoolingKeepsModelAvailable reproduces the production
// report: an OpenAI-compatibility upstream row with disable-cooling enabled
// still ended up cooling (surfacing as "auth_unavailable: no auth available"
// on subsequent requests) even though cooling is disabled for the credential.
// Every failure classification on the MarkResult path must honor
// cooldownDisabledForAuth — a disabled-cooling auth must never end up with
// Unavailable=true, a future NextRetryAfter, or a suspended model.
func TestMarkResult_DisableCoolingKeepsModelAvailable(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
	}{
		{"403 group not allowed", &Error{Message: `{"code":"GROUP_NOT_ALLOWED","message":"API Key 所属专属分组不再允许当前用户使用"}`, HTTPStatus: http.StatusForbidden}},
		{"401 unauthorized", &Error{Message: "unauthorized", HTTPStatus: http.StatusUnauthorized}},
		{"402 payment required", &Error{Message: "quota exceeded for plan", HTTPStatus: http.StatusPaymentRequired}},
		{"404 not found", &Error{Message: "model not found", HTTPStatus: http.StatusNotFound}},
		{"429 rate limited", &Error{Message: "rate limit exceeded", HTTPStatus: http.StatusTooManyRequests}},
		{"500 upstream failure", &Error{Message: "internal server error", HTTPStatus: http.StatusInternalServerError}},
		{"unknown status", &Error{Message: "something broke"}},
		{"model not supported (400)", &Error{Message: "Requested model is not supported", HTTPStatus: http.StatusBadRequest}},
		{"cloudflare challenge", &Error{Message: "<html>cf-mitigated challenge-platform</html>", HTTPStatus: http.StatusForbidden}},
		{"invalid grant", &Error{Message: "invalid_grant token expired", HTTPStatus: http.StatusBadRequest}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const (
				provider = "openai-compat-cooling-test"
				model    = "cooling-test-model"
				authID   = "cooling-test-auth"
			)
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       authID,
				Provider: provider,
				Status:   StatusActive,
				Metadata: map[string]any{"disable_cooling": true},
			}
			if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
				t.Fatalf("Register(): %v", errRegister)
			}

			manager.MarkResult(context.Background(), Result{
				AuthID:   authID,
				Provider: provider,
				Model:    model,
				Success:  false,
				Error:    tc.err,
			})

			updated := manager.auths[authID]
			if updated == nil {
				t.Fatalf("auth missing after MarkResult")
			}
			if updated.Unavailable {
				t.Fatalf("Unavailable = true, want false (disable-cooling)")
			}
			if !updated.NextRetryAfter.IsZero() && updated.NextRetryAfter.After(time.Now()) {
				t.Fatalf("NextRetryAfter = %v, want zero/past (disable-cooling)", updated.NextRetryAfter)
			}
			if state := updated.ModelStates[model]; state != nil {
				if state.Unavailable {
					t.Fatalf("model state Unavailable = true, want false (disable-cooling)")
				}
				if !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(time.Now()) {
					t.Fatalf("model state NextRetryAfter = %v, want zero/past (disable-cooling)", state.NextRetryAfter)
				}
				if state.Quota.Exceeded {
					t.Fatalf("model state Quota.Exceeded = true, want false (disable-cooling)")
				}
			}
			// The auth must remain pickable for the model.
			picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pick after failure = %v, want the disable-cooling auth to stay pickable", errPick)
			}
			if picked == nil || picked.ID != authID {
				t.Fatalf("picked = %v, want %q", picked, authID)
			}
		})
	}
}
