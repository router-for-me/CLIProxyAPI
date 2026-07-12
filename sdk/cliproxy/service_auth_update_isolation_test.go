package cliproxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestServiceHandleAuthUpdates_PreservesUnrelatedAuthRuntimeState(t *testing.T) {
	testCases := []struct {
		name   string
		action watcher.AuthUpdateAction
		setup  func(t *testing.T, service *Service, auth *coreauth.Auth)
	}{
		{
			name:   "add",
			action: watcher.AuthUpdateActionAdd,
			setup:  func(*testing.T, *Service, *coreauth.Auth) {},
		},
		{
			name:   "modify",
			action: watcher.AuthUpdateActionModify,
			setup: func(t *testing.T, service *Service, auth *coreauth.Auth) {
				t.Helper()
				if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("register updated auth: %v", errRegister)
				}
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			service, otherID, modelID := newServiceWithErroredXAIAuth(t)
			target := testXAIAuth("xai-updated-auth.json")
			tt.setup(t, service, target)

			service.handleAuthUpdates(context.Background(), []watcher.AuthUpdate{{
				Action: tt.action,
				ID:     target.ID,
				Auth:   target,
			}})

			assertXAIAuthRegistered(t, service.coreManager, target.ID)
			assertErroredXAIAuthUnchanged(t, service.coreManager, otherID, modelID)
		})
	}
}

func TestServiceApplyCoreAuthRemoval_PreservesUnrelatedAuthRuntimeState(t *testing.T) {
	service, otherID, modelID := newServiceWithErroredXAIAuth(t)
	target := testXAIAuth("xai-removed-auth.json")
	if _, errRegister := service.coreManager.Register(context.Background(), target); errRegister != nil {
		t.Fatalf("register removed auth: %v", errRegister)
	}

	service.applyCoreAuthRemoval(context.Background(), target.ID)

	if _, ok := service.coreManager.GetByID(target.ID); ok {
		t.Fatalf("removed auth %q is still registered", target.ID)
	}
	if models := registry.GetGlobalRegistry().GetModelsForClient(target.ID); len(models) != 0 {
		t.Fatalf("removed auth %q still has %d registered models", target.ID, len(models))
	}
	assertErroredXAIAuthUnchanged(t, service.coreManager, otherID, modelID)
}

func newServiceWithErroredXAIAuth(t *testing.T) (*Service, string, string) {
	t.Helper()
	models := registry.GetXAIModels()
	if len(models) == 0 || models[0] == nil || models[0].ID == "" {
		t.Fatal("xAI model catalog is empty")
	}

	modelID := models[0].ID
	other := testXAIAuth("xai-errored-auth.json")
	retryAfter := time.Now().Add(time.Hour)
	other.Status = coreauth.StatusError
	other.StatusMessage = "upstream timeout"
	other.Unavailable = true
	other.LastError = &coreauth.Error{
		Code:       "upstream_error",
		Message:    "upstream timeout",
		HTTPStatus: http.StatusServiceUnavailable,
	}
	other.NextRetryAfter = retryAfter
	other.ModelStates = map[string]*coreauth.ModelState{
		modelID: {
			Status:        coreauth.StatusError,
			StatusMessage: "upstream timeout",
			Unavailable:   true,
			LastError: &coreauth.Error{
				Code:       "upstream_error",
				Message:    "upstream timeout",
				HTTPStatus: http.StatusServiceUnavailable,
			},
			NextRetryAfter: retryAfter,
			Quota: coreauth.QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: retryAfter,
				BackoffLevel:  2,
			},
		},
	}

	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), other); errRegister != nil {
		t.Fatalf("register errored auth: %v", errRegister)
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(other.ID)
		GlobalModelRegistry().UnregisterClient("xai-updated-auth.json")
		GlobalModelRegistry().UnregisterClient("xai-removed-auth.json")
		sdktranslator.SetPluginHooks(nil)
	})

	return &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		pluginHost:  pluginhost.New(),
	}, other.ID, modelID
}

func testXAIAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "xai",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type": "xai",
		},
	}
}

func assertXAIAuthRegistered(t *testing.T, manager *coreauth.Manager, authID string) {
	t.Helper()
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatalf("updated auth %q was not registered", authID)
	}
	if auth.Provider != "xai" {
		t.Fatalf("updated auth provider = %q, want xai", auth.Provider)
	}
	if models := registry.GetGlobalRegistry().GetModelsForClient(authID); len(models) == 0 {
		t.Fatalf("updated auth %q did not register models", authID)
	}
}

func assertErroredXAIAuthUnchanged(t *testing.T, manager *coreauth.Manager, authID, modelID string) {
	t.Helper()
	got, ok := manager.GetByID(authID)
	if !ok || got == nil {
		t.Fatalf("errored auth %q disappeared", authID)
	}
	if got.Status != coreauth.StatusError {
		t.Fatalf("auth status = %q, want %q", got.Status, coreauth.StatusError)
	}
	if got.StatusMessage != "upstream timeout" {
		t.Fatalf("auth status message = %q, want %q", got.StatusMessage, "upstream timeout")
	}
	if !got.Unavailable {
		t.Fatal("auth unavailable state was cleared")
	}
	if got.LastError == nil || got.LastError.Message != "upstream timeout" {
		t.Fatalf("auth last error = %#v, want upstream timeout", got.LastError)
	}
	if got.NextRetryAfter.Before(time.Now()) {
		t.Fatalf("auth retry deadline = %v, want an active cooldown", got.NextRetryAfter)
	}

	state := got.ModelStates[modelID]
	if state == nil {
		t.Fatalf("model state %q disappeared", modelID)
	}
	if state.Status != coreauth.StatusError {
		t.Fatalf("model status = %q, want %q", state.Status, coreauth.StatusError)
	}
	if state.StatusMessage != "upstream timeout" {
		t.Fatalf("model status message = %q, want %q", state.StatusMessage, "upstream timeout")
	}
	if !state.Unavailable {
		t.Fatal("model unavailable state was cleared")
	}
	if state.LastError == nil || state.LastError.Message != "upstream timeout" {
		t.Fatalf("model last error = %#v, want upstream timeout", state.LastError)
	}
	if state.NextRetryAfter.Before(time.Now()) {
		t.Fatalf("model retry deadline = %v, want an active cooldown", state.NextRetryAfter)
	}
	if !state.Quota.Exceeded || state.Quota.Reason != "quota" || state.Quota.BackoffLevel != 2 {
		t.Fatalf("model quota = %#v, want active quota state", state.Quota)
	}
}
