package auth

import (
	"context"
	"net/http"
	"testing"
)

func TestManagerMarkResult_DisablesAuthOnInsufficientBalance402(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
	}{
		{name: "english", message: "Insufficient Balance"},
		{name: "chinese", message: "余额不足，请充值后重试"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := NewManager(nil, nil, nil)
			auth := &Auth{
				ID:       "balance-auth-" + tt.name,
				Provider: "deepseek",
			}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			manager.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    "deepseek-v4-pro",
				Success:  false,
				Error: &Error{
					Code:       "upstream_error",
					Message:    tt.message,
					HTTPStatus: http.StatusPaymentRequired,
				},
			})

			updated, ok := manager.GetByID(auth.ID)
			if !ok {
				t.Fatal("auth not found")
			}
			if !updated.Disabled {
				t.Fatal("expected auth to be disabled after insufficient balance")
			}
			if updated.Status != StatusDisabled {
				t.Fatalf("status = %q, want %q", updated.Status, StatusDisabled)
			}
			if updated.StatusMessage != "disabled due to insufficient balance" {
				t.Fatalf("status message = %q", updated.StatusMessage)
			}
			state := updated.ModelStates["deepseek-v4-pro"]
			if state == nil || state.Status != StatusDisabled {
				t.Fatalf("model state = %+v, want disabled", state)
			}
		})
	}
}

func TestManagerMarkResult_DoesNotDisableBillingCycleQuota402(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "billing-cycle-auth",
		Provider: "claude",
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "claude-sonnet-4-6",
		Success:  false,
		Error: &Error{
			Code:       "quota_exceeded",
			Message:    "You have reached your usage limit for this billing cycle.",
			HTTPStatus: http.StatusPaymentRequired,
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if updated.Disabled || updated.Status == StatusDisabled {
		t.Fatalf("auth disabled=%v status=%q, want quota cooldown but not disabled", updated.Disabled, updated.Status)
	}
	state := updated.ModelStates["claude-sonnet-4-6"]
	if state == nil || state.Status == StatusDisabled {
		t.Fatalf("model state = %+v, want non-disabled quota state", state)
	}
	if state.Quota.Reason != "billing_cycle_quota" {
		t.Fatalf("quota reason = %q, want billing_cycle_quota", state.Quota.Reason)
	}
}
