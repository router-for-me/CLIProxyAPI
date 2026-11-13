package cliproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type issue7512Plugin struct {
	provider string
	models   []pluginapi.ModelInfo
}

func (p issue7512Plugin) Identifier() string { return p.provider }

func (p issue7512Plugin) ParseAuth(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return pluginapi.AuthParseResponse{}, nil
}

func (p issue7512Plugin) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, nil
}

func (p issue7512Plugin) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, nil
}

func (p issue7512Plugin) RefreshAuth(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{}, nil
}

func (p issue7512Plugin) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: p.provider, Models: p.models}, nil
}

func (p issue7512Plugin) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: p.provider, Models: p.models}, nil
}

func (p issue7512Plugin) Execute(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{}, nil
}

func (p issue7512Plugin) ExecuteStream(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return pluginapi.ExecutorStreamResponse{}, nil
}

func (p issue7512Plugin) CountTokens(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{}, nil
}

func (p issue7512Plugin) HttpRequest(context.Context, pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return pluginapi.ExecutorHTTPResponse{}, nil
}

func TestIssue7512_AuthPluginEnforcesExclusionsAliasesDisabledAndKeepsCatalog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope pluginapi.ExecutorModelScope
	}{
		{name: "unset scope with auth provider", scope: ""},
		{name: "explicit oauth scope", scope: pluginapi.ExecutorModelScopeOAuth},
		{name: "explicit both scope with auth provider", scope: pluginapi.ExecutorModelScopeBoth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := "demo-issue7512-" + strings.ReplaceAll(tc.name, " ", "-")
			pluginID := provider + "-plugin"
			reg := registry.GetGlobalRegistry()
			staticClientID := "plugin:" + pluginID + ":" + provider + ":executor"
			reg.UnregisterClient(staticClientID)

			plugin := issue7512Plugin{
				provider: provider,
				models: []pluginapi.ModelInfo{
					{ID: "demo-a"},
					{ID: "demo-blocked"},
					{ID: "demo-per-auth-blocked"},
				},
			}

			host := pluginhost.New()
			host.RegisterPluginForTest(pluginID, pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProvider:       plugin,
					ModelProvider:      plugin,
					Executor:           plugin,
					ExecutorModelScope: tc.scope,
				},
			})

			manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
			cfg := &config.Config{
				OAuthExcludedModels: map[string][]string{
					provider: {"demo-blocked"},
				},
				OAuthModelAlias: map[string][]config.OAuthModelAlias{
					provider: {{Name: "demo-a", Alias: "demo-a-high", Fork: false}},
				},
			}
			service := &Service{cfg: cfg, coreManager: manager, pluginHost: host}

			host.RegisterModels(t.Context(), reg)
			host.RegisterExecutors(manager, reg)
			t.Cleanup(func() {
				reg.UnregisterClient(staticClientID)
			})

			// Phase 1: zero credentials must publish no models in ModelRegistry,
			// while still keeping static catalog definitions available for #6736.
			if available := reg.GetAvailableModelsByProvider(provider); len(available) != 0 {
				t.Fatalf("with zero credentials, GetAvailableModelsByProvider(%q) = %#v, want empty", provider, available)
			}
			if catalog := host.ModelsForProvider(provider); len(catalog) != 3 {
				t.Fatalf("with zero credentials, ModelsForProvider(%q) = %#v, want 3 catalog models", provider, catalog)
			}

			// Phase 2: an active credential must enforce oauth-excluded-models,
			// per-credential excluded_models, and oauth-model-alias (fork=false).
			authID := provider + "-auth-1"
			auth := &coreauth.Auth{
				ID:       authID,
				Provider: provider,
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind":       "oauth",
					"excluded_models": "demo-blocked,demo-per-auth-blocked",
				},
			}
			registered, errRegister := manager.Register(t.Context(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			t.Cleanup(func() {
				reg.UnregisterClient(authID)
			})

			service.registerModelsForAuth(t.Context(), registered)

			availableIDs := make(map[string]bool)
			for _, item := range reg.GetAvailableModelsByProvider(provider) {
				if item != nil && item.ID != "" {
					availableIDs[item.ID] = true
				}
			}
			if availableIDs["demo-blocked"] {
				t.Fatal("oauth-excluded model demo-blocked leaked into ModelRegistry")
			}
			if availableIDs["demo-per-auth-blocked"] {
				t.Fatal("per-credential excluded model demo-per-auth-blocked leaked into ModelRegistry")
			}
			if availableIDs["demo-a"] {
				t.Fatal("unaliased model demo-a leaked despite fork=false oauth-model-alias")
			}
			if !availableIDs["demo-a-high"] {
				t.Fatalf("aliased model demo-a-high missing from ModelRegistry: %#v", availableIDs)
			}

			// Phase 3: disabling the credential must remove all published models for the provider.
			disabledAuth := registered.Clone()
			disabledAuth.Disabled = true
			disabledAuth.Status = coreauth.StatusDisabled
			updated, errUpdate := manager.Update(t.Context(), disabledAuth)
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			service.registerModelsForAuth(t.Context(), updated)

			if available := reg.GetAvailableModelsByProvider(provider); len(available) != 0 {
				t.Fatalf("with disabled credential, GetAvailableModelsByProvider(%q) = %#v, want empty", provider, available)
			}
			if catalog := host.ModelsForProvider(provider); len(catalog) != 3 {
				t.Fatalf("with disabled credential, ModelsForProvider(%q) = %#v, want 3 catalog models", provider, catalog)
			}
		})
	}
}
