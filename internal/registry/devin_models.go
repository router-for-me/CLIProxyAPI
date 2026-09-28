package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
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

const devinBuiltinSWE16SlowID = "devin/swe-1-6-slow"

func devinBuiltinSWE16SlowModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:                         devinBuiltinSWE16SlowID,
		Object:                     "model",
		Type:                       "devin",
		OwnedBy:                    "cognition",
		DisplayName:                "SWE-1.6 Slow",
		ContextLength:              200000,
		MaxCompletionTokens:        64000,
		InputTokenLimit:            200000,
		OutputTokenLimit:           64000,
		SupportedInputModalities:   []string{"text", "image"},
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent", "countTokens"},
	}
}

// WithDevinBuiltins injects hard-coded Devin model definitions that should
// not depend on remote or embedded devin_models.json updates. Built-ins replace
// any matching IDs already present in the provided slice.
func WithDevinBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, devinBuiltinSWE16SlowModelInfo())
}

// GetDevinModels returns the active Devin model catalog.
// It prioritizes the dynamic/embedded devin_models.json catalog, then models.json's devin section,
// and finally hardcoded staticDevinModels.
func GetDevinModels() []*ModelInfo {
	devinCatalogStore.mu.RLock()
	models := devinCatalogStore.models
	devinCatalogStore.mu.RUnlock()

	if len(models) > 0 {
		return WithDevinBuiltins(cloneModelInfos(models))
	}

	if m := getModels(); m != nil && len(m.Devin) > 0 {
		return WithDevinBuiltins(cloneModelInfos(m.Devin))
	}

	return WithDevinBuiltins(cloneModelInfos(staticDevinModels))
}

