package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRegisterResolvedModelsZeroWeightFollowsRoutingStrategy(t *testing.T) {
	trueValue := true
	falseValue := false

	tests := []struct {
		strategy       string
		wantRegistered bool
		wantSearch     bool
	}{
		{strategy: "round-robin", wantRegistered: true, wantSearch: false},
		{strategy: "fill-first", wantRegistered: true, wantSearch: false},
		{strategy: "weighted-round-robin", wantRegistered: false, wantSearch: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.strategy, func(t *testing.T) {
			modelID := "zero-weight-web-search-model-" + testCase.strategy
			enabledClientID := "enabled-weight-client-" + testCase.strategy
			zeroClientID := "zero-weight-client-" + testCase.strategy
			globalRegistry := registry.GetGlobalRegistry()
			service := &Service{cfg: &internalconfig.Config{
				Routing: internalconfig.RoutingConfig{Strategy: testCase.strategy},
			}}

			service.registerResolvedModelsForAuth(&coreauth.Auth{
				ID:       enabledClientID,
				Provider: "codex",
			}, "codex", []*ModelInfo{{
				ID:             modelID,
				CodexWebSearch: &trueValue,
			}})
			service.registerResolvedModelsForAuth(&coreauth.Auth{
				ID:       zeroClientID,
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					coreauth.AttributeWeight: "0",
				},
			}, "openai-compatible-zero-weight", []*ModelInfo{{
				ID:             modelID,
				CodexWebSearch: &falseValue,
			}})
			t.Cleanup(func() {
				globalRegistry.UnregisterClient(enabledClientID)
				globalRegistry.UnregisterClient(zeroClientID)
			})

			if got := len(globalRegistry.GetModelsForClient(zeroClientID)); (got > 0) != testCase.wantRegistered {
				t.Fatalf("zero-weight client model count = %d, wantRegistered=%v", got, testCase.wantRegistered)
			}

			models := globalRegistry.GetAvailableModels("openai")
			if len(models) != 1 {
				t.Fatalf("models len = %d, want 1: %#v", len(models), models)
			}
			if got, ok := models[0]["codex_web_search"].(bool); !ok || got != testCase.wantSearch {
				t.Fatalf("codex_web_search = %#v, want %v", models[0]["codex_web_search"], testCase.wantSearch)
			}
		})
	}
}

func TestWeightedRoutingEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *internalconfig.Config
		want bool
	}{
		{name: "default", cfg: &internalconfig.Config{}, want: false},
		{name: "round-robin", cfg: &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "round-robin"}}, want: false},
		{name: "fill-first", cfg: &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first"}}, want: false},
		{name: "wrr alias", cfg: &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "wrr"}}, want: true},
		{name: "weighted-round-robin", cfg: &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "weighted-round-robin"}}, want: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &Service{cfg: testCase.cfg}
			if got := service.weightedRoutingEnabled(); got != testCase.want {
				t.Fatalf("weightedRoutingEnabled() = %v, want %v", got, testCase.want)
			}
		})
	}
}
