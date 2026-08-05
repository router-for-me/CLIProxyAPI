package cliproxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceWatcherEchoPreservesIndependentQuotaAcrossPaymentReenable(t *testing.T) {
	models := registry.GetClaudeModels()
	if len(models) < 2 {
		t.Fatalf("claude model catalog has %d models, want at least 2", len(models))
	}
	quotaModel := models[0].ID
	paymentModel := models[1].ID
	reg := registry.GetGlobalRegistry()
	quotaBaseline := reg.GetModelCount(quotaModel)
	paymentBaseline := reg.GetModelCount(paymentModel)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "payment-watcher-echo-auth",
		Provider: "claude",
		Status:   coreauth.StatusActive,
	}
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	service.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     auth.ID,
		Auth:   auth,
	})
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    quotaModel,
		Error:    &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
	})
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    paymentModel,
		Error:    &coreauth.Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})

	disabledEcho := &coreauth.Auth{
		ID:       auth.ID,
		Provider: auth.Provider,
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		Metadata: map[string]any{
			"disabled":        true,
			"disabled_reason": "payment_required",
		},
	}
	service.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     auth.ID,
		Auth:   disabledEcho,
	})
	assertIndependentQuotaState(t, manager, auth.ID, quotaModel)
	if got := reg.GetModelCount(quotaModel); got != quotaBaseline {
		t.Fatalf("quota model count after disabled watcher echo = %d, want baseline %d", got, quotaBaseline)
	}

	reenabled, _ := manager.GetByID(auth.ID)
	reenabled.Disabled = false
	reenabled.Status = coreauth.StatusActive
	reenabled.StatusMessage = ""
	reenabled.Metadata["disabled"] = false
	delete(reenabled.Metadata, "disabled_reason")
	if _, err := manager.Update(context.Background(), reenabled); err != nil {
		t.Fatalf("management re-enable: %v", err)
	}

	activeEcho := &coreauth.Auth{
		ID:       auth.ID,
		Provider: auth.Provider,
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"disabled": false},
	}
	service.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     auth.ID,
		Auth:   activeEcho,
	})
	assertIndependentQuotaState(t, manager, auth.ID, quotaModel)
	if got := reg.GetModelCount(quotaModel); got != quotaBaseline {
		t.Fatalf("quota model count after active watcher echo = %d, want baseline %d", got, quotaBaseline)
	}
	if got := reg.GetModelCount(paymentModel); got != paymentBaseline+1 {
		t.Fatalf("payment model count after re-enable = %d, want %d", got, paymentBaseline+1)
	}
}

func assertIndependentQuotaState(t *testing.T, manager *coreauth.Manager, authID, model string) {
	t.Helper()
	current, ok := manager.GetByID(authID)
	if !ok || current == nil {
		t.Fatal("auth missing")
	}
	state := current.ModelStates[model]
	if state == nil || !state.Unavailable || !state.Quota.Exceeded ||
		state.LastError == nil || state.LastError.HTTPStatus != http.StatusTooManyRequests ||
		state.NextRetryAfter.Before(time.Now()) {
		t.Fatalf("independent quota state changed: %#v", state)
	}
}
