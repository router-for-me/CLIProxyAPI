// Package modelinfo encodes DeepSeek web-client model knowledge: which model
// IDs are recognised, their type (default/expert/vision), thinking/search
// capability, no-thinking variants, and alias resolution. This is a pure,
// dependency-free port of the subset of ds2api/internal/config that the
// promptcompat layer needs; it intentionally carries no runtime config state.
package modelinfo

import "strings"

const noThinkingModelSuffix = "-nothinking"

// ModelAliasReader provides user-configured model alias overrides. Callers may
// pass nil when only the built-in alias table should be consulted.
type ModelAliasReader interface {
	ModelAliases() map[string]string
}

// GetModelConfig returns the (thinking, search, recognised) flags for a model.
// thinking is false for *-nothinking variants.
func GetModelConfig(model string) (thinking bool, search bool, ok bool) {
	baseModel, noThinking := splitNoThinkingModel(model)
	if baseModel == "" {
		return false, false, false
	}
	switch baseModel {
	case "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-vision":
		return !noThinking, false, true
	case "deepseek-v4-flash-search":
		return !noThinking, true, true
	default:
		return false, false, false
	}
}

// GetModelType returns the DeepSeek model type (default/expert/vision) and
// whether the model is recognised.
func GetModelType(model string) (modelType string, ok bool) {
	baseModel, _ := splitNoThinkingModel(model)
	switch baseModel {
	case "deepseek-v4-flash", "deepseek-v4-flash-search":
		return "default", true
	case "deepseek-v4-pro":
		return "expert", true
	case "deepseek-v4-vision":
		return "vision", true
	default:
		return "", false
	}
}

// IsSupportedDeepSeekModel reports whether model is a recognised DeepSeek model.
func IsSupportedDeepSeekModel(model string) bool {
	_, _, ok := GetModelConfig(model)
	return ok
}

// IsNoThinkingModel reports whether model is a *-nothinking variant.
func IsNoThinkingModel(model string) bool {
	_, noThinking := splitNoThinkingModel(model)
	return noThinking
}

// ResolveModel resolves a requested model name (alias or canonical) to its
// canonical DeepSeek model ID. store may be nil.
func ResolveModel(store ModelAliasReader, requested string) (string, bool) {
	model := lower(strings.TrimSpace(requested))
	if model == "" {
		return "", false
	}
	aliases := loadModelAliases(store)
	if IsSupportedDeepSeekModel(model) {
		return model, true
	}
	if mapped, ok := aliases[model]; ok && IsSupportedDeepSeekModel(mapped) {
		return mapped, true
	}
	baseModel, noThinking := splitNoThinkingModel(model)
	if mapped, ok := aliases[baseModel]; ok && IsSupportedDeepSeekModel(mapped) {
		return withNoThinkingVariant(mapped, noThinking), true
	}
	return "", false
}

