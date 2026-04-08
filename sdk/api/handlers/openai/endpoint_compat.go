package openai

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

const (
	openAIChatEndpoint      = "/chat/completions"
	openAIResponsesEndpoint = "/responses"
)

func resolveEndpointOverride(modelName, requestedEndpoint string) (string, bool) {
	if modelName == "" {
		return "", false
	}
	info := registry.GetGlobalRegistry().GetModelInfo(modelName, "")
	if info == nil {
		baseModel := thinking.ParseSuffix(modelName).ModelName
		if baseModel != "" && baseModel != modelName {
			info = registry.GetGlobalRegistry().GetModelInfo(baseModel, "")
		}
	}
	if info == nil || len(info.SupportedEndpoints) == 0 {
		return "", false
	}
	if endpointListContains(info.SupportedEndpoints, requestedEndpoint) {
		return "", false
	}
	if requestedEndpoint == openAIChatEndpoint && endpointListContains(info.SupportedEndpoints, openAIResponsesEndpoint) {
		return openAIResponsesEndpoint, true
	}
	if requestedEndpoint == openAIResponsesEndpoint && endpointListContains(info.SupportedEndpoints, openAIChatEndpoint) {
		return openAIChatEndpoint, true
	}
	return "", false
}

func endpointListContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
