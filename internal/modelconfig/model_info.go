package modelconfig

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// ResolveModelInfo returns a private capability snapshot for a configured model.
// Static capabilities come from the suffix-free upstream name, while explicit
// configuration takes precedence.
//
// Capability absence is only authoritative when it is actually known. A model the
// bundled catalog does not carry, configured without an explicit thinking block,
// has *unknown* reasoning capability — not a proven absent one. Such a snapshot is
// marked user-defined so downstream thinking application forwards the caller's
// configuration and lets the upstream service validate it, instead of stripping
// generationConfig.thinkingConfig as it would for a catalog model documented to
// have no reasoning support. Without this distinction every model newer than the
// embedded catalog silently loses reasoning: the upstream still bills thinking
// tokens while returning no thought parts, because includeThoughts never arrives.
func ResolveModelInfo(name, modelType string, support *registry.ThinkingSupport) *registry.ModelInfo {
	trimmedName := strings.TrimSpace(name)
	baseName := strings.TrimSpace(thinking.ParseSuffix(trimmedName).ModelName)
	info := registry.LookupStaticModelInfo(baseName)
	catalogKnown := info != nil
	if info == nil {
		info = &registry.ModelInfo{}
	}
	info.ID = trimmedName
	info.Type = strings.TrimSpace(modelType)
	if support != nil {
		info.Thinking = NormalizeThinkingSupport(support)
	}
	info.UserDefined = !catalogKnown && support == nil
	return info
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
		switch level {
		case "none":
			normalized.ZeroAllowed = true
		case "auto":
			normalized.DynamicAllowed = true
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		normalized.Levels = append(normalized.Levels, level)
	}
	return &normalized
}
