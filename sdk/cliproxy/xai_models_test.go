package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestXAIModelsForAuthUsesAccountCatalogAndStaticMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.6","name":"Account Grok","context_window":500000,"reasoning_efforts":[{"id":"low"},{"id":"high"}]},{"id":"account-only-model","description":"new account model"}]}`))
	}))
	defer server.Close()

	service := &Service{}
	auth := &coreauth.Auth{
		ID:       "xai-model-discovery-test",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind":    coreauth.AuthKindOAuth,
			"base_url":     server.URL + "/v1",
			"access_token": "test-access-token",
		},
	}

	models, err := service.xaiModelsForAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("xaiModelsForAuth() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}
	if models[0].ID != "grok-4.6" || models[0].ContextLength != 500000 {
		t.Fatalf("first model = %#v", models[0])
	}
	if models[0].Thinking == nil || len(models[0].Thinking.Levels) != 2 {
		t.Fatalf("first model thinking = %#v", models[0].Thinking)
	}
	if models[1].ID != "account-only-model" {
		t.Fatalf("second model ID = %q", models[1].ID)
	}
}

func TestRegisterModelsForAuthUsesDiscoveredOAuthModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"account-only-model"}]}`))
	}))
	defer server.Close()

	service := &Service{}
	auth := &coreauth.Auth{
		ID:       "xai-register-discovered-test",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind":    coreauth.AuthKindOAuth,
			"base_url":     server.URL + "/v1",
			"access_token": "test-access-token",
		},
	}
	registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	service.registerModelsForAuth(context.Background(), auth)
	models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	if len(models) != 1 || models[0].ID != "account-only-model" {
		t.Fatalf("registered models = %#v, want account-only-model only", models)
	}
}

func TestXAIModelsForAuthUsesLastSuccessfulResultOnTemporaryFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.6"}]}`))
			return
		}
		http.Error(w, "temporary failure", http.StatusBadGateway)
	}))
	defer server.Close()

	service := &Service{}
	auth := &coreauth.Auth{
		ID:       "xai-model-cache-test",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind":    coreauth.AuthKindOAuth,
			"base_url":     server.URL + "/v1",
			"access_token": "test-access-token",
		},
	}
	first, err := service.xaiModelsForAuth(context.Background(), auth)
	if err != nil || len(first) != 1 {
		t.Fatalf("first discovery = %#v, %v", first, err)
	}
	service.xaiModelsMu.Lock()
	entry := service.xaiModels[auth.ID]
	entry.fetchedAt = entry.fetchedAt.Add(-xaiModelsCacheTTL)
	service.xaiModels[auth.ID] = entry
	service.xaiModelsMu.Unlock()
	second, err := service.xaiModelsForAuth(context.Background(), auth)
	if err != nil || len(second) != 1 || second[0].ID != "grok-4.6" {
		t.Fatalf("cached discovery = %#v, %v", second, err)
	}
}

func TestResolveXAIModelsRoute(t *testing.T) {
	tests := []struct {
		name string
		auth *coreauth.Auth
		want string
		cli  bool
	}{
		{
			name: "oauth defaults to cli",
			auth: &coreauth.Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": coreauth.AuthKindOAuth}},
			want: "https://cli-chat-proxy.grok.com/v1",
			cli:  true,
		},
		{
			name: "oauth using api",
			auth: &coreauth.Auth{Provider: "xai", Attributes: map[string]string{"auth_kind": coreauth.AuthKindOAuth, "using_api": "true"}},
			want: "https://api.x.ai/v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cli := resolveXAIModelsRoute(nil, tt.auth)
			if got != tt.want || cli != tt.cli {
				t.Fatalf("route = %q, cli=%v; want %q, cli=%v", got, cli, tt.want, tt.cli)
			}
		})
	}
}
