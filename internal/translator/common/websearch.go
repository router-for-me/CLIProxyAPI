package common

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// IsClaudeWebSearchToolType reports whether a Claude tool type is a
// versioned server web_search declaration. Anthropic versions the type
// (web_search_20250305, web_search_20260209, ...), so matching is by prefix
// to accept future versions without per-path updates.
func IsClaudeWebSearchToolType(toolType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(toolType)), "web_search_")
}

// IsResponsesWebSearchToolType reports whether an OpenAI Responses tool
// type is a web search declaration, including versioned and preview
// aliases.
func IsResponsesWebSearchToolType(toolType string) bool {
	switch strings.TrimSpace(toolType) {
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		return true
	default:
		return false
	}
}

// GeminiModelSupportsWebSearch reports whether a model can run native
// googleSearch grounding, checking static catalog capabilities first
// (explicit false vetoes) and dynamic probe flags second.
func GeminiModelSupportsWebSearch(modelID string) bool {
	info := registry.LookupModelInfo(modelID)
	infoAG := registry.LookupModelInfo(modelID, "antigravity")

	// 1. Explicit false in static definitions acts as an absolute veto.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && !*info.NativeCapabilities.WebSearch {
		return false
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && !*infoAG.NativeCapabilities.WebSearch {
		return false
	}

	// 2. Explicit true in static definitions.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && *info.NativeCapabilities.WebSearch {
		return true
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && *infoAG.NativeCapabilities.WebSearch {
		return true
	}

	// 3. Dynamic capability checks via Antigravity probes and registry flags.
	if registry.AntigravityWebSearchModelFor(modelID) != "" {
		return true
	}
	if (info != nil && info.SupportsWebSearch) || (infoAG != nil && infoAG.SupportsWebSearch) {
		return true
	}
	return false
}
