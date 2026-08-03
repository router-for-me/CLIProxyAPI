package pluginhost

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// FilterModelCatalog chains active catalog filters in plugin priority order.
// Errors are returned to the endpoint so an authorization filter fails closed
// instead of accidentally exposing the unfiltered global catalog.
func (h *Host) FilterModelCatalog(ctx context.Context, req pluginapi.ModelCatalogFilterRequest) (pluginapi.ModelCatalogFilterResponse, error) {
	if h == nil {
		return pluginapi.ModelCatalogFilterResponse{Models: req.Models}, nil
	}
	current := cloneCatalogModels(req.Models)
	handled := false
	for _, record := range h.activeRecords() {
		filter := record.plugin.Capabilities.ModelCatalogFilter
		if filter == nil || h.isPluginFused(record.id) {
			continue
		}
		nextReq := req
		nextReq.Plugin = clonePluginMetadata(record.meta)
		nextReq.PluginID = record.id
		nextReq.Models = cloneCatalogModels(current)
		nextReq.ModelProviders = cloneModelProviders(req.ModelProviders)
		resp, err := filter.FilterModelCatalog(ctx, nextReq)
		if err != nil {
			return pluginapi.ModelCatalogFilterResponse{}, fmt.Errorf("model catalog filter %s: %w", record.id, err)
		}
		if !resp.Handled {
			continue
		}
		current = cloneCatalogModels(resp.Models)
		handled = true
	}
	return pluginapi.ModelCatalogFilterResponse{Handled: handled, Models: current}, nil
}

func cloneModelProviders(input map[string][]string) map[string][]string {
	if len(input) == 0 {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(input))
	for model, providers := range input {
		out[model] = append([]string(nil), providers...)
	}
	return out
}

func cloneCatalogModels(models []map[string]any) []map[string]any {
	if len(models) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		copyModel := make(map[string]any, len(model))
		for key, value := range model {
			copyModel[key] = value
		}
		out = append(out, copyModel)
	}
	return out
}
