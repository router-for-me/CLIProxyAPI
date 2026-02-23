package openai

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/thinking"
)

const (
	openAIChatEndpoint      = "/chat/completions"
	openAIResponsesEndpoint = "/responses"
)

func resolveEndpointOverride(modelName, requestedEndpoint string) (string, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", false
	}
	info := lookupModelInfoForEndpointOverride(modelName)
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

func lookupModelInfoForEndpointOverride(modelName string) *registry.ModelInfo {
	if info := registry.GetGlobalRegistry().GetModelInfo(modelName, ""); info != nil {
		return info
	}

	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel != "" && baseModel != modelName {
		if info := registry.GetGlobalRegistry().GetModelInfo(baseModel, ""); info != nil {
			return info
		}
	}

	providerPinnedModel := modelName
	if slash := strings.IndexByte(modelName, '/'); slash > 0 && slash+1 < len(modelName) {
		providerPinnedModel = strings.TrimSpace(modelName[slash+1:])
	}
	if providerPinnedModel != "" && providerPinnedModel != modelName {
		if info := registry.GetGlobalRegistry().GetModelInfo(providerPinnedModel, ""); info != nil {
			return info
		}
		if providerPinnedBase := strings.TrimSpace(thinking.ParseSuffix(providerPinnedModel).ModelName); providerPinnedBase != "" && providerPinnedBase != providerPinnedModel {
			if info := registry.GetGlobalRegistry().GetModelInfo(providerPinnedBase, ""); info != nil {
				return info
			}
		}
	}

	return nil
}

func endpointListContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
