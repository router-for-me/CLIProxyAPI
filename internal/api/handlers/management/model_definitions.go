package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// RefreshModelDefinitions triggers an immediate on-demand refresh of the model catalogs.
func (h *Handler) RefreshModelDefinitions(c *gin.Context) {
	var sources registry.CatalogSources
	home := false
	if h != nil {
		h.mu.Lock()
		if h.cfg != nil {
			sources = h.cfg.Models
			home = h.cfg.Home.Enabled
		}
		h.mu.Unlock()
	}

	if errValidate := sources.Validate(); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"ok":    false,
			"error": errValidate.Error(),
		})
		return
	}

	result, errRefresh := registry.RefreshModelCatalogsWithPolicy(c.Request.Context(), sources, home)
	if errRefresh != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"ok":                false,
			"error":             errRefresh.Error(),
			"catalog":           result.Catalog,
			"codex_catalog":     result.CodexCatalog,
			"devin_catalog":     result.DevinCatalog,
			"sources":           result.Sources,
			"catalogs":          result.Catalogs,
			"changed_providers": result.ChangedProviders,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":                true,
		"status":            "ok",
		"catalog":           result.Catalog,
		"codex_catalog":     result.CodexCatalog,
		"devin_catalog":     result.DevinCatalog,
		"sources":           result.Sources,
		"catalogs":          result.Catalogs,
		"changed_providers": result.ChangedProviders,
	})
}

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

	normalizedChannel := strings.ToLower(channel)
	models := registry.GetStaticModelDefinitionsByChannel(normalizedChannel)
	fromStatic := len(models) > 0
	fromPluginStatic := false
	if !fromStatic {
		models, fromPluginStatic = h.pluginModelDefinitionsByChannel(channel)
	}
	if (fromStatic || fromPluginStatic) && c.GetBool(ConfigV8ContextKey) && (normalizedChannel == "copilot" || normalizedChannel == "devin") {
		// Expose the same IDs used for OAuth rules before aliases and credential prefixes.
		prefix := normalizedChannel + "/"
		for _, model := range models {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.ID)
			if id != "" && !strings.HasPrefix(id, prefix) {
				model.ID = prefix + id
			}
		}
	}
	if len(models) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": normalizedChannel,
		"models":  models,
	})
}

func (h *Handler) pluginModelDefinitionsByChannel(channel string) ([]*registry.ModelInfo, bool) {
	var host *pluginhost.Host
	if h != nil {
		h.mu.Lock()
		host = h.pluginHost
		h.mu.Unlock()
	}
	if host != nil {
		if models := host.ModelsForProvider(channel); len(models) > 0 {
			return models, true
		}
	}
	models := registry.GetGlobalRegistry().GetAvailableModelsByProvider(channel)
	if len(models) > 1 {
		sort.Slice(models, func(i, j int) bool {
			if models[i] == nil {
				return models[j] != nil
			}
			if models[j] == nil {
				return false
			}
			return models[i].ID < models[j].ID
		})
	}
	return models, false
}
