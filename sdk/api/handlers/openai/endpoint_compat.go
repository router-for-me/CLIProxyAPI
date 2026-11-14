package openai

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
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
	if info == nil || len(info.Protocols) == 0 {
		return "", false
	}
	if endpointListContains(info.Protocols, endpointProtocol(requestedEndpoint)) {
		return "", false
	}
	if requestedEndpoint == openAIChatEndpoint && endpointListContains(info.Protocols, modelrouting.ProtocolOpenAIResponses) {
		return openAIResponsesEndpoint, true
	}
	if requestedEndpoint == openAIResponsesEndpoint && endpointListContains(info.Protocols, modelrouting.ProtocolOpenAIChat) {
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

func endpointProtocol(endpoint string) string {
	switch endpoint {
	case openAIChatEndpoint:
		return modelrouting.ProtocolOpenAIChat
	case openAIResponsesEndpoint:
		return modelrouting.ProtocolOpenAIResponses
	default:
		return ""
	}
}
