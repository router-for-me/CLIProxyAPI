package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
)

func TestVertexOpenAPIModelSettingsChangeHashes(t *testing.T) {
	enabled, disabled := true, false
	vertexHashes, vertexSummaries := map[string]bool{}, map[string]bool{}
	aliasSummaries, settingSummaries := map[string]bool{}, map[string]bool{}
	for _, setting := range []*bool{nil, &enabled, &disabled} {
		models := []config.VertexCompatModel{{Name: "model", Alias: "alias", OpenAPI: setting}}
		vertexHashes[modelconfig.ComputeVertexCompatModelsHash(models)] = true
		vertexSummaries[SummarizeVertexModels(models).hash] = true
		aliasSummaries[summarizeOAuthModelAliasList([]config.OAuthModelAlias{{Name: "model", Alias: "alias", OpenAPI: setting}}).hash] = true
		settingSummaries[summarizeOAuthSettingsList([]config.OAuthModelSetting{{Name: "model", OpenAPI: setting}}).hash] = true
	}
	for name, hashes := range map[string]map[string]bool{"routing": vertexHashes, "vertex": vertexSummaries, "alias": aliasSummaries, "settings": settingSummaries} {
		if len(hashes) != 3 {
			t.Fatalf("%s hashes do not distinguish unset, true, and false: %v", name, hashes)
		}
	}
}

func TestVertexOpenAPICredentialConfigChanges(t *testing.T) {
	enabled, disabled := true, false
	for _, values := range [][2]*bool{{nil, &enabled}, {nil, &disabled}, {&enabled, &disabled}, {&disabled, nil}} {
		oldCfg := &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "test-key", OpenAPI: values[0], OpenAPILocation: "global"}}}
		newCfg := &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "test-key", OpenAPI: values[1], OpenAPILocation: "us-central1"}}}
		changes := strings.Join(BuildConfigChangeDetails(oldCfg, newCfg), "\n")
		for _, field := range []string{"vertex[0].openapi:", "vertex[0].openapi-location:"} {
			if !strings.Contains(changes, field) {
				t.Fatalf("missing %s change in %q", field, changes)
			}
		}
	}
}
