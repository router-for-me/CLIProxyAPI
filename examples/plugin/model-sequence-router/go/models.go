package main

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (r *runtimeState) registeredModels() pluginapi.ModelRegistrationResponse {
	cfg := r.loadedConfig()
	response := pluginapi.ModelRegistrationResponse{Provider: pluginIdentifier}
	if cfg == nil || !cfg.Enabled {
		return response
	}
	response.Models = make([]pluginapi.ModelInfo, 0, len(cfg.Aliases))
	for _, alias := range cfg.Aliases {
		response.Models = append(response.Models, pluginapi.ModelInfo{
			ID:          alias.Alias,
			Name:        alias.Alias,
			Object:      "model",
			Created:     r.loadedAt.Unix(),
			OwnedBy:     pluginIdentifier,
			Type:        "model-sequence",
			DisplayName: alias.DisplayName,
			Description: "Per-conversation routed model sequence",
			UserDefined: true,
		})
	}
	return response
}

func stableLoadTime(now time.Time) time.Time {
	return now.Truncate(time.Second)
}
