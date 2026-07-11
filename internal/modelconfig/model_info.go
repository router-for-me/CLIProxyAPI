package modelconfig

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// ResolveModelInfo returns a private model-info snapshot for a configured
// API-key model. Static capabilities are inherited from the suffix-free
// upstream model name and explicit configuration takes precedence.
func ResolveModelInfo(name, modelType string, support *registry.ThinkingSupport) *registry.ModelInfo {
	return ResolveModelInfoForProvider(name, modelType, modelType, support)
}

// ResolveModelInfoForProvider returns configured model metadata while keeping
// provider-scoped endpoint capabilities separate from the public model type.
func ResolveModelInfoForProvider(name, modelType, provider string, support *registry.ThinkingSupport) *registry.ModelInfo {
	trimmedName := strings.TrimSpace(name)
	baseName := strings.TrimSpace(thinking.ParseSuffix(trimmedName).ModelName)
	info := registry.LookupStaticModelInfo(baseName)
	if info == nil {
		info = &registry.ModelInfo{}
	}
	modelType = strings.TrimSpace(modelType)
	RebindModelInfo(info, baseName, trimmedName)
	info.Type = modelType
	// Endpoint capabilities are provider-specific; only native providers may
	// inherit them from their own static catalog entries.
	info.SupportsImageAPI, info.SupportsVideoAPI, info.ChatDisabled = staticEndpointCapabilities(baseName, provider)
	if support != nil {
		info.Thinking = NormalizeThinkingSupport(support)
	}
	info.UserDefined = false
	return info
}

// RebindModelInfo updates every public model identity field while preserving
// the remaining private metadata snapshot.
func RebindModelInfo(info *registry.ModelInfo, oldID, newID string) {
	if info == nil {
		return
	}
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if newID == "" {
		return
	}
	info.ID = newID
	info.Name = rebindResourceName(info.Name, oldID, newID)
}

func rebindResourceName(name, oldID, newID string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || oldID == "" || strings.EqualFold(oldID, newID) {
		return name
	}
	if strings.EqualFold(trimmed, oldID) {
		return newID
	}
	lowerName := strings.ToLower(trimmed)
	lowerSuffix := "/" + strings.ToLower(oldID)
	if strings.HasSuffix(lowerName, lowerSuffix) {
		return trimmed[:len(trimmed)-len(oldID)] + newID
	}
	return name
}

func staticEndpointCapabilities(modelName, provider string) (supportsImageAPI, supportsVideoAPI, chatDisabled bool) {
	channel := ""
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		channel = "codex"
	case "xai":
		channel = "xai"
	}
	if channel == "" {
		return false, false, false
	}
	info := registry.LookupStaticModelInfoByChannel(modelName, channel)
	if info != nil {
		return info.SupportsImageAPI, info.SupportsVideoAPI, info.ChatDisabled
	}
	return false, false, false
}

// NormalizeThinkingSupport clones and normalizes configured reasoning levels.
func NormalizeThinkingSupport(raw *registry.ThinkingSupport) *registry.ThinkingSupport {
	if raw == nil {
		return nil
	}
	normalized := *raw
	normalized.Levels = nil
	seen := make(map[string]struct{}, len(raw.Levels))
	for _, value := range raw.Levels {
		level := strings.ToLower(strings.TrimSpace(value))
		if level == "" {
			continue
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		normalized.Levels = append(normalized.Levels, level)
		switch level {
		case "none":
			normalized.ZeroAllowed = true
		case "auto":
			normalized.DynamicAllowed = true
		}
	}
	return &normalized
}

// NormalizeModalities returns unique lower-case configured modalities.
func NormalizeModalities(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
