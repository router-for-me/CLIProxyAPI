package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	pluginStaticGlobalModel     = "plugin-static-global"
	pluginStaticCredentialModel = "plugin-static-credential"
)

type pluginStaticModelRegistrar struct {
	provider string
}

func (r pluginStaticModelRegistrar) RegisterModels(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
	return pluginapi.ModelRegistrationResponse{
		Provider: r.provider,
		Models:   []pluginapi.ModelInfo{{ID: pluginStaticGlobalModel}, {ID: pluginStaticCredentialModel}},
	}, nil
}

// newPluginStaticModelHost loads a static-only plugin so the provider's models
// are appended to native registrations instead of handled per auth.
func newPluginStaticModelHost(t *testing.T, provider string) *pluginhost.Host {
	t.Helper()
	host := pluginhost.New()
	pluginID := t.Name()
	host.RegisterPluginForTest(pluginID, pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelRegistrar: pluginStaticModelRegistrar{provider: provider}}})
	host.RegisterModels(t.Context(), registry.GetGlobalRegistry())
	host.RegisterExecutors(coreauth.NewManager(nil, nil, nil), registry.GetGlobalRegistry())
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("plugin:" + pluginID + ":" + provider) })
	if got := len(host.ModelsForProvider(provider)); got != 2 {
		t.Fatalf("plugin static models for %s = %d, want 2", provider, got)
	}
	return host
}

func TestRegisterModelsForAuth_PluginStaticModelsFollowExclusions(t *testing.T) {
	const provider = "kimi"
	cfg := &config.Config{OAuthExcludedModels: map[string][]string{provider: {pluginStaticGlobalModel}}}
	service := &Service{cfg: cfg, pluginHost: newPluginStaticModelHost(t, provider)}
	reg := GlobalModelRegistry()

	for _, tc := range []struct {
		name          string
		ownRules      string
		wantGlobal    bool
		wantCredValue bool
	}{
		{name: "global rules", wantGlobal: false, wantCredValue: true},
		{name: "credential rules override global", ownRules: pluginStaticCredentialModel, wantGlobal: true, wantCredValue: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
			if tc.ownRules != "" {
				auth.Attributes["excluded_models"] = tc.ownRules
			}
			reg.UnregisterClient(auth.ID)
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

			service.registerModelsForAuth(t.Context(), auth)

			present := make(map[string]bool)
			for _, model := range reg.GetModelsForClient(auth.ID) {
				present[model.ID] = true
			}
			if present[pluginStaticGlobalModel] != tc.wantGlobal || present[pluginStaticCredentialModel] != tc.wantCredValue {
				t.Fatalf("plugin static models = (%t,%t), want (%t,%t); models = %v", present[pluginStaticGlobalModel], present[pluginStaticCredentialModel], tc.wantGlobal, tc.wantCredValue, present)
			}

			// The public model list must not retain unfiltered plugin-only clients.
			available := openAIModelIDSet(reg.GetAvailableModels("openai"))
			_, hasGlobal := available[pluginStaticGlobalModel]
			_, hasCredential := available[pluginStaticCredentialModel]
			if hasGlobal != tc.wantGlobal || hasCredential != tc.wantCredValue {
				t.Fatalf("available plugin models = (%t,%t), want (%t,%t)", hasGlobal, hasCredential, tc.wantGlobal, tc.wantCredValue)
			}
		})
	}
}

