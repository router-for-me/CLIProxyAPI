package cliproxy

import (
	"context"
	"encoding/base64"
	"net/http"
	"sync"
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type xaiTierRefreshTestExecutor struct {
	mu             sync.Mutex
	rejectedToken  string
	refreshedToken string
	executeTokens  []string
}

func (e *xaiTierRefreshTestExecutor) Identifier() string { return "xai" }

func (e *xaiTierRefreshTestExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	token := xaiAuthAccessToken(auth)
	e.mu.Lock()
	e.executeTokens = append(e.executeTokens, token)
	e.mu.Unlock()
	if token == e.rejectedToken {
		return cliproxyexecutor.Response{}, &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "expired token"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *xaiTierRefreshTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *xaiTierRefreshTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = e.refreshedToken
	return auth, nil
}

func (e *xaiTierRefreshTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *xaiTierRefreshTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *xaiTierRefreshTestExecutor) executeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.executeTokens)
}

func xaiAuthAccessToken(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	token, _ := auth.Metadata["access_token"].(string)
	return token
}

func TestRegisterModelsForAuthScopesXAIByOAuthTier(t *testing.T) {
	paidToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"tier":4}`)) + ".signature"
	tests := []struct {
		name          string
		token         string
		baseURL       string
		usingAPI      string
		wantOnlyFree  bool
		wantComposer  bool
		wantPaidModel bool
	}{
		{name: "free", token: "opaque-free-token", wantOnlyFree: true},
		{name: "paid", token: paidToken, wantComposer: true, wantPaidModel: true},
		{name: "free explicit api override", token: "opaque-free-token", usingAPI: "true", wantComposer: true, wantPaidModel: true},
		{name: "paid explicit cli override", token: paidToken, usingAPI: "false", wantComposer: true, wantPaidModel: true},
		{name: "unknown explicit cli override", token: "opaque-free-token", usingAPI: "false", wantOnlyFree: true},
		{name: "custom gateway", token: "opaque-token", baseURL: "https://gateway.example.com/v1", wantComposer: true, wantPaidModel: true},
		{name: "explicit cli proxy", token: "opaque-token", baseURL: xaiauth.CLIChatProxyBaseURL, wantComposer: true, wantPaidModel: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &coreauth.Auth{
				ID:       "xai-entitlement-" + tt.name,
				Provider: "xai",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": coreauth.AuthKindOAuth,
					"base_url":  tt.baseURL,
				},
				Metadata: map[string]any{"access_token": tt.token},
			}
			if tt.usingAPI != "" {
				auth.Attributes[xaiauth.UsingAPIKey] = tt.usingAPI
			}
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(auth.ID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })

			(&Service{}).registerModelsForAuth(context.Background(), auth)
			models := modelRegistry.GetModelsForClient(auth.ID)
			if tt.wantOnlyFree {
				if len(models) != 1 || models[0] == nil || models[0].ID != xaiauth.FreeOAuthModel {
					t.Fatalf("free models = %#v, want only %q", modelIDs(models), xaiauth.FreeOAuthModel)
				}
				return
			}
			if got := containsModelID(models, "grok-composer-2.5-fast"); got != tt.wantComposer {
				t.Fatalf("composer registered = %v, want %v", got, tt.wantComposer)
			}
			if got := containsModelID(models, "grok-4.3"); got != tt.wantPaidModel {
				t.Fatalf("paid model registered = %v, want %v", got, tt.wantPaidModel)
			}
		})
	}
}

func TestRegisterModelsForAuthScopesXAIAPIKeyComposer(t *testing.T) {
	tests := []struct {
		name         string
		baseURL      string
		wantComposer bool
	}{
		{name: "empty official base", wantComposer: false},
		{name: "explicit official base", baseURL: xaiauth.DefaultAPIBaseURL, wantComposer: false},
		{name: "custom gateway", baseURL: "https://gateway.example.com/v1", wantComposer: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &coreauth.Auth{
				ID:       "xai-api-key-composer-" + tt.name,
				Provider: "xai",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": coreauth.AuthKindAPIKey,
					"api_key":   "test-key",
					"base_url":  tt.baseURL,
				},
			}
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(auth.ID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })

			(&Service{}).registerModelsForAuth(context.Background(), auth)
			models := modelRegistry.GetModelsForClient(auth.ID)
			if got := containsModelID(models, "grok-composer-2.5-fast"); got != tt.wantComposer {
				t.Fatalf("composer registered = %v, want %v", got, tt.wantComposer)
			}
			if !containsModelID(models, "grok-4.5") {
				t.Fatal("standard model grok-4.5 was not registered")
			}
		})
	}
}

func TestXAIRefreshReregistersModelsWhenOAuthTierChanges(t *testing.T) {
	paidToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"tier":4}`)) + ".signature"
	tests := []struct {
		name        string
		initial     string
		refreshed   string
		usingAPI    string
		initialPaid bool
		wantPaid    bool
	}{
		{name: "free_to_paid", initial: "opaque-free-token", refreshed: paidToken, wantPaid: true},
		{name: "paid_to_free", initial: paidToken, refreshed: "opaque-refreshed-free-token", initialPaid: true},
		{name: "explicit_api_free_to_paid", initial: "opaque-free-token", refreshed: paidToken, usingAPI: "true", initialPaid: true, wantPaid: true},
		{name: "explicit_api_paid_to_unknown", initial: paidToken, refreshed: "opaque-refreshed-token", usingAPI: "true", initialPaid: true, wantPaid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			manager := coreauth.NewManager(nil, nil, nil)
			testExecutor := &xaiTierRefreshTestExecutor{
				rejectedToken:  tt.initial,
				refreshedToken: tt.refreshed,
			}
			manager.RegisterExecutor(testExecutor)
			service, errBuild := NewBuilder().
				WithConfig(&config.Config{}).
				WithConfigPath("test-config.yaml").
				WithCoreAuthManager(manager).
				Build()
			if errBuild != nil {
				t.Fatalf("build service: %v", errBuild)
			}

			auth := &coreauth.Auth{
				ID:       "xai-refresh-tier-" + tt.name,
				Provider: "xai",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": coreauth.AuthKindOAuth,
					"base_url":  xaiauth.DefaultAPIBaseURL,
				},
				Metadata: map[string]any{
					"access_token":  tt.initial,
					"refresh_token": "refresh-token",
				},
			}
			if tt.usingAPI != "" {
				auth.Attributes[xaiauth.UsingAPIKey] = tt.usingAPI
			}
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(auth.ID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })

			if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			service.completeModelRegistrationForAuth(ctx, auth)
			if got := containsModelID(modelRegistry.GetModelsForClient(auth.ID), "grok-4.3"); got != tt.initialPaid {
				t.Fatalf("paid model before refresh = %v, want %v", got, tt.initialPaid)
			}
			if _, errExecute := manager.Execute(ctx, []string{"xai"}, cliproxyexecutor.Request{Model: xaiauth.FreeOAuthModel}, cliproxyexecutor.Options{}); errExecute != nil {
				t.Fatalf("execute triggering refresh: %v", errExecute)
			}
			if got := testExecutor.executeCount(); got != 2 {
				t.Fatalf("execute calls after refresh = %d, want 2", got)
			}

			models := modelRegistry.GetModelsForClient(auth.ID)
			if got := containsModelID(models, "grok-4.3"); got != tt.wantPaid {
				t.Fatalf("paid model after refresh = %v, want %v; models=%v", got, tt.wantPaid, modelIDs(models))
			}
			if got := containsModelID(models, "grok-composer-2.5-fast"); got != tt.wantPaid {
				t.Fatalf("composer model after refresh = %v, want %v", got, tt.wantPaid)
			}

			callsBeforePaidRequest := testExecutor.executeCount()
			_, errPaid := manager.Execute(ctx, []string{"xai"}, cliproxyexecutor.Request{Model: "grok-4.3"}, cliproxyexecutor.Options{})
			if tt.wantPaid {
				if errPaid != nil {
					t.Fatalf("paid model was not selectable after upgrade: %v", errPaid)
				}
				if got := testExecutor.executeCount(); got != callsBeforePaidRequest+1 {
					t.Fatalf("paid request execute calls = %d, want %d", got, callsBeforePaidRequest+1)
				}
				return
			}
			if errPaid == nil {
				t.Fatal("paid model remained selectable after downgrade")
			}
			if got := testExecutor.executeCount(); got != callsBeforePaidRequest {
				t.Fatalf("downgraded auth reached executor for paid model; calls = %d, want %d", got, callsBeforePaidRequest)
			}
		})
	}
}

func containsModelID(models []*ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && model.ID == id {
			return true
		}
	}
	return false
}

func modelIDs(models []*ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	return ids
}
