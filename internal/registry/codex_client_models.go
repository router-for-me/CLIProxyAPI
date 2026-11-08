package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

//go:embed models/codex_client_models.json
var embeddedCodexClientModelsJSON []byte

type codexClientModelsPayload struct {
	Models []map[string]any `json:"models"`
}

type codexClientModelsStore struct {
	mu                sync.RWMutex
	data              []byte
	revision          uint64
	responsesLiteMap  map[string]bool
	thinkingLevelsMap map[string][]string
}

var codexClientCatalogStore = &codexClientModelsStore{}

func init() {
	if _, err := loadCodexClientModelsFromBytes(embeddedCodexClientModelsJSON, "embed"); err != nil {
		log.Warnf("registry: failed to parse embedded codex_client_models.json (Codex client catalog will remain unavailable until a valid remote refresh): %v", err)
	}
}

// GetCodexClientModelsJSON returns the current Codex client model catalog.
func GetCodexClientModelsJSON() []byte {
	data, _ := GetCodexClientModelsSnapshot()
	return data
}

// GetCodexClientModelsRevision returns the current revision of the Codex client model catalog.
func GetCodexClientModelsRevision() uint64 {
	codexClientCatalogStore.mu.RLock()
	defer codexClientCatalogStore.mu.RUnlock()
	return codexClientCatalogStore.revision
}

// GetCodexClientModelsSnapshot returns a consistent catalog copy and revision.
// The revision changes only when validated catalog content changes.
func GetCodexClientModelsSnapshot() ([]byte, uint64) {
	codexClientCatalogStore.mu.RLock()
	defer codexClientCatalogStore.mu.RUnlock()
	return append([]byte(nil), codexClientCatalogStore.data...), codexClientCatalogStore.revision
}

// CodexClientModelUsesResponsesLite reports whether slug is marked
// use_responses_lite in the active Codex client model catalog.
func CodexClientModelUsesResponsesLite(slug string) bool {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return false
	}
	codexClientCatalogStore.mu.RLock()
	defer codexClientCatalogStore.mu.RUnlock()
	return codexClientCatalogStore.responsesLiteMap[slug]
}

// CodexClientModelThinkingLevels returns a copy of the supported reasoning
// levels for slug in the active Codex client model catalog, or nil if absent.
func CodexClientModelThinkingLevels(slug string) []string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return nil
	}
	codexClientCatalogStore.mu.RLock()
	defer codexClientCatalogStore.mu.RUnlock()
	levels := codexClientCatalogStore.thinkingLevelsMap[slug]
	if len(levels) == 0 {
		return nil
	}
	return append([]string(nil), levels...)
}

func loadCodexClientModelsFromBytes(data []byte, source string) (bool, error) {
	responsesLiteMap, thinkingLevelsMap, err := parseAndValidateCodexClientModelsJSON(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", source, err)
	}

	cloned := append([]byte(nil), data...)
	codexClientCatalogStore.mu.Lock()
	defer codexClientCatalogStore.mu.Unlock()
	if bytes.Equal(codexClientCatalogStore.data, cloned) {
		return false, nil
	}
	codexClientCatalogStore.data = cloned
	codexClientCatalogStore.responsesLiteMap = responsesLiteMap
	codexClientCatalogStore.thinkingLevelsMap = thinkingLevelsMap
	codexClientCatalogStore.revision++
	return true, nil
}

// ValidateCodexClientModelsJSON validates the fields required to serve a
// complete Codex client model catalog.
func ValidateCodexClientModelsJSON(data []byte) error {
	_, _, err := parseAndValidateCodexClientModelsJSON(data)
	return err
}

