package helps

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

var cursorRoutingCatalogs sync.Map

// StoreCursorRoutingModels retains the catalog after exclusions and before aliases,
// so family resolution cannot select an excluded upstream variant.
// Nil models is normalized to an empty slice representing an authoritative empty catalog
// (e.g. when an upstream fetch is empty or exclusions remove all models).
func StoreCursorRoutingModels(authID string, models []*registry.ModelInfo) {
	cursorRoutingCatalogs.Store(authID, cloneCursorRoutingModels(models))
}

// DeleteCursorRoutingModels removes the cached routing catalog for an auth
// during auth removal, disable, or test teardown.
func DeleteCursorRoutingModels(authID string) {
	cursorRoutingCatalogs.Delete(authID)
}

func CursorRoutingModels(authID string, fallback []*registry.ModelInfo) []*registry.ModelInfo {
	if models, ok := cursorRoutingCatalogs.Load(authID); ok {
		return cloneCursorRoutingModels(models.([]*registry.ModelInfo))
	}
	return fallback
}

func cloneCursorRoutingModels(models []*registry.ModelInfo) []*registry.ModelInfo {
	result := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model != nil {
			copy := *model
			result = append(result, &copy)
		}
	}
	return result
}
