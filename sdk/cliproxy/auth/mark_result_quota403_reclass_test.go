package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// withQuota403Reclassification enables the opt-in 403→429 reclassification
// gate for the duration of a test, restoring the previous value on cleanup.
func withQuota403Reclassification(t *testing.T, enabled bool) {
	t.Helper()
	prev := reclassifyQuota403.Load()
	reclassifyQuota403.Store(enabled)
	t.Cleanup(func() { reclassifyQuota403.Store(prev) })
}

// TestMarkResultQuota403Reclassification verifies that quota-shaped 403
// failures take the retry-eligible 429 quota ladder (base 1s backoff) instead
// of the 30-minute payment_required cooldown when the opt-in gate is enabled.
// Billing-shaped 403s stay in the payment-required class in all cases.
func TestMarkResultQuota403Reclassification(t *testing.T) {
	const (
		provider = "quota403-reclass-test"
		model    = "quota403-test-model"
		authID   = "quota403-test-auth"
	)

	cases := []struct {
		name        string
		err         *Error
		gateEnabled bool
		wantQuota   bool
	}{
		{
			name:        "quota exceeded 403 with gate on takes quota ladder",
			err:         &Error{Code: "forbidden", Message: "quota exceeded for this model", HTTPStatus: http.StatusForbidden},
			gateEnabled: true,
			wantQuota:   true,
		},
		{
			name:        "quota exceeded 403 with gate off keeps payment_required class",
			err:         &Error{Code: "forbidden", Message: "quota exceeded for this model", HTTPStatus: http.StatusForbidden},
			gateEnabled: false,
			wantQuota:   false,
		},
		{
			name:        "billing required 403 with gate on stays payment_required class",
			err:         &Error{Code: "forbidden", Message: "billing required on this account", HTTPStatus: http.StatusForbidden},
			gateEnabled: true,
			wantQuota:   false,
		},
		{
			name:        "insufficient quota 403 with gate on takes quota ladder",
			err:         &Error{Code: "forbidden", Message: "insufficient quota for this model", HTTPStatus: http.StatusForbidden},
			gateEnabled: true,
			wantQuota:   true,
		},
		{
			name:        "plain forbidden 403 with gate on keeps payment_required class",
			err:         &Error{Code: "forbidden", Message: "forbidden", HTTPStatus: http.StatusForbidden},
			gateEnabled: true,
			wantQuota:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withQuota403Reclassification(t, tc.gateEnabled)

			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       authID,
				Provider: provider,
				Status:   StatusActive,
			}
			if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
				t.Fatalf("Register(): %v", errRegister)
			}

			before := time.Now()
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
			state := updated.ModelStates[model]
			if state == nil {
				t.Fatalf("model state for %q missing after MarkResult", model)
			}
			if state.Quota.Exceeded != tc.wantQuota {
				t.Fatalf("Quota.Exceeded = %v, want %v", state.Quota.Exceeded, tc.wantQuota)
			}
			if tc.wantQuota {
				// Quota ladder base is 1s; anything near the 30-minute
				// unauthorized/payment cooldown means misclassification.
				if state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(before.Add(5*time.Second)) {
					t.Fatalf("NextRetryAfter = %v, want within ~1s of %v (quota ladder)", state.NextRetryAfter, before)
				}
			} else {
				if state.NextRetryAfter.Before(before.Add(29 * time.Minute)) {
					t.Fatalf("NextRetryAfter = %v, want ~30min cooldown (payment_required class)", state.NextRetryAfter)
				}
			}
		})
	}
}
