package auth

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestCompileVertexAPIKeyOpenAPICapabilities(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name       string
		model      string
		credential *bool
		setting    *bool
		thinking   *registry.ThinkingSupport
		levels     []string
	}{
		{name: "automatic partner", model: "partner/custom-model", levels: []string{"low", "medium", "high"}},
		{name: "resource model", model: "publishers/partner/models/custom-model", levels: []string{"low", "medium", "high"}},
		{name: "credential opt-in", model: "custom-model", credential: &enabled, levels: []string{"low", "medium", "high"}},
		{name: "credential opt-out", model: "partner/custom-model", credential: &disabled},
		{name: "model opt-in", model: "custom-model", credential: &disabled, setting: &enabled, levels: []string{"low", "medium", "high"}},
		{name: "model opt-out", model: "partner/custom-model", credential: &enabled, setting: &disabled},
		{name: "google prefix", model: "google/custom-model"},
		{name: "explicit thinking", model: "partner/custom-model", thinking: &registry.ThinkingSupport{Levels: []string{"high"}}, levels: []string{"high"}},
		{name: "static thinking preserved", model: "gemini-2.5-pro", credential: &enabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{
				APIKey: "test-key", OpenAPI: tc.credential,
				Models: []config.VertexCompatModel{{Name: tc.model, Alias: "public-model", OpenAPI: tc.setting, Thinking: tc.thinking}},
			}}}
			auth := &Auth{Provider: "vertex", Attributes: map[string]string{AttributeAPIKey: "test-key", AttributeSource: "config:vertex-apikey[test]"}}
			capabilities := compileAPIKeyModelCapabilitiesForAuth(cfg, auth)
			routes := capabilities["public-model"]
			if len(routes) != 1 || routes[0].modelInfo == nil {
				t.Fatalf("capability routes = %+v", routes)
			}
			info := routes[0].modelInfo
			if tc.name == "static thinking preserved" {
				want := modelconfig.ResolveModelInfo(tc.model, "gemini", nil).Thinking
				if want == nil || !reflect.DeepEqual(info.Thinking, want) {
					t.Fatalf("Thinking = %+v, want static support %+v", info.Thinking, want)
				}
				return
			}
			var levels []string
			if info.Thinking != nil {
				levels = info.Thinking.Levels
			}
			if !reflect.DeepEqual(levels, tc.levels) {
				t.Fatalf("Thinking levels = %v, want %v", levels, tc.levels)
			}
		})
	}
}
