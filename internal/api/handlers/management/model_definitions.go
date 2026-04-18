package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// GetStaticModelDefinitions returns static model metadata for a given channel.
// Channel is provided via path param (:channel) or query param (?channel=...).
func (h *Handler) GetStaticModelDefinitions(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Query("channel"))
	}
	if channel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required"})
		return
	}

	models := registry.GetStaticModelDefinitionsByChannel(channel)
	if models == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": strings.ToLower(strings.TrimSpace(channel)),
		"models":  models,
	})
}

// GetModelsHealth returns comprehensive health information for all registered models.
func (h *Handler) GetModelsHealth(c *gin.Context) {
	globalRegistry := registry.GetGlobalRegistry()

	// Get ALL registered models including suspended/unhealthy for full operator visibility
	models := globalRegistry.GetAllRegisteredModels("openai")
	// Build enhanced model health with suspension and provider info
	enhancedModels := make([]map[string]any, 0, len(models))
	for _, model := range models {
		modelID, _ := model["id"].(string)
		if modelID == "" {
			continue
		}
		// Get health details for this model
		providers, suspendedClients, count := globalRegistry.GetModelHealthDetails(modelID)
		// Build suspension summary
		suspensionSummary := make(map[string]any)
		if len(suspendedClients) > 0 {
			suspensionSummary["count"] = len(suspendedClients)
			suspensionSummary["clients"] = suspendedClients
			// Extract unique reasons
			reasons := make(map[string]bool)
			for _, reason := range suspendedClients {
				if reason == "" {
					reasons["unknown"] = true
				} else {
					reasons[strings.ToLower(reason)] = true
				}
			}
			reasonList := make([]string, 0, len(reasons))
			for reason := range reasons {
				reasonList = append(reasonList, reason)
			}
			suspensionSummary["reasons"] = reasonList
		} else {
			suspensionSummary["count"] = 0
			suspensionSummary["clients"] = map[string]string{}
			suspensionSummary["reasons"] = []string{}
		}

		// Build provider list
		providerList := make([]string, 0, len(providers))
		for provider := range providers {
			providerList = append(providerList, provider)
		}
		// Create enhanced model entry
		enhancedModel := make(map[string]any)
		for k, v := range model {
			enhancedModel[k] = v
		}
		enhancedModel["total_clients"] = count
		enhancedModel["providers"] = providerList
		enhancedModel["provider_counts"] = providers
		enhancedModel["suspension"] = suspensionSummary
		enhancedModels = append(enhancedModels, enhancedModel)
	}
	// Build the sources map indicating where context_length came from
	sources := registry.BuildModelSources(globalRegistry)
	// Get last refresh timestamp from OpenRouter enrichment
	lastRefresh := registry.GetOpenRouterLastRefresh()
	lastRefreshStr := ""
	if !lastRefresh.IsZero() {
		lastRefreshStr = lastRefresh.Format(time.RFC3339)
	}
	response := gin.H{
		"models":           enhancedModels,
		"sources":          sources,
		"last_refresh":     lastRefreshStr,
		"refresh_interval": "24h",
	}
	c.JSON(http.StatusOK, response)
}

// RefreshModels triggers an immediate refresh of model metadata from OpenRouter and the enrichment cache.
// This re-fetches context_length from OpenRouter's public /api/v1/models endpoint and enriches registered models that lack this data.
//
// TriggerOpenRouterRefresh returns the number of *newly* enriched models on this
// invocation. A zero return is a legitimate outcome — everything is already
// enriched, or no new models matched — and must not be reported as an error.
func (h *Handler) RefreshModels(c *gin.Context) {
	count := registry.TriggerOpenRouterRefresh(c.Request.Context())
	lastRefresh := registry.GetOpenRouterLastRefresh()
	c.JSON(http.StatusOK, gin.H{
		"status":         "refreshed",
		"enriched_count": count,
		"last_refresh":   lastRefresh.Format(time.RFC3339),
		"total_models":   len(registry.GetGlobalRegistry().GetAvailableModels("openai")),
	})
}
