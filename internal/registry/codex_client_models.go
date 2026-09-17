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

// codexClientModelsDefaultTemplateSlug is the catalog entry the server serves for a
// model that has no catalog entry of its own, and the entry that stands in for the
// fields only a model itself supplies. The catalog must always carry it.
const codexClientModelsDefaultTemplateSlug = "gpt-5.5"

type codexClientModelsPayload struct {
	Models []map[string]any `json:"models"`
}

// codexClientModelsStore holds the base catalog fetched from the embedded file or a
// source URL together with the local override layer. The override is applied to the
// entries the server assembles for the models it can serve, not to the base catalog,
// so both are stored side by side: the base supplies the templates, the override
// shapes what each model serves.
type codexClientModelsStore struct {
	mu            sync.RWMutex
	base          []byte
	baseSource    string
	override      map[string]json.RawMessage
	overridePath  string
	overrideError string
	revision      uint64
}

var codexClientCatalogStore = &codexClientModelsStore{}

func init() {
	if _, err := setCodexClientModelsBase(embeddedCodexClientModelsJSON, "embed"); err != nil {
		log.Warnf("registry: failed to parse embedded codex_client_models.json (Codex client catalog will remain unavailable until a valid remote refresh): %v", err)
	}
}

// GetCodexClientModelsJSON returns the base Codex client model catalog, the template
// source the server assembles served entries from.
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

// GetCodexClientModelsSnapshot returns a consistent copy of the base catalog and the
// current revision. The revision changes whenever validated catalog content or the
// override layer changes, so callers may cache on it.
func GetCodexClientModelsSnapshot() ([]byte, uint64) {
	codexClientCatalogStore.mu.RLock()
	defer codexClientCatalogStore.mu.RUnlock()
	return append([]byte(nil), codexClientCatalogStore.base...), codexClientCatalogStore.revision
}

// setCodexClientModelsBase replaces the base catalog. The local override layer is
// applied to the assembled entries later, so the base only has to satisfy the catalog
// schema and is stored on its own.
func setCodexClientModelsBase(data []byte, source string) (bool, error) {
	if err := ValidateCodexClientModelsJSON(data); err != nil {
		return false, fmt.Errorf("%s: %w", source, err)
	}
	base := append([]byte(nil), data...)

	codexClientCatalogStore.mu.Lock()
	defer codexClientCatalogStore.mu.Unlock()
	if bytes.Equal(codexClientCatalogStore.base, base) {
		return false, nil
	}
	codexClientCatalogStore.base = base
	codexClientCatalogStore.baseSource = source
	codexClientCatalogStore.revision++
	return true, nil
}

// ValidateCodexClientModelsJSON validates the fields required to serve a
// complete Codex client model catalog.
func ValidateCodexClientModelsJSON(data []byte) error {
	var payload codexClientModelsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode Codex client model catalog: %w", err)
	}
	if len(payload.Models) == 0 {
		return fmt.Errorf("Codex client model catalog has no models")
	}

	seen := make(map[string]struct{}, len(payload.Models))
	for i, model := range payload.Models {
		slug, err := requiredCodexClientModelString(model, "slug")
		if err != nil {
			return fmt.Errorf("Codex client model catalog models[%d]: %w", i, err)
		}
		if _, exists := seen[slug]; exists {
			return fmt.Errorf("Codex client model catalog contains duplicate slug %q", slug)
		}
		seen[slug] = struct{}{}

		if err = validateCodexClientModel(model); err != nil {
			return fmt.Errorf("Codex client model catalog model %q: %w", slug, err)
		}
	}
	if _, ok := seen[codexClientModelsDefaultTemplateSlug]; !ok {
		return fmt.Errorf("Codex client model catalog is missing default template %q", codexClientModelsDefaultTemplateSlug)
	}
	return nil
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
	value, ok := codexClientModelIntegerValue(model[field])
	if !ok {
		return 0, fmt.Errorf("field %q must be an integer", field)
	}
	if positive && value <= 0 {
		return 0, fmt.Errorf("field %q must be positive", field)
	}
	if !positive && value < 0 {
		return 0, fmt.Errorf("field %q must not be negative", field)
	}
	return value, nil
}

// codexClientModelIntegerValue reads an integer field whatever numeric type carries it: a
// catalog decoded from JSON holds float64, while an entry the server assembled in memory
// holds int.
func codexClientModelIntegerValue(raw any) (int64, bool) {
	switch typed := raw.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}
