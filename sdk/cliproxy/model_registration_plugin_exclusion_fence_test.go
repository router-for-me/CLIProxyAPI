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

type exclusionFenceModelProvider struct {
	provider string
	onModels func() pluginapi.ModelResponse
}

func (p *exclusionFenceModelProvider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: p.provider, Models: []pluginapi.ModelInfo{{ID: "model-a"}, {ID: "model-b"}}}, nil
}

func (p *exclusionFenceModelProvider) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return p.onModels(), nil
}

func TestModelRegistrationPluginExclusionSources(t *testing.T) {
	const provider = "exclusion-fixture"
	for _, tc := range []struct {
		name       string
		compat     bool
		addPrimary bool
		updateAuth bool
	}{
		{name: "compat fallback changed", compat: true},
		{name: "compat primary added", compat: true, addPrimary: true},
		{name: "plugin auth update", updateAuth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := provider
			if tc.compat {
				channel = "openai-compatibility"
			}
			oldCfg := &config.Config{OAuthExcludedModels: map[string][]string{channel: {"model-a"}}}
			newCfg := &config.Config{OAuthExcludedModels: map[string][]string{channel: {"model-b"}}}
			if tc.addPrimary {
				newCfg.OAuthExcludedModels[channel] = []string{"model-a"}
				newCfg.OAuthExcludedModels[provider] = []string{"model-b"}
			}
			manager := coreauth.NewManager(nil, nil, nil)
			host := pluginhost.New()
			service := &Service{cfg: oldCfg, coreManager: manager, pluginHost: host}
			calls := 0
			modelProvider := &exclusionFenceModelProvider{provider: provider, onModels: func() pluginapi.ModelResponse {
				calls++
				if calls == 1 {
					if commit := service.commitConfigUpdate(newCfg); commit.cfg == nil {
						t.Fatal("config update was rejected")
					}
				}
				response := pluginapi.ModelResponse{Provider: provider, Models: []pluginapi.ModelInfo{{ID: "model-a"}, {ID: "model-b"}}}
				if tc.updateAuth {
					response.AuthUpdate = pluginapi.AuthData{Label: "updated by plugin"}
				}
				return response
			}}
			pluginID := t.Name()
			host.RegisterPluginForTest(pluginID, pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelProvider: modelProvider}})
			host.RegisterModels(t.Context(), registry.GetGlobalRegistry())
			reg := GlobalModelRegistry()
			t.Cleanup(func() { reg.UnregisterClient("plugin:" + pluginID + ":" + provider) })
			auth := &coreauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"auth_kind": "oauth"}}
			if tc.compat {
				auth.Attributes["compat_name"] = "compat-fixture"
			}
			registered, errRegister := manager.Register(t.Context(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			reg.RegisterClient(auth.ID, provider, []*ModelInfo{{ID: "model-old"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			epoch := reg.ClientRegistrationEpoch(auth.ID)
			service.registerModelsForAuth(t.Context(), registered)
			if calls != 1 {
				t.Fatalf("plugin calls = %d, want 1", calls)
			}
			if got := reg.ClientRegistrationEpoch(auth.ID); got != epoch {
				t.Fatalf("obsolete plugin result advanced epoch from %d to %d", epoch, got)
			}
			current, ok := manager.GetByID(auth.ID)
			if !ok {
				t.Fatal("credential disappeared")
			}
			if tc.updateAuth && current.Label != "updated by plugin" {
				t.Fatal("test did not exercise the plugin auth update path")
			}
			service.registerModelsForAuth(t.Context(), current)
			if !reg.ClientSupportsModel(auth.ID, "model-a") || reg.ClientSupportsModel(auth.ID, "model-b") {
				t.Fatalf("final plugin models = %v, want only model-a", reg.GetModelsForClient(auth.ID))
			}
		})
	}
}

func TestModelRegistrationPluginPublishedPrefixExclusions(t *testing.T) {
	const provider = "exclusion-fixture"
	for _, tc := range []struct {
		name  string
		rule  string
		wantA bool
	}{
		{name: "published prefix", rule: "team//model-a"},
		{name: "normalized prefix", rule: "team/model-a"},
		{name: "different prefix", rule: "other/model-a", wantA: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			host := pluginhost.New()
			service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: host}
			modelProvider := &exclusionFenceModelProvider{provider: provider, onModels: func() pluginapi.ModelResponse {
				return pluginapi.ModelResponse{
					Provider:   provider,
					Models:     []pluginapi.ModelInfo{{ID: "model-a"}, {ID: "model-b"}},
					AuthUpdate: pluginapi.AuthData{Prefix: "team/"},
				}
			}}
			pluginID := t.Name()
			host.RegisterPluginForTest(pluginID, pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelProvider: modelProvider}})
			reg := GlobalModelRegistry()
			host.RegisterModels(t.Context(), reg)
			t.Cleanup(func() { reg.UnregisterClient("plugin:" + pluginID + ":" + provider) })
			auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
				ID:       t.Name(),
				Provider: provider,
				Prefix:   "other",
				Attributes: map[string]string{
					"auth_kind":       "oauth",
					"excluded_models": tc.rule,
				},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			service.registerModelsForAuth(t.Context(), auth)
			if reg.ClientSupportsModel(auth.ID, "model-a") != tc.wantA || reg.ClientSupportsModel(auth.ID, "team//model-a") != tc.wantA {
				t.Fatalf("published-prefix exclusion not respected: %v", reg.GetModelsForClient(auth.ID))
			}
			if !reg.ClientSupportsModel(auth.ID, "model-b") || !reg.ClientSupportsModel(auth.ID, "team//model-b") {
				t.Fatalf("allowed model or its existing published route disappeared: %v", reg.GetModelsForClient(auth.ID))
			}
		})
	}
}
