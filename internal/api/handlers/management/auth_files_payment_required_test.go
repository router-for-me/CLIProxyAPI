package management

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyAuthDisabledStateReenablesPaymentDisabledAuth(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{
		QuotaExceeded: config.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &coreauth.Auth{
		ID:       "empty-model-payment-disabled",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"disabled": false},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Error:    &coreauth.Error{HTTPStatus: http.StatusPaymentRequired},
	})
	auth, _ = manager.GetByID(auth.ID)
	if auth == nil || !auth.Disabled || auth.Status != coreauth.StatusDisabled || auth.Unavailable {
		t.Fatalf("402 did not apply only the disable overlay: %#v", auth)
	}

	applyAuthDisabledState(auth, false)

	assertPaymentDisabledAuthReenabled(t, auth)
}

func TestSyncAuthFileDisabledStateReenablesPaymentDisabledAuth(t *testing.T) {
	auth := paymentDisabledAuthForManagementTest()
	auth.Metadata["disabled"] = false

	syncAuthFileDisabledState(auth)

	assertPaymentDisabledAuthReenabled(t, auth)
}

func paymentDisabledAuthForManagementTest() *coreauth.Auth {
	retryAt := time.Now().Add(time.Hour)
	return &coreauth.Auth{
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		Metadata: map[string]any{
			"disabled":        true,
			"disabled_reason": "payment_required",
		},
		ModelStates: map[string]*coreauth.ModelState{
			"quota-model": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: retryAt,
				Quota: coreauth.QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: retryAt,
				},
				LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
			},
		},
	}
}

func assertPaymentDisabledAuthReenabled(t *testing.T, auth *coreauth.Auth) {
	t.Helper()
	if auth.Disabled || auth.Unavailable || auth.Status != coreauth.StatusActive {
		t.Fatalf("auth not re-enabled: disabled=%v unavailable=%v status=%s", auth.Disabled, auth.Unavailable, auth.Status)
	}
	if _, exists := auth.Metadata["disabled_reason"]; exists {
		t.Fatalf("payment disabled reason remained: %#v", auth.Metadata["disabled_reason"])
	}
	quotaState := auth.ModelStates["quota-model"]
	if quotaState != nil && (!quotaState.Unavailable || !quotaState.Quota.Exceeded || quotaState.NextRetryAfter.IsZero()) {
		t.Fatalf("independent quota state was changed: %#v", quotaState)
	}
}
