// Package models builds model catalogs for Anthropic clients.
package models

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

const claudeTranslatedModelPrefix = "claude/"

// BuildResponse builds an Anthropic model response from available models.
func BuildResponse(availableModels []map[string]any) map[string]any {
	models := make([]map[string]any, len(availableModels))
	for i, model := range availableModels {
		models[i] = cloneModel(model)
		if id, ok := models[i]["id"].(string); ok {
			models[i]["id"] = EnsureClaudeModelIDPrefix(id)
		}
	}

	sort.SliceStable(models, func(i, j int) bool {
		displayNameI, _ := models[i]["display_name"].(string)
		displayNameJ, _ := models[j]["display_name"].(string)
		if displayNameI != displayNameJ {
			return displayNameI < displayNameJ
		}
		idI, _ := models[i]["id"].(string)
		idJ, _ := models[j]["id"].(string)
		return idI < idJ
	})

	firstID := ""
	lastID := ""
	if len(models) > 0 {
		firstID, _ = models[0]["id"].(string)
		lastID, _ = models[len(models)-1]["id"].(string)
	}

	return map[string]any{
		"data":     models,
		"has_more": false,
		"first_id": firstID,
		"last_id":  lastID,
	}
}

// EnsureClaudeModelIDPrefix namespaces translated model IDs for Anthropic model listings.
// Catalog Claude and already-namespaced IDs are returned unchanged.
func EnsureClaudeModelIDPrefix(id string) string {
	if id == "" {
		return id
	}
	base := thinking.ParseSuffix(id).ModelName
	if registry.IsClaudeModelID(base) || strings.HasPrefix(id, claudeTranslatedModelPrefix) {
		return id
	}
	return claudeTranslatedModelPrefix + id
}

// ResolveClaudeModelIDPrefix removes every translated-model namespace layer for request routing.
// Catalog Claude IDs stop namespace removal, and thinking suffixes are preserved exactly.
func ResolveClaudeModelIDPrefix(id string) string {
	if id == "" {
		return id
	}

	base := thinking.ParseSuffix(id).ModelName
	if registry.IsClaudeModelID(base) {
		return id
	}

	resolved := base
	for strings.HasPrefix(resolved, claudeTranslatedModelPrefix) {
		resolved = strings.TrimPrefix(resolved, claudeTranslatedModelPrefix)
		if registry.IsClaudeModelID(resolved) {
			break
		}
	}
	if resolved == base {
		return id
	}
	return resolved + id[len(base):]
}

func cloneModel(model map[string]any) map[string]any {
	cloned := make(map[string]any, len(model))
	for key, value := range model {
		cloned[key] = value
	}
	return cloned
}
