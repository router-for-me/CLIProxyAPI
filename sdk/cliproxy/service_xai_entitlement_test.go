package cliproxy

import (
	"context"
	"encoding/base64"
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRegisterModelsForAuthScopesXAIByOAuthTier(t *testing.T) {
	paidToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"tier":4}`)) + ".signature"
	tests := []struct {
		name          string
		token         string
		baseURL       string
		wantOnlyFree  bool
		wantComposer  bool
		wantPaidModel bool
	}{
		{name: "free", token: "opaque-free-token", wantOnlyFree: true},
		{name: "paid", token: paidToken, wantComposer: true, wantPaidModel: true},
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