func TestPluginStaticModelAvailabilityFollowsCredentials(t *testing.T) {
	const provider = "kimi"
	cfg := &config.Config{OAuthExcludedModels: map[string][]string{provider: {pluginStaticGlobalModel}}}
	host := newPluginStaticModelHost(t, provider)
	service := &Service{cfg: cfg, pluginHost: host}
	reg := GlobalModelRegistry()
	inherited := &coreauth.Auth{ID: t.Name() + "/inherited", Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
	override := &coreauth.Auth{ID: t.Name() + "/override", Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{
		"auth_kind":       "oauth",
		"excluded_models": pluginStaticCredentialModel,
	}}
	t.Cleanup(func() {
		reg.UnregisterClient(inherited.ID)
		reg.UnregisterClient(override.ID)
	})
	assertAvailable := func(stage string, wantGlobal, wantCredential bool) {
		t.Helper()
		available := openAIModelIDSet(reg.GetAvailableModels("openai"))
		_, hasGlobal := available[pluginStaticGlobalModel]
		_, hasCredential := available[pluginStaticCredentialModel]
		if hasGlobal != wantGlobal || hasCredential != wantCredential {
			t.Fatalf("%s: available plugin models = (%t,%t), want (%t,%t)", stage, hasGlobal, hasCredential, wantGlobal, wantCredential)
		}
	}

	assertAvailable("no credentials", false, false)
	service.registerModelsForAuth(t.Context(), inherited)
	assertAvailable("global exclusions", false, true)

	// A credential override may still expose a globally excluded model.
	service.registerModelsForAuth(t.Context(), override)
	assertAvailable("different credential exclusions", true, true)
	override.Attributes["excluded_models"] = "plugin-static-*"
	service.registerModelsForAuth(t.Context(), override)
	assertAvailable("last supporting credential excludes model", false, true)

	cfg.OAuthExcludedModels[provider] = []string{"plugin-static-*"}
	service.registerModelsForAuth(t.Context(), inherited)
	assertAvailable("all credentials exclude both models", false, false)

	// Re-loading the catalog must not republish models excluded by every auth.
	host.RegisterModels(t.Context(), registry.GetGlobalRegistry())
	host.RegisterExecutors(coreauth.NewManager(nil, nil, nil), registry.GetGlobalRegistry())
	assertAvailable("catalog reload", false, false)
	if models := host.ModelsForProvider(provider); len(models) != 2 {
		t.Fatalf("source catalog = %#v, want both models retained", models)
	}

	override.Attributes["excluded_models"] = pluginStaticCredentialModel
	service.registerModelsForAuth(t.Context(), override)
	assertAvailable("credential override after reload", true, false)
	override.Disabled = true
	service.registerModelsForAuth(t.Context(), override)
	assertAvailable("last supporting credential disabled", false, false)
	override.Disabled = false
	service.registerModelsForAuth(t.Context(), override)
	assertAvailable("credential re-enabled", true, false)
	reg.UnregisterClient(override.ID)
	assertAvailable("last supporting credential removed", false, false)
}

func TestRegisterModelsForAuth_PluginStaticExclusionsDoNotMatchNativeAliases(t *testing.T) {
	const provider = "kimi"
	const nativeAlias = "native-alias"
	catalog := registry.GetKimiModels()
	if len(catalog) == 0 {
		t.Fatal("kimi catalog is empty")
	}
	cfg := &config.Config{
		OAuthExcludedModels: map[string][]string{provider: {pluginStaticGlobalModel, nativeAlias}},
		OAuthModelAlias:     map[string][]config.OAuthModelAlias{provider: {{Name: catalog[0].ID, Alias: nativeAlias}}},
	}
	service := &Service{cfg: cfg, pluginHost: newPluginStaticModelHost(t, provider)}
	auth := &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(t.Context(), auth)

	// Exclusions match original model IDs, so an alias named like a rule stays visible.
	if !reg.ClientSupportsModel(auth.ID, nativeAlias) || reg.ClientSupportsModel(auth.ID, pluginStaticGlobalModel) {
		t.Fatalf("models = (%s:%t, %s:%t), want native alias kept and plugin model excluded", nativeAlias, reg.ClientSupportsModel(auth.ID, nativeAlias), pluginStaticGlobalModel, reg.ClientSupportsModel(auth.ID, pluginStaticGlobalModel))
	}
}

func TestAntigravityModelsForHints_PluginStaticModelsFollowExclusions(t *testing.T) {
	const provider = "antigravity"
	cfg := &config.Config{OAuthExcludedModels: map[string][]string{provider: {pluginStaticGlobalModel}}}
	service := &Service{cfg: cfg, pluginHost: newPluginStaticModelHost(t, provider)}
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}}}

	for _, tc := range []struct {
		name          string
		ownRules      string
		wantGlobal    bool
		wantCredValue bool
	}{
		{name: "global rules", wantGlobal: false, wantCredValue: true},
		{name: "credential rules override global", ownRules: pluginStaticCredentialModel, wantGlobal: true, wantCredValue: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := antigravityTestAuth(t.Name(), "http://127.0.0.1:1")
			if tc.ownRules != "" {
				auth.Attributes["excluded_models"] = tc.ownRules
			}

			present := make(map[string]bool)
			for _, model := range service.antigravityModelsForHints(auth, hints) {
				present[model.ID] = true
			}

			if present[pluginStaticGlobalModel] != tc.wantGlobal || present[pluginStaticCredentialModel] != tc.wantCredValue {
				t.Fatalf("plugin static models = (%t,%t), want (%t,%t); models = %v", present[pluginStaticGlobalModel], present[pluginStaticCredentialModel], tc.wantGlobal, tc.wantCredValue, present)
			}
		})
	}
}
