package openai

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexClientModelsUltrafastFollowsAccountPool(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "codex-ultrafast-catalog"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{ID: "gpt-6.1-sol"},
		{ID: "gpt-6-astra"},
		{ID: "gpt-6-sol"},
		{ID: "ultrafast-alias", MetadataModelID: "gpt-6.1-sol"},
		{ID: "prefix/gpt-6-astra"},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	if _, errRegister := manager.Register(t.Context(), &auth.Auth{
		ID: clientID, Provider: "codex", Status: auth.StatusActive,
		Attributes: map[string]string{"plan_type": "pro"},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	assertTiers := func(available bool) {
		t.Helper()
		entries := handler.codexClientModelsResponse("0.162.0")["models"].([]map[string]any)
		for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "ultrafast-alias", "prefix/gpt-6-astra"} {
			want := []string{"priority"}
			if available && model != "gpt-6-sol" {
				want = append(want, "ultrafast")
			}
			var got []string
			for _, entry := range entries {
				if entry["slug"] == model {
					for _, raw := range entry["service_tiers"].([]any) {
						got = append(got, raw.(map[string]any)["id"].(string))
					}
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s tiers = %v, want %v", model, got, want)
			}
		}
	}
	assertTiers(false)
	proMax := &auth.Auth{
		ID: "codex-ultrafast-promax", Provider: "codex", Status: auth.StatusActive,
		Attributes: map[string]string{"plan_type": "promax"},
	}
	if _, errRegister := manager.Register(t.Context(), proMax); errRegister != nil {
		t.Fatal(errRegister)
	}
	assertTiers(true)
	for _, tc := range []struct {
		name      string
		plan      string
		provider  string
		disabled  bool
		status    auth.Status
		available bool
	}{
		{name: "disabled", plan: "promax", provider: "codex", disabled: true, status: auth.StatusDisabled},
		{name: "downgraded", plan: "pro", provider: "codex", status: auth.StatusActive},
		{name: "other provider", plan: "promax", provider: "openai", status: auth.StatusActive},
		{name: "re-enabled", plan: " PROMAX ", provider: "codex", status: auth.StatusActive, available: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated := proMax.Clone()
			updated.Provider, updated.Disabled, updated.Status = tc.provider, tc.disabled, tc.status
			updated.Attributes["plan_type"] = tc.plan
			if _, errUpdate := manager.Update(t.Context(), updated); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			assertTiers(tc.available)
		})
	}
}
