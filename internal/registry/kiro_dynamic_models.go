package registry

import (
	"strings"
	"sync"
	"sync/atomic"
)

// kiroDynamicDisabled mirrors the --local-model flag for Kiro. The flag lives in
// the command layer, which cannot reach the service, so it is recorded here for
// the live-catalog updater to consult.
var kiroDynamicDisabled atomic.Bool

// DisableKiroDynamicModels turns off live Kiro catalog discovery so the provider
// stays on its built-in definitions, matching --local-model semantics.
func DisableKiroDynamicModels() {
	kiroDynamicDisabled.Store(true)
}

// KiroDynamicModelsDisabled reports whether live Kiro catalog discovery is off.
func KiroDynamicModelsDisabled() bool {
	return kiroDynamicDisabled.Load()
}

// kiroDynamicModelStore holds the Kiro model catalog discovered at runtime from
// the account's own ListAvailableModels endpoint. Kiro publishes new models
// there before any static or remote catalog is updated, so the live catalog
// wins over both when it is available.
type kiroDynamicModelStore struct {
	mu     sync.RWMutex
	models []*ModelInfo
}

var kiroDynamicModels = &kiroDynamicModelStore{}

// SetKiroDynamicModels replaces the runtime Kiro catalog and reports whether the
// stored catalog changed. Passing an empty list clears the override so the
// remote and built-in catalogs take over again.
func SetKiroDynamicModels(models []*ModelInfo) bool {
	normalized := cloneModelInfosUnique(models)

	kiroDynamicModels.mu.Lock()
	defer kiroDynamicModels.mu.Unlock()

	if !modelSectionChanged(kiroDynamicModels.models, normalized) {
		return false
	}
	kiroDynamicModels.models = normalized
	return true
}

// KiroDynamicModels returns a copy of the runtime Kiro catalog, or nil when no
// live catalog has been discovered yet.
func KiroDynamicModels() []*ModelInfo {
	kiroDynamicModels.mu.RLock()
	defer kiroDynamicModels.mu.RUnlock()
	return cloneModelInfos(kiroDynamicModels.models)
}

// KiroModelCatalogEntry carries the subset of the live Kiro catalog needed to
// build registry model definitions, without importing the auth package.
type KiroModelCatalogEntry struct {
	ModelID         string
	ModelName       string
	Description     string
	MaxInputTokens  int
	MaxOutputTokens int
}

// BuildKiroModelInfos converts a live Kiro catalog into registry model
// definitions. The generic "kiro" and "kiro-auto" aliases are always kept so
// existing client configurations that target them keep working even though the
// upstream catalog only publishes "auto".
func BuildKiroModelInfos(entries []KiroModelCatalogEntry) []*ModelInfo {
	if len(entries) == 0 {
		return nil
	}

	models := make([]*ModelInfo, 0, len(entries)+2)
	var autoContext, autoCompletion int
	for _, entry := range entries {
		modelID := strings.TrimSpace(entry.ModelID)
		if modelID == "" {
			continue
		}
		contextLength := entry.MaxInputTokens
		completionTokens := entry.MaxOutputTokens
		if modelID == "auto" {
			autoContext = contextLength
			autoCompletion = completionTokens
		}
		models = append(models, &ModelInfo{
			ID:                  modelID,
			Object:              "model",
			OwnedBy:             "aws-kiro",
			Type:                "kiro",
			DisplayName:         kiroDisplayName(modelID, entry.ModelName),
			Description:         entry.Description,
			ContextLength:       contextLength,
			MaxCompletionTokens: completionTokens,
		})
	}
	if len(models) == 0 {
		return nil
	}

	for _, alias := range []struct {
		id          string
		displayName string
	}{
		{id: "kiro-auto", displayName: "Kiro Auto"},
		{id: "kiro", displayName: "Kiro AI"},
	} {
		models = append(models, &ModelInfo{
			ID:                  alias.id,
			Object:              "model",
			OwnedBy:             "aws-kiro",
			Type:                "kiro",
			DisplayName:         alias.displayName,
			ContextLength:       autoContext,
			MaxCompletionTokens: autoCompletion,
		})
	}
	return cloneModelInfosUnique(models)
}

// kiroDisplayName renders the catalog name in the same "(Kiro)"-suffixed style
// the built-in definitions use, so the UI stays consistent across sources.
func kiroDisplayName(modelID, modelName string) string {
	name := strings.TrimSpace(modelName)
	if name == "" {
		name = modelID
	}
	if modelID == "auto" {
		return "Auto (Kiro)"
	}
	return name + " (Kiro)"
}
