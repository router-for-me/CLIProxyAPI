package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	log "github.com/sirupsen/logrus"
)

//go:embed models/devin_models.json
var embeddedDevinModelsJSON []byte

type devinModelsFilePayload struct {
	Devin  []*ModelInfo `json:"devin,omitempty"`
	Models []*ModelInfo `json:"models,omitempty"`
}

type devinModelsStore struct {
	mu       sync.RWMutex
	models   []*ModelInfo
	rawJSON  []byte
	revision uint64
}

var devinCatalogStore = &devinModelsStore{}

func init() {
	if _, err := loadDevinModelsFromBytes(embeddedDevinModelsJSON, "embed"); err != nil {
		log.Warnf("registry: failed to parse embedded devin_models.json (will rely on static fallback and remote refresh): %v", err)
	}
}

// GetDevinModels returns the active Devin model catalog.
// It prioritizes the dynamic/embedded devin_models.json catalog, then models.json's devin section,
// and finally hardcoded staticDevinModels.
func GetDevinModels() []*ModelInfo {
	devinCatalogStore.mu.RLock()
	models := devinCatalogStore.models
	devinCatalogStore.mu.RUnlock()

	if len(models) > 0 {
		return cloneModelInfos(models)
	}

	if m := getModels(); m != nil && len(m.Devin) > 0 {
		return cloneModelInfos(m.Devin)
	}

	return cloneModelInfos(staticDevinModels)
}

// GetDevinModelsJSON returns the current raw JSON payload of the Devin model catalog.
func GetDevinModelsJSON() []byte {
	data, _ := GetDevinModelsSnapshot()
	return data
}

// GetDevinModelsRevision returns the revision counter of the Devin model catalog.
func GetDevinModelsRevision() uint64 {
	devinCatalogStore.mu.RLock()
	defer devinCatalogStore.mu.RUnlock()
	return devinCatalogStore.revision
}

// GetDevinModelsSnapshot returns a copy of raw JSON and current catalog revision.
func GetDevinModelsSnapshot() ([]byte, uint64) {
	devinCatalogStore.mu.RLock()
	defer devinCatalogStore.mu.RUnlock()
	return append([]byte(nil), devinCatalogStore.rawJSON...), devinCatalogStore.revision
}

func loadDevinModelsFromBytes(data []byte, source string) (bool, error) {
	models, err := ValidateDevinModelsJSON(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", source, err)
	}

	clonedData := append([]byte(nil), data...)
	devinCatalogStore.mu.Lock()
	if bytes.Equal(devinCatalogStore.rawJSON, clonedData) {
		devinCatalogStore.mu.Unlock()
		return false, nil
	}
	devinCatalogStore.models = models
	devinCatalogStore.rawJSON = clonedData
	devinCatalogStore.revision++
	devinCatalogStore.mu.Unlock()

	return true, nil
}

// ValidateDevinModelsJSON parses and validates a Devin model catalog payload.
// Accepts {"devin": [...]}, {"models": [...]}, or a direct JSON array of ModelInfo.
func ValidateDevinModelsJSON(data []byte) ([]*ModelInfo, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("empty Devin models payload")
	}

	// 1. Try envelope {"devin": [...]} or {"models": [...]}
	var payload devinModelsFilePayload
	if err := json.Unmarshal(data, &payload); err == nil {
		candidates := payload.Devin
		if len(candidates) == 0 {
			candidates = payload.Models
		}
		if len(candidates) > 0 {
			return sanitizeAndValidateDevinModels(candidates)
		}
	}

	// 2. Try raw array []*ModelInfo
	var rawList []*ModelInfo
	if err := json.Unmarshal(data, &rawList); err == nil && len(rawList) > 0 {
		return sanitizeAndValidateDevinModels(rawList)
	}

	return nil, fmt.Errorf("invalid Devin models JSON: expected non-empty 'devin'/'models' array or model list")
}

func sanitizeAndValidateDevinModels(models []*ModelInfo) ([]*ModelInfo, error) {
	seen := make(map[string]struct{}, len(models))
	out := make([]*ModelInfo, 0, len(models))

	for i, m := range models {
		if m == nil {
			return nil, fmt.Errorf("model at index %d is null", i)
		}
		id := m.ID
		if id == "" {
			return nil, fmt.Errorf("model at index %d has empty id", i)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate model id: %q", id)
		}
		seen[id] = struct{}{}

		// Ensure proper default fields
		if m.Type == "" {
			m.Type = "devin"
		}
		if m.Object == "" {
			m.Object = "model"
		}
		if len(m.SupportedInputModalities) == 0 {
			m.SupportedInputModalities = []string{"text"}
		}
		if len(m.SupportedOutputModalities) == 0 {
			m.SupportedOutputModalities = []string{"text"}
		}
		out = append(out, m)
	}

	return out, nil
}
