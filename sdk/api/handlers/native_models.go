package handlers

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// NativeProviderModels narrows a model list to the models served by the named
// provider when client.native-model-lists is enabled. It is used for the lists
// shown to native clients, so Claude Code is offered Claude models and Codex is
// offered Codex models instead of every model the proxy can route. The list is
// returned unchanged when the setting is off.
func NativeProviderModels(cfg *config.SDKConfig, models []map[string]any, provider string) []map[string]any {
	if cfg == nil || !cfg.Client.NativeModelLists {
		return models
	}
	modelRegistry := registry.GetGlobalRegistry()
	native := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		if id == "" {
			continue
		}
		for _, candidate := range modelRegistry.GetModelProviders(id) {
			if strings.EqualFold(candidate, provider) {
				native = append(native, model)
				break
			}
		}
	}
	return native
}