func parseAndValidateCodexClientModelsJSON(data []byte) (map[string]bool, map[string][]string, error) {
	var payload codexClientModelsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, fmt.Errorf("decode Codex client model catalog: %w", err)
	}
	if len(payload.Models) == 0 {
		return nil, nil, fmt.Errorf("Codex client model catalog has no models")
	}

	seen := make(map[string]struct{}, len(payload.Models))
	responsesLiteMap := make(map[string]bool, len(payload.Models))
	thinkingLevelsMap := make(map[string][]string, len(payload.Models))
	for i, model := range payload.Models {
		slug, err := requiredCodexClientModelString(model, "slug")
		if err != nil {
			return nil, nil, fmt.Errorf("Codex client model catalog models[%d]: %w", i, err)
		}
		if _, exists := seen[slug]; exists {
			return nil, nil, fmt.Errorf("Codex client model catalog contains duplicate slug %q", slug)
		}
		seen[slug] = struct{}{}

		if err = validateCodexClientModel(model); err != nil {
			return nil, nil, fmt.Errorf("Codex client model catalog model %q: %w", slug, err)
		}
		lowerSlug := strings.ToLower(slug)
		if lite, ok := model["use_responses_lite"].(bool); ok && lite {
			responsesLiteMap[lowerSlug] = true
		}
		if rawLevels, ok := model["supported_reasoning_levels"].([]any); ok {
			levels := make([]string, 0, len(rawLevels))
			for _, rawLevel := range rawLevels {
				if entry, okMap := rawLevel.(map[string]any); okMap {
					if effort, okEffort := entry["effort"].(string); okEffort && strings.TrimSpace(effort) != "" {
						levels = append(levels, strings.TrimSpace(effort))
					}
				}
			}
			if len(levels) > 0 {
				thinkingLevelsMap[lowerSlug] = levels
			}
		}
	}
	return responsesLiteMap, thinkingLevelsMap, nil
}

func validateCodexClientModel(model map[string]any) error {
	for _, field := range []string{
		"display_name",
		"description",
		"base_instructions",
		"minimal_client_version",
		"visibility",
		"default_reasoning_level",
	} {
		if _, err := requiredCodexClientModelString(model, field); err != nil {
			return err
		}
	}

	contextWindow, err := requiredCodexClientModelInteger(model, "context_window", true)
	if err != nil {
		return err
	}
	maxContextWindow, err := requiredCodexClientModelInteger(model, "max_context_window", true)
	if err != nil {
		return err
	}
	if contextWindow > maxContextWindow {
		return fmt.Errorf("context_window %d exceeds max_context_window %d", contextWindow, maxContextWindow)
	}
	if _, err = requiredCodexClientModelInteger(model, "priority", false); err != nil {
		return err
	}

	levels, ok := model["supported_reasoning_levels"].([]any)
	if !ok || len(levels) == 0 {
		return fmt.Errorf("field %q must be a non-empty array", "supported_reasoning_levels")
	}
	seenLevels := make(map[string]struct{}, len(levels))
	for i, rawLevel := range levels {
		level, ok := rawLevel.(map[string]any)
		if !ok {
			return fmt.Errorf("field %q entry %d must be an object", "supported_reasoning_levels", i)
		}
		effort, errEffort := requiredCodexClientModelString(level, "effort")
		if errEffort != nil {
			return fmt.Errorf("field %q entry %d: %w", "supported_reasoning_levels", i, errEffort)
		}
		if _, exists := seenLevels[effort]; exists {
			return fmt.Errorf("field %q contains duplicate effort %q", "supported_reasoning_levels", effort)
		}
		seenLevels[effort] = struct{}{}
	}
	defaultLevel, _ := requiredCodexClientModelString(model, "default_reasoning_level")
	if _, ok = seenLevels[defaultLevel]; !ok {
		return fmt.Errorf("default_reasoning_level %q is not listed in supported_reasoning_levels", defaultLevel)
	}
	return nil
}

func requiredCodexClientModelString(model map[string]any, field string) (string, error) {
	value, ok := model[field].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("field %q must be a non-empty string", field)
	}
	return value, nil
}

func requiredCodexClientModelInteger(model map[string]any, field string, positive bool) (int64, error) {
	value, ok := model[field].(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value > math.MaxInt64 {
		return 0, fmt.Errorf("field %q must be an integer", field)
	}
	if positive && value <= 0 {
		return 0, fmt.Errorf("field %q must be positive", field)
	}
	if !positive && value < 0 {
		return 0, fmt.Errorf("field %q must not be negative", field)
	}
	return int64(value), nil
}
