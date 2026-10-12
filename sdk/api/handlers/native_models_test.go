package handlers

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestNativeProviderModels(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	claudeModel, codexModel, sharedModel := "native-list-claude-model", "native-list-codex-model", "native-list-shared-model"
	modelRegistry.RegisterClient("native-list-claude-client", "claude", []*registry.ModelInfo{{ID: claudeModel}, {ID: sharedModel}})
	modelRegistry.RegisterClient("native-list-codex-client", "codex", []*registry.ModelInfo{{ID: codexModel}, {ID: sharedModel}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("native-list-claude-client")
		modelRegistry.UnregisterClient("native-list-codex-client")
	})

	models := []map[string]any{
		{"id": claudeModel},
		{"id": codexModel},
		{"id": sharedModel},
		{"id": "native-list-unregistered-model"},
		{"object": "model"},
	}
	ids := func(list []map[string]any) []string {
		out := make([]string, 0, len(list))
		for _, model := range list {
			id, _ := model["id"].(string)
			out = append(out, id)
		}
		return out
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	enabled := &config.SDKConfig{}
	enabled.Client.NativeModelLists = true

	if got := ids(NativeProviderModels(enabled, models, "claude")); !equal(got, []string{claudeModel, sharedModel}) {
		t.Fatalf("claude list = %v, want the Claude and shared models", got)
	}
	if got := ids(NativeProviderModels(enabled, models, "codex")); !equal(got, []string{codexModel, sharedModel}) {
		t.Fatalf("codex list = %v, want the Codex and shared models", got)
	}
	if got := NativeProviderModels(&config.SDKConfig{}, models, "claude"); len(got) != len(models) {
		t.Fatalf("list with the setting off has %d models, want all %d", len(got), len(models))
	}
	if got := NativeProviderModels(nil, models, "claude"); len(got) != len(models) {
		t.Fatalf("list with no config has %d models, want all %d", len(got), len(models))
	}
}
