package openai

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type codexClientModelsPayload struct {
	Models []map[string]any `json:"models"`
}

var (
	codexClientModelTemplatesOnce sync.Once
	codexClientModelTemplates     map[string]map[string]any
	codexClientDefaultTemplate    map[string]any
	codexClientModelTemplatesErr  error
)

func (h *OpenAIAPIHandler) codexClientModelsResponse() map[string]any {
	return CodexClientModelsResponse(h.Models())
}

func CodexClientModelsResponse(models []map[string]any) map[string]any {
	return map[string]any{
		"models": buildCodexClientModels(models),
	}
}

func buildCodexClientModels(models []map[string]any) []map[string]any {
	templates, defaultTemplate, err := loadCodexClientModelTemplates()
	if err != nil || defaultTemplate == nil {
		return nil
	}

	result := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(stringModelValue(model, "id"))
		if id == "" {
			continue
		}

		if template, ok := templates[id]; ok {
			entry := cloneCodexClientModelMap(template)
			applyCodexClientVisibilityOverride(entry, id)
			result = append(result, entry)
			continue
		}

		entry := cloneCodexClientModelMap(defaultTemplate)
		applyCodexClientModelMetadata(entry, id, model)
		applyCodexClientVisibilityOverride(entry, id)
		result = append(result, entry)
	}

	sort.SliceStable(result, func(i, j int) bool {
		return codexClientModelPriority(result[i]) < codexClientModelPriority(result[j])
	})

	return result
}

func loadCodexClientModelTemplates() (map[string]map[string]any, map[string]any, error) {
	codexClientModelTemplatesOnce.Do(func() {
		var payload codexClientModelsPayload
		codexClientModelTemplatesErr = json.Unmarshal(codexClientModelsJSON, &payload)
		if codexClientModelTemplatesErr != nil {
			return
		}

		codexClientModelTemplates = make(map[string]map[string]any, len(payload.Models))
		for _, model := range payload.Models {
			slug := strings.TrimSpace(stringModelValue(model, "slug"))
			if slug == "" {
				continue
			}
			codexClientModelTemplates[slug] = cloneCodexClientModelMap(model)
			if slug == "gpt-5.5" {
				codexClientDefaultTemplate = cloneCodexClientModelMap(model)
			}
		}
	})

	return codexClientModelTemplates, codexClientDefaultTemplate, codexClientModelTemplatesErr
}

func applyCodexClientModelMetadata(entry map[string]any, id string, model map[string]any) {
	info := registry.LookupModelInfo(id)

	displayName := stringModelValue(model, "display_name")
	description := stringModelValue(model, "description")
	contextWindow := intModelValue(model, "context_length")

	if info != nil {
		if info.DisplayName != "" {
			displayName = info.DisplayName
		}
		if info.Description != "" {
			description = info.Description
		}
		if info.ContextLength > 0 {
			contextWindow = info.ContextLength
		}
		applyCodexClientThinkingMetadata(entry, info.Thinking)
	}

	if displayName == "" {
		displayName = id
	}
	if description == "" {
		description = id
	}

	entry["slug"] = id
	entry["display_name"] = displayName
	entry["description"] = description
	entry["priority"] = 100
	entry["prefer_websockets"] = false
	delete(entry, "apply_patch_tool_type")

	if contextWindow > 0 {
		entry["context_window"] = contextWindow
		entry["max_context_window"] = contextWindow
	}

	if baseInstructions := stringModelValue(model, "base_instructions"); baseInstructions != "" {
		entry["base_instructions"] = baseInstructions
	}
	if plans, ok := model["available_in_plans"]; ok {
		entry["available_in_plans"] = cloneCodexClientModelValue(plans)
	}
}

func applyCodexClientVisibilityOverride(entry map[string]any, id string) {
	switch strings.TrimSpace(id) {
	case "grok-imagine-image-quality", "gpt-image-2", "grok-imagine-image", "grok-imagine-video":
		entry["visibility"] = "hide"
	}
}

func applyCodexClientThinkingMetadata(entry map[string]any, thinking *registry.ThinkingSupport) {
	if thinking == nil || len(thinking.Levels) == 0 {
		return
	}

	levels := make([]any, 0, len(thinking.Levels))
	defaultLevel := ""
	for _, rawLevel := range thinking.Levels {
		level := strings.ToLower(strings.TrimSpace(rawLevel))
		if level == "" || level == "none" {
			continue
		}
		if defaultLevel == "" || level == "medium" {
			defaultLevel = level
		}
		levels = append(levels, map[string]any{
			"effort":      level,
			"description": codexClientReasoningDescription(level),
		})
	}
	if len(levels) == 0 {
		return
	}

	entry["supported_reasoning_levels"] = levels
	entry["default_reasoning_level"] = defaultLevel
}

func codexClientReasoningDescription(level string) string {
	switch level {
	case "minimal":
		return "Fastest responses with minimal reasoning"
	case "low":
		return "Fast responses with lighter reasoning"
	case "medium":
		return "Balances speed and reasoning depth for everyday tasks"
	case "high":
		return "Greater reasoning depth for complex problems"
	case "xhigh":
		return "Extra high reasoning depth for complex problems"
	default:
		return level
	}
}

func codexClientModelPriority(model map[string]any) int {
	if priority, ok := model["priority"].(int); ok {
		return priority
	}
	if priority, ok := model["priority"].(float64); ok {
		return int(priority)
	}
	return 100
}

func stringModelValue(model map[string]any, key string) string {
	if model == nil {
		return ""
	}
	value, ok := model[key]
	if !ok {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func intModelValue(model map[string]any, key string) int {
	if model == nil {
		return 0
	}
	switch value := model[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func cloneCodexClientModelMap(model map[string]any) map[string]any {
	if model == nil {
		return nil
	}
	cloned := make(map[string]any, len(model))
	for key, value := range model {
		cloned[key] = cloneCodexClientModelValue(value)
	}
	return cloned
}

func cloneCodexClientModelValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneCodexClientModelMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, entry := range typed {
			cloned[i] = cloneCodexClientModelValue(entry)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
