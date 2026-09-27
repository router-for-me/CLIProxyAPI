package openai

import (
	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func (h *OpenAIAPIHandler) codexClientModelsResponse(clientVersion ...string) map[string]any {
	version := ""
	if len(clientVersion) > 0 {
		version = clientVersion[0]
	}
	optimizeMultiAgentV2 := h != nil && h.Cfg != nil && h.Cfg.CodexOptimizeMultiAgentV2
	var resolver codexmodels.SearchToolCapabilityForModelFunc
	if h != nil && h.BaseAPIHandler != nil && h.AuthManager != nil {
		manager := h.AuthManager
		if manager.ResponsesToolsEnabled() {
			resolver = func(modelID string) *bool { return manager.ClientSearchSupported(modelID) }
		}
	}
	return codexmodels.BuildResponseForClientWithToolCapabilities(h.Models(), registry.GetGlobalRegistry().GetModelProviders, registry.GetGlobalRegistry().GetResponsesWebSearchCapability, resolver, optimizeMultiAgentV2, version)
}

// CodexClientModelsResponse builds a Codex client model response.
func CodexClientModelsResponse(models []map[string]any) map[string]any {
	return codexmodels.BuildResponse(models, nil, false)
}

// CodexClientModelsResponseWithMultiAgentV2 builds a Codex client model response
// and advertises multi-agent v2 for synthesized models when enabled.
func CodexClientModelsResponseWithMultiAgentV2(models []map[string]any, enabled bool) map[string]any {
	return codexmodels.BuildResponse(models, nil, enabled)
}

// CodexClientModelsResponseForClient builds a Codex client model response
// tailored for a specific client version.
func CodexClientModelsResponseForClient(models []map[string]any, clientVersion string, enabled bool) map[string]any {
	return codexmodels.BuildResponseForClient(models, nil, enabled, clientVersion)
}

// CodexClientModelsResponseForClientWithSearchCapability builds a Codex client
// model response with the core client-search capability resolver. A nil
// resolver keeps legacy behavior.
func CodexClientModelsResponseForClientWithSearchCapability(models []map[string]any, clientVersion string, enabled bool, resolver func(string) *bool) map[string]any {
	return codexmodels.BuildResponseForClientWithToolCapabilities(models, registry.GetGlobalRegistry().GetModelProviders, registry.GetGlobalRegistry().GetResponsesWebSearchCapability, codexmodels.SearchToolCapabilityForModelFunc(resolver), enabled, clientVersion)
}