// LookupDevinModel looks up a model definition from the active Devin catalog.
// Accepts both namespaced ("devin/model") and bare ("model") IDs.
func LookupDevinModel(modelID string) *ModelInfo {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	clean = strings.TrimPrefix(clean, "devin/")
	if clean == "" {
		return nil
	}

	devinCatalogStore.mu.RLock()
	models := devinCatalogStore.models
	devinCatalogStore.mu.RUnlock()

	if len(models) == 0 {
		models = GetDevinModels()
	}

	for _, m := range models {
		mClean := strings.ToLower(strings.TrimPrefix(m.ID, "devin/"))
		if mClean == clean {
			return cloneModelInfo(m)
		}
	}
	for _, m := range WithDevinBuiltins(nil) {
		mClean := strings.ToLower(strings.TrimPrefix(m.ID, "devin/"))
		if mClean == clean {
			return cloneModelInfo(m)
		}
	}

	// Fallback: If clean is a thinking variant (e.g. claude-opus-5-low-fast or gpt-6-astra-high),
	// look up its base model (e.g. claude-opus-5 or gpt-6-astra).
	if base, _ := splitDevinModelID(clean); base != clean && base != "" {
		for _, m := range models {
			mClean := strings.ToLower(strings.TrimPrefix(m.ID, "devin/"))
			if mClean == base {
				return cloneModelInfo(m)
			}
		}
	}
	return nil
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

	models = WithDevinBuiltins(models)

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

var devinCompoundSuffixes = []struct {
	suffix string
	effort string
	readd  string
}{
	{suffix: "-low-fast", effort: "low"},
	{suffix: "-medium-fast", effort: "medium"},
	{suffix: "-high-fast", effort: "high"},
	{suffix: "-xhigh-fast", effort: "xhigh"},
	{suffix: "-max-fast", effort: "max"},
	{suffix: "-none-fast", effort: "none"},
	{suffix: "-low-priority", effort: "low"},
	{suffix: "-medium-priority", effort: "medium"},
	{suffix: "-high-priority", effort: "high"},
	{suffix: "-xhigh-priority", effort: "xhigh"},
	{suffix: "-max-priority", effort: "max"},
	{suffix: "-none-priority", effort: "none"},
	{suffix: "-thinking-1m", effort: "", readd: "-1m"},
	{suffix: "-thinking", effort: ""},
	{suffix: "-max-1m", effort: "max", readd: "-1m"},
	{suffix: "-none-1m", effort: "none", readd: "-1m"},
}

var devinSimpleEffortSuffixes = []struct {
	suffix string
	effort string
}{
	{suffix: "-none", effort: "none"},
	{suffix: "-minimal", effort: "minimal"},
	{suffix: "-low", effort: "low"},
	{suffix: "-medium", effort: "medium"},
	{suffix: "-high", effort: "high"},
	{suffix: "-xhigh", effort: "xhigh"},
	{suffix: "-max", effort: "max"},
}

var devinDisplayNameSuffixes = []string{
	" Low Fast", " Medium Fast", " High Fast", " XHigh Fast", " Max Fast",
	" Low Thinking Fast", " Medium Thinking Fast", " High Thinking Fast",
	" XHigh Thinking Fast", " Max Thinking Fast", " No Thinking Fast",
	" Low Thinking", " Medium Thinking", " High Thinking", " XHigh Thinking",
	" Max Thinking", " No Thinking",
	" Low", " Medium", " High", " XHigh", " Max", " None", " Minimal",
	" Thinking", " Fast",
}

var devinLevelOrder = map[string]int{
	"none":     0,
	"minimal":  1,
	"low":      2,
	"medium":   3,
	"high":     4,
	"xhigh":    5,
	"max":      6,
	"fast":     7,
	"priority": 8,
}

func splitDevinModelID(cleanID string) (string, string) {
	if cleanID == "swe-1-6-slow" {
		return cleanID, ""
	}
	if cleanID == "swe-1-6-fast" {
		return "swe-1-6", ""
	}

	upper := strings.ToUpper(cleanID)
	for _, s := range []struct {
		suffix string
		effort string
	}{
		{"_NONE", "none"},
		{"_MINIMAL", "minimal"},
		{"_LOW", "low"},
		{"_MEDIUM", "medium"},
		{"_HIGH", "high"},
		{"_XHIGH", "xhigh"},
		{"_MAX", "max"},
		{"_THINKING", "high"},
	} {
		if strings.HasSuffix(upper, s.suffix) {
			base := cleanID[:len(cleanID)-len(s.suffix)]
			return base, s.effort
		}
	}

	for _, s := range devinCompoundSuffixes {
		if strings.HasSuffix(cleanID, s.suffix) {
			base := cleanID[:len(cleanID)-len(s.suffix)]
			if s.readd != "" {
				base += s.readd
			}
			return base, s.effort
		}
	}

	for _, s := range devinSimpleEffortSuffixes {
		if strings.HasSuffix(cleanID, s.suffix) {
			base := cleanID[:len(cleanID)-len(s.suffix)]
			return base, s.effort
		}
	}

	return cleanID, ""
}

func cleanDevinDisplayName(name string) string {
	trimmed := strings.TrimSpace(name)
	for {
		changed := false
		for _, s := range devinDisplayNameSuffixes {
			if strings.HasSuffix(strings.ToLower(trimmed), strings.ToLower(s)) {
				trimmed = strings.TrimSpace(trimmed[:len(trimmed)-len(s)])
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	return trimmed
}

func aggregateDevinModels(models []*ModelInfo) []*ModelInfo {
	type aggEntry struct {
		model  *ModelInfo
		levels map[string]struct{}
	}

	order := make([]string, 0, len(models))
	aggregated := make(map[string]*aggEntry, len(models))

	for _, m := range models {
		if m == nil {
			continue
		}
		cleanID := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(m.ID), "devin/"))
		baseID, effort := splitDevinModelID(cleanID)
		if baseID == "" {
			baseID = cleanID
		}
		namespacedBase := "devin/" + baseID
		isBase := (baseID == cleanID)

		entry, exists := aggregated[namespacedBase]
		if !exists {
			clone := *m
			clone.ID = namespacedBase
			clone.DisplayName = cleanDevinDisplayName(m.DisplayName)
			if clone.DisplayName == "" {
				clone.DisplayName = m.DisplayName
			}
			entry = &aggEntry{
				model:  &clone,
				levels: make(map[string]struct{}),
			}
			aggregated[namespacedBase] = entry
			order = append(order, namespacedBase)
		}

		if isBase {
			if m.DisplayName != "" {
				entry.model.DisplayName = cleanDevinDisplayName(m.DisplayName)
			}
			if m.OwnedBy != "" {
				entry.model.OwnedBy = m.OwnedBy
			}
		}
		if m.ContextLength > entry.model.ContextLength {
			entry.model.ContextLength = m.ContextLength
		}
		if m.MaxCompletionTokens > entry.model.MaxCompletionTokens {
			entry.model.MaxCompletionTokens = m.MaxCompletionTokens
		}
		if m.InputTokenLimit > entry.model.InputTokenLimit {
			entry.model.InputTokenLimit = m.InputTokenLimit
		}
		if m.OutputTokenLimit > entry.model.OutputTokenLimit {
			entry.model.OutputTokenLimit = m.OutputTokenLimit
		}
		for _, mod := range m.SupportedInputModalities {
			if !slices.Contains(entry.model.SupportedInputModalities, mod) {
				entry.model.SupportedInputModalities = append(entry.model.SupportedInputModalities, mod)
			}
		}
		for _, mod := range m.SupportedOutputModalities {
			if !slices.Contains(entry.model.SupportedOutputModalities, mod) {
				entry.model.SupportedOutputModalities = append(entry.model.SupportedOutputModalities, mod)
			}
		}
		for _, gen := range m.SupportedGenerationMethods {
			if !slices.Contains(entry.model.SupportedGenerationMethods, gen) {
				entry.model.SupportedGenerationMethods = append(entry.model.SupportedGenerationMethods, gen)
			}
		}

		if m.Thinking != nil && len(m.Thinking.Levels) > 0 {
			for _, l := range m.Thinking.Levels {
				if l != "" && l != "priority" {
					entry.levels[l] = struct{}{}
				}
			}
		}
		if effort != "" && effort != "priority" {
			entry.levels[effort] = struct{}{}
		}
	}

	out := make([]*ModelInfo, 0, len(order))
	for _, id := range order {
		entry := aggregated[id]
		m := entry.model

		if len(entry.levels) > 0 {
			lvls := make([]string, 0, len(entry.levels))
			for l := range entry.levels {
				lvls = append(lvls, l)
			}
			sort.Slice(lvls, func(i, j int) bool {
				rI, okI := devinLevelOrder[lvls[i]]
				if !okI {
					rI = 99
				}
				rJ, okJ := devinLevelOrder[lvls[j]]
				if !okJ {
					rJ = 99
				}
				if rI != rJ {
					return rI < rJ
				}
				return lvls[i] < lvls[j]
			})
			m.Thinking = &ThinkingSupport{
				Levels: lvls,
			}
		}

		// Ensure defaults
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
		if m.InputTokenLimit == 0 && m.ContextLength > 0 {
			m.InputTokenLimit = m.ContextLength
		}
		if m.OutputTokenLimit == 0 && m.MaxCompletionTokens > 0 {
			m.OutputTokenLimit = m.MaxCompletionTokens
		}
		if len(m.SupportedGenerationMethods) == 0 {
			m.SupportedGenerationMethods = []string{"generateContent", "countTokens"}
		}

		out = append(out, m)
	}

	return out
}

func sanitizeAndValidateDevinModels(models []*ModelInfo) ([]*ModelInfo, error) {
	seenExact := make(map[string]struct{}, len(models))

	for i, m := range models {
		if m == nil {
			return nil, fmt.Errorf("model at index %d is null", i)
		}
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return nil, fmt.Errorf("model at index %d has empty id", i)
		}
		// Automatically namespace model IDs under devin/ if not already prefixed
		if !strings.HasPrefix(strings.ToLower(id), "devin/") {
			id = "devin/" + id
		}
		id = strings.ToLower(id)
		m.ID = id
		if _, exists := seenExact[id]; exists {
			return nil, fmt.Errorf("duplicate model id: %q", id)
		}
		seenExact[id] = struct{}{}
	}

	return aggregateDevinModels(models), nil
}
