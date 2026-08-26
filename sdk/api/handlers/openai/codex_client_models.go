package openai

import (
	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func (h *OpenAIAPIHandler) codexClientModelsResponse() map[string]any {
	return h.codexClientModelsResponseForClient("")
}

func (h *OpenAIAPIHandler) codexClientModelsResponseForClient(clientVersion string) map[string]any {
	optimizeMultiAgentV2 := h != nil && h.Cfg != nil && h.Cfg.CodexOptimizeMultiAgentV2
	return codexmodels.BuildResponseForClient(h.Models(), registry.GetGlobalRegistry().GetModelProviders, optimizeMultiAgentV2, clientVersion)
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
