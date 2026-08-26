package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestXAIModelsForAuthAppliesCustomHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Gateway custom-token" {
			t.Fatalf("authorization = %q, want custom override", got)
		}
		if got := r.Header.Get("X-Gateway-Tenant"); got != "tenant-123" {
			t.Fatalf("X-Gateway-Tenant = %q, want tenant-123", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.6"}]}`))
	}))
	defer server.Close()

	service := &Service{}
	auth := &coreauth.Auth{
		ID:       "xai-model-discovery-custom-headers-test",
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind":               coreauth.AuthKindOAuth,
			"base_url":                server.URL + "/v1",
			"access_token":            "test-access-token",
			"header:Authorization":    "Gateway custom-token",
			"header:X-Gateway-Tenant": "tenant-123",
		},
	}

	models, err := service.xaiModelsForAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("xaiModelsForAuth() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "grok-4.6" {
		t.Fatalf("models = %#v, want grok-4.6", models)
	}
}

func TestXAIModelsErrorOmitsUpstreamBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echoed-access-token-secret", http.StatusUnauthorized)
	}))
	defer server.Close()
	auth := &coreauth.Auth{ID: "xai-error-body-test", Provider: "xai", Attributes: map[string]string{
		"auth_kind": coreauth.AuthKindOAuth, "base_url": server.URL + "/v1", "access_token": "token",
	}}
	_, err := (&Service{}).xaiModelsForAuth(context.Background(), auth)
	if err == nil || strings.Contains(err.Error(), "echoed-access-token-secret") {
		t.Fatalf("error = %v, want status-only error without upstream body", err)
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

func TestXAIModelsForAuthAlwaysFetches(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.6"}]}`))
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
	for i := 0; i < 2; i++ {
		models, err := service.xaiModelsForAuth(context.Background(), auth)
		if err != nil || len(models) != 1 || models[0].ID != "grok-4.6" {
			t.Fatalf("discovery %d = %#v, %v", i+1, models, err)
		}
	}
	if requests != 2 {
		t.Fatalf("upstream requests = %d, want 2", requests)
	}
}

func TestRegisterModelsForAuthUnregistersOnDiscoveryFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary failure", http.StatusBadGateway)
	}))
	defer server.Close()

	service := &Service{}
	auth := &coreauth.Auth{ID: "xai-register-failure-test", Provider: "xai", Attributes: map[string]string{
		"auth_kind": coreauth.AuthKindOAuth, "base_url": server.URL + "/v1", "access_token": "test-access-token",
	}}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: "old-model"}})
	defer reg.UnregisterClient(auth.ID)
	service.registerModelsForAuth(context.Background(), auth)
	if got := reg.GetModelsForClient(auth.ID); len(got) != 0 {
		t.Fatalf("registered models after failure = %#v, want empty", got)
	}
}

func TestXAIModelsResponseEmptyAndMissingFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "explicit empty data", body: `{"data":[]}`},
		{name: "explicit empty models", body: `{"models":[]}`},
		{name: "missing fields", body: `{}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			auth := &coreauth.Auth{ID: tc.name, Provider: "xai", Attributes: map[string]string{"auth_kind": coreauth.AuthKindOAuth, "base_url": server.URL + "/v1", "access_token": "token"}}
			models, err := (&Service{}).xaiModelsForAuth(context.Background(), auth)
			if tc.wantErr && err == nil {
				t.Fatal("expected protocol error")
			}
			if !tc.wantErr && (err != nil || len(models) != 0) {
				t.Fatalf("models=%#v err=%v", models, err)
			}
		})
	}
}

func TestMergeXAIModelsReasoningMetadata(t *testing.T) {
	static := []*registry.ModelInfo{{ID: "grok", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}}}
	missing := mergeXAIModels([]xaiRemoteModel{{ID: "grok"}}, static)
	if missing[0].Thinking == nil || len(missing[0].Thinking.Levels) != 2 {
		t.Fatalf("missing reasoning metadata = %#v, want static thinking preserved", missing[0].Thinking)
	}
	falseValue := false
	cleared := mergeXAIModels([]xaiRemoteModel{{ID: "grok", SupportsReasoning: &falseValue}}, static)
	if cleared[0].Thinking != nil {
		t.Fatalf("explicit false reasoning metadata = %#v, want cleared", cleared[0].Thinking)
	}
	trueValue := true
	overridden := mergeXAIModels([]xaiRemoteModel{{ID: "grok", SupportsReasoning: &trueValue, ReasoningEfforts: []xaiRemoteEffort{{Value: "minimal"}, {Value: "max"}}}}, static)
	if overridden[0].Thinking == nil || len(overridden[0].Thinking.Levels) != 2 || overridden[0].Thinking.Levels[0] != "minimal" {
		t.Fatalf("remote reasoning levels = %#v, want remote levels", overridden[0].Thinking)
	}
}

func TestMergeXAIModelsPreservesStaticNamesWhenRemoteOmitsName(t *testing.T) {
	models := mergeXAIModels([]xaiRemoteModel{{ID: "grok-4.6"}}, []*registry.ModelInfo{{
		ID: "grok-4.6", Name: "Grok 4.6", DisplayName: "Grok 4.6",
	}})
	if len(models) != 1 || models[0].Name != "Grok 4.6" || models[0].DisplayName != "Grok 4.6" {
		t.Fatalf("merged names = %#v, want static names preserved", models)
	}
}

func TestSameXAIModelRegistrationInputsRequiresLatestXAIProvider(t *testing.T) {
	current := &coreauth.Auth{ID: "same-inputs-test", Provider: "xai", Attributes: map[string]string{
		"auth_kind": coreauth.AuthKindOAuth, "access_token": "token",
	}}
	latest := current.Clone()
	latest.Provider = "claude"
	if sameXAIModelRegistrationInputs(current, latest) {
		t.Fatal("sameXAIModelRegistrationInputs() = true for non-xAI latest provider")
	}
}

func TestRefreshXAIRegistrationDoesNotRediscoverUnchangedAuth(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[{"id":"refresh-model"}]}`))
	}))
	defer server.Close()

	auth := &coreauth.Auth{ID: "xai-refresh-dedup-test", Provider: "xai", Attributes: map[string]string{
		"auth_kind": coreauth.AuthKindOAuth, "base_url": server.URL + "/v1", "access_token": "token",
	}}
	manager := coreauth.NewManager(nil, nil, nil)
	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	service := &Service{coreManager: manager}
	reg := registry.GetGlobalRegistry()
	defer reg.UnregisterClient(auth.ID)
	if !service.refreshModelRegistrationForAuthWithContext(context.Background(), registered, nil) {
		t.Fatal("refreshModelRegistrationForAuthWithContext() = false")
	}
	if requests != 1 {
		t.Fatalf("upstream requests = %d, want 1 for unchanged refresh snapshot", requests)
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
