package modelalias

import (
	"sort"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestApplyClientAPIKeyModelAliasesToOpenAIMaps_CompatBridgeFork(t *testing.T) {
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
	sort.Strings(ids)
	want := []string{"gpt5.3", "mimo-v2.5"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

func TestApplyClientAPIKeyModelAliasesToOpenAIMaps_DevLikeFork(t *testing.T) {
	keys := config.ClientAPIKeys{
		{
			Key: "sk-test",
			ModelAliases: []config.OAuthModelAlias{
				{Name: "mimo-v2.5", Alias: "gpt5.3", Fork: true},
				{Name: "mimo-v2.5-pro", Alias: "opus4.8", Fork: true},
			},
		},
	}
	models := []map[string]any{
		{"id": "opus4.5", "object": "model"},
		{"id": "opus4.1", "object": "model"},
	}
	compat := []config.OpenAICompatibility{
		{
			Name: "test",
			Models: []config.OpenAICompatibilityModel{
				{Name: "mimo-v2.5", Alias: "opus4.5"},
				{Name: "mimo-v2.5-pro", Alias: "opus4.1"},
			},
		},
	}
	out := ApplyClientAPIKeyModelAliasesToOpenAIMaps(keys, "sk-test", models, compat)
	ids := make([]string, 0, len(out))
	for _, m := range out {
		ids = append(ids, m["id"].(string))
	}
	sort.Strings(ids)
	want := []string{"gpt5.3", "mimo-v2.5", "mimo-v2.5-pro", "opus4.8"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}
