package modelalias

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestApplyClientAPIKeyModelAliasesToOpenAIMaps_CompatBridge(t *testing.T) {
	keys := config.ClientAPIKeys{
		{
			Key: "sk-test",
			ModelAliases: []config.OAuthModelAlias{
				{Name: "mimo-v2.5", Alias: "gpt5.3", Fork: true},
			},
		},
	}
	models := []map[string]any{
		{"id": "gpt5.5", "object": "model"},
	}
	compat := []config.OpenAICompatibility{
		{
			Name: "test",
			Models: []config.OpenAICompatibilityModel{
				{Name: "mimo-v2.5", Alias: "gpt5.5"},
			},
		},
	}
	out := ApplyClientAPIKeyModelAliasesToOpenAIMaps(keys, "sk-test", models, compat)
	ids := make([]string, 0, len(out))
	for _, m := range out {
		ids = append(ids, m["id"].(string))
	}
	want := map[string]bool{"gpt5.5": true, "gpt5.3": true}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected id %q in %v", id, ids)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("missing ids %v, got %v", want, ids)
	}
}
