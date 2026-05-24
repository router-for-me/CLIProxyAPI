package cliproxy

import (
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// TestServiceRegistersGrokExecutorForGrokProvider asserts that a Service built
// with a Provider:"grok" auth routes to *executor.GrokExecutor concretely — NOT
// to the *OpenAICompatExecutor default fallthrough.
//
// This test is the tripwire for the v1-surfaced architectural defect: when
// `case "grok":` is missing from sdk/cliproxy/service.go, grok auths silently
// route to OpenAICompatExecutor, which fails with HTTP 401 in production
// because it expects auth.Attributes["base_url"] / ["api_key"] (api-key flow)
// not auth.Metadata["access_token"] (OAuth flow). Removing the case arm causes
// the type assertion below to fail immediately.
func TestServiceRegistersGrokExecutorForGrokProvider(t *testing.T) {
	svc := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	auth := &coreauth.Auth{
		ID:       "grok-auth-1",
		Provider: "grok",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"access_token": "test-token",
		},
	}

	svc.ensureExecutorsForAuth(auth)

	registered, ok := svc.coreManager.Executor("grok")
	if !ok || registered == nil {
		t.Fatal("expected a registered executor for provider 'grok', got none")
	}

	// Type assertion: this fails the moment `case "grok":` is removed from
	// service.go and the default arm registers *OpenAICompatExecutor instead.
	ex, ok := registered.(*executor.GrokExecutor)
	if !ok {
		t.Fatalf("expected *executor.GrokExecutor, got %T — dispatch is routing grok auths to the wrong executor", registered)
	}
	if ex.Identifier() != "grok" {
		t.Errorf("GrokExecutor.Identifier() = %q; want %q", ex.Identifier(), "grok")
	}
}

func TestRegisterModelsForAuth_RegistersGrokProviderForRouting(t *testing.T) {
	svc := &Service{
		cfg: &config.Config{},
	}

	auth := &coreauth.Auth{
		ID:       "grok-routing-auth",
		Provider: "grok",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"access_token":  "test-token",
			"refresh_token": "test-refresh",
		},
	}

	reg := registry.GetGlobalRegistry()
	registry.UnregisterAuth(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterAuth(auth.ID)
	})

	svc.registerModelsForAuth(auth)

	providers := reg.GetModelProviders("grok-code-fast-1")
	if !slices.Contains(providers, "grok") {
		t.Fatalf("expected grok-code-fast-1 to be registered for provider grok, got %v", providers)
	}
}