// DefaultModelAliases returns the built-in alias table mapping common
// OpenAI/Anthropic/Google model names to DeepSeek equivalents.
func DefaultModelAliases() map[string]string {
	return map[string]string{
		// OpenAI GPT / ChatGPT families
		"chatgpt-4o":          "deepseek-v4-flash",
		"gpt-4":               "deepseek-v4-flash",
		"gpt-4-turbo":         "deepseek-v4-flash",
		"gpt-4-turbo-preview": "deepseek-v4-flash",
		"gpt-4.5-preview":     "deepseek-v4-flash",
		"gpt-4o":              "deepseek-v4-flash",
		"gpt-4o-mini":         "deepseek-v4-flash",
		"gpt-4.1":             "deepseek-v4-flash",
		"gpt-4.1-mini":        "deepseek-v4-flash",
		"gpt-4.1-nano":        "deepseek-v4-flash",
		"gpt-5":               "deepseek-v4-flash",
		"gpt-5-chat":          "deepseek-v4-flash",
		"gpt-5.1":             "deepseek-v4-flash",
		"gpt-5.1-chat":        "deepseek-v4-flash",
		"gpt-5.2":             "deepseek-v4-flash",
		"gpt-5.2-chat":        "deepseek-v4-flash",
		"gpt-5.3-chat":        "deepseek-v4-flash",
		"gpt-5.4":             "deepseek-v4-flash",
		"gpt-5.5":             "deepseek-v4-flash",
		"gpt-5-mini":          "deepseek-v4-flash",
		"gpt-5-nano":          "deepseek-v4-flash",
		"gpt-5.4-mini":        "deepseek-v4-flash",
		"gpt-5.4-nano":        "deepseek-v4-flash",
		"gpt-5-pro":           "deepseek-v4-pro",
		"gpt-5.2-pro":         "deepseek-v4-pro",
		"gpt-5.4-pro":         "deepseek-v4-pro",
		"gpt-5.5-pro":         "deepseek-v4-pro",
		"gpt-5-codex":         "deepseek-v4-pro",
		"gpt-5.1-codex":       "deepseek-v4-pro",
		"gpt-5.1-codex-mini":  "deepseek-v4-pro",
		"gpt-5.1-codex-max":   "deepseek-v4-pro",
		"gpt-5.2-codex":       "deepseek-v4-pro",
		"gpt-5.3-codex":       "deepseek-v4-pro",
		"codex-mini-latest":   "deepseek-v4-pro",

		// OpenAI reasoning / research families
		"o1":                    "deepseek-v4-pro",
		"o1-preview":            "deepseek-v4-pro",
		"o1-mini":               "deepseek-v4-pro",
		"o1-pro":                "deepseek-v4-pro",
		"o3":                    "deepseek-v4-pro",
		"o3-mini":               "deepseek-v4-pro",
		"o3-pro":                "deepseek-v4-pro",
		"o3-deep-research":      "deepseek-v4-flash-search",
		"o4-mini":               "deepseek-v4-pro",
		"o4-mini-deep-research": "deepseek-v4-flash-search",

		// Claude current and historical aliases
		"claude-opus-4-6":            "deepseek-v4-pro",
		"claude-opus-4-1":            "deepseek-v4-pro",
		"claude-opus-4-1-20250805":   "deepseek-v4-pro",
		"claude-opus-4-0":            "deepseek-v4-pro",
		"claude-opus-4-20250514":     "deepseek-v4-pro",
		"claude-sonnet-4-6":          "deepseek-v4-flash",
		"claude-sonnet-4-5":          "deepseek-v4-flash",
		"claude-sonnet-4-5-20250929": "deepseek-v4-flash",
		"claude-sonnet-4-0":          "deepseek-v4-flash",
		"claude-sonnet-4-20250514":   "deepseek-v4-flash",
		"claude-haiku-4-5":           "deepseek-v4-flash",
		"claude-haiku-4-5-20251001":  "deepseek-v4-flash",
		"claude-3-7-sonnet":          "deepseek-v4-flash",
		"claude-3-7-sonnet-latest":   "deepseek-v4-flash",
		"claude-3-7-sonnet-20250219": "deepseek-v4-flash",
		"claude-3-5-sonnet":          "deepseek-v4-flash",
		"claude-3-5-sonnet-latest":   "deepseek-v4-flash",
		"claude-3-5-sonnet-20240620": "deepseek-v4-flash",
		"claude-3-5-sonnet-20241022": "deepseek-v4-flash",
		"claude-3-5-haiku":           "deepseek-v4-flash",
		"claude-3-5-haiku-latest":    "deepseek-v4-flash",
		"claude-3-5-haiku-20241022":  "deepseek-v4-flash",
		"claude-3-opus":              "deepseek-v4-pro",
		"claude-3-opus-20240229":     "deepseek-v4-pro",
		"claude-3-sonnet":            "deepseek-v4-flash",
		"claude-3-sonnet-20240229":   "deepseek-v4-flash",
		"claude-3-haiku":             "deepseek-v4-flash",
		"claude-3-haiku-20240307":    "deepseek-v4-flash",

		// Gemini current and historical text / multimodal models
		"gemini-pro":            "deepseek-v4-pro",
		"gemini-pro-vision":     "deepseek-v4-vision",
		"gemini-pro-latest":     "deepseek-v4-pro",
		"gemini-flash-latest":   "deepseek-v4-flash",
		"gemini-1.5-pro":        "deepseek-v4-pro",
		"gemini-1.5-flash":      "deepseek-v4-flash",
		"gemini-1.5-flash-8b":   "deepseek-v4-flash",
		"gemini-2.0-flash":      "deepseek-v4-flash",
		"gemini-2.0-flash-lite": "deepseek-v4-flash",
		"gemini-2.5-pro":        "deepseek-v4-pro",
		"gemini-2.5-flash":      "deepseek-v4-flash",
		"gemini-2.5-flash-lite": "deepseek-v4-flash",
		"gemini-3.1-pro":        "deepseek-v4-pro",
		"gemini-3-pro":          "deepseek-v4-pro",
		"gemini-3-flash":        "deepseek-v4-flash",
		"gemini-3.1-flash":      "deepseek-v4-flash",
		"gemini-3.1-flash-lite": "deepseek-v4-flash",

		"llama-3.1-70b-instruct": "deepseek-v4-flash",
		"qwen-max":               "deepseek-v4-flash",
	}
}

func splitNoThinkingModel(model string) (string, bool) {
	model = lower(strings.TrimSpace(model))
	if strings.HasSuffix(model, noThinkingModelSuffix) {
		return strings.TrimSuffix(model, noThinkingModelSuffix), true
	}
	return model, false
}

func withNoThinkingVariant(model string, enabled bool) string {
	baseModel, _ := splitNoThinkingModel(model)
	if !enabled {
		return baseModel
	}
	if baseModel == "" {
		return ""
	}
	return baseModel + noThinkingModelSuffix
}

func loadModelAliases(store ModelAliasReader) map[string]string {
	aliases := DefaultModelAliases()
	if store != nil {
		for k, v := range store.ModelAliases() {
			aliases[lower(strings.TrimSpace(k))] = lower(strings.TrimSpace(v))
		}
	}
	return aliases
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
