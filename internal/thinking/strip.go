// Package thinking provides unified thinking configuration processing.
package thinking

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// StripThinkingConfig removes thinking configuration fields from request body.
//
// This function is used when a model doesn't support thinking but the request
// contains thinking configuration. The configuration is silently removed to
// prevent upstream API errors.
//
// Parameters:
//   - body: Original request body JSON
//   - provider: Provider name (determines which fields to strip)
//
// Returns:
//   - Modified request body JSON with thinking configuration removed
//   - Original body is returned unchanged if:
//   - body is empty or invalid JSON
//   - provider is unknown
//   - no thinking configuration found
func StripThinkingConfig(body []byte, provider string) []byte {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !stripThinkingFieldsDoc(doc, provider) {
		return body
	}
	return doc.Bytes()
}

// StripThinkingFieldsDoc removes thinking configuration fields from doc in place.
func StripThinkingFieldsDoc(doc *ojson.Doc, format string, keepTopLevelEffort ...bool) {
	_ = stripThinkingFieldsDoc(doc, format, keepTopLevelEffort...)
}

func stripThinkingFieldsDoc(doc *ojson.Doc, format string, keepTopLevelEffort ...bool) bool {
	if doc == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(format))
	if provider == "openai-response" {
		provider = "codex"
	}
	keepEffort := len(keepTopLevelEffort) > 0 && keepTopLevelEffort[0]

	var paths []string
	switch provider {
	case "claude":
		if keepEffort {
			paths = []string{"thinking"}
		} else {
			paths = []string{"thinking", "output_config.effort"}
		}
	case "gemini":
		paths = []string{"generationConfig.thinkingConfig"}
	case "antigravity":
		paths = []string{"request.generationConfig.thinkingConfig"}
	case "interactions":
		paths = []string{
			"generation_config.thinking_level",
			"generation_config.thinkingLevel",
			"generation_config.thinking_budget",
			"generation_config.thinkingBudget",
			"generation_config.thinking_summaries",
			"generation_config.thinkingSummaries",
			"generation_config.thinking_config",
			"generation_config.thinkingConfig",
		}
	case "openai":
		if keepEffort {
			paths = []string{"reasoning"}
		} else {
			paths = []string{"reasoning_effort", "reasoning"}
		}
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		if keepEffort {
			paths = []string{"thinking"}
		} else {
			paths = []string{"reasoning_effort", "thinking"}
		}
	case "codex", "xai":
		if !keepEffort {
			paths = []string{"reasoning"}
		}
	default:
		return false
	}

	modified := false
	for _, path := range paths {
		if doc.Get(path).Exists() {
			if doc.Delete(path) == nil {
				modified = true
			}
		}
	}

	// Avoid leaving an empty output_config object for Claude when effort was the only field.
	if provider == "claude" && !keepEffort {
		if oc := doc.Get("output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			if util.DeleteJSONDocIfExists(doc, "output_config") == nil {
				modified = true
			}
		}
	}
	return modified
}
