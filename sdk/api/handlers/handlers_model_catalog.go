package handlers

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pluginModelCatalogFilterHost interface {
	FilterModelCatalog(ctx context.Context, req pluginapi.ModelCatalogFilterRequest) (pluginapi.ModelCatalogFilterResponse, error)
}

// FilterModelCatalog applies authenticated caller-specific catalog filters.
func (h *BaseAPIHandler) FilterModelCatalog(c *gin.Context, models []map[string]any, modelProviders map[string][]string) ([]map[string]any, error) {
	if h == nil || c == nil || h.PluginHost == nil {
		return models, nil
	}
	host, ok := h.PluginHost.(pluginModelCatalogFilterHost)
	if !ok {
		return models, nil
	}
	metadata := map[string]string{}
	if raw, exists := c.Get("accessMetadata"); exists {
		if values, valid := raw.(map[string]string); valid {
			for key, value := range values {
				metadata[key] = value
			}
		}
	}
	resp, err := host.FilterModelCatalog(c.Request.Context(), pluginapi.ModelCatalogFilterRequest{
		Method:         c.Request.Method,
		Path:           c.Request.URL.Path,
		Headers:        c.Request.Header.Clone(),
		Query:          c.Request.URL.Query(),
		AccessMetadata: metadata,
		Models:         models,
		ModelProviders: modelProviders,
	})
	if err != nil {
		return nil, fmt.Errorf("filter model catalog: %w", err)
	}
	if !resp.Handled {
		return models, nil
	}
	return resp.Models, nil
}
