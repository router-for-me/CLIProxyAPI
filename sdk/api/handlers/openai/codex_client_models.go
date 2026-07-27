package openai

import (
	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func (h *OpenAIAPIHandler) codexClientModelsResponse() map[string]any {
	optimizeMultiAgentV2 := h != nil && h.Cfg != nil && h.Cfg.CodexOptimizeMultiAgentV2
	return codexmodels.BuildResponse(h.Models(), registry.GetGlobalRegistry().GetModelProviders, optimizeMultiAgentV2)
}

// codexClientModelsResponseFiltered is the per-API-key variant of
// codexClientModelsResponse: it drops models the caller's resolved policy does
// not permit before building the Codex client catalog. Empty allowed/blocked
// lists (non-PG keys / no policy) preserve the full list.
func (h *OpenAIAPIHandler) codexClientModelsResponseFiltered(allowed, blocked []string) map[string]any {
	models := h.Models()
	if len(allowed) > 0 || len(blocked) > 0 {
		filtered := make([]map[string]any, 0, len(models))
		for _, m := range models {
			id, _ := m["id"].(string)
			if policy.ModelVisible(allowed, blocked, id) {
				filtered = append(filtered, m)
			}
		}
		models = filtered
	}
	optimizeMultiAgentV2 := h != nil && h.Cfg != nil && h.Cfg.CodexOptimizeMultiAgentV2
	return codexmodels.BuildResponse(models, registry.GetGlobalRegistry().GetModelProviders, optimizeMultiAgentV2)
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
