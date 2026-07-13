package executor

import (
	"regexp"
	"strings"

	windsurfmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/windsurf/models"
)

// mapOpenAIModelToWindsurf maps an OpenAI-style model ID (e.g. "devin/glm-5.2")
// to the backend Windsurf model UID.
func mapOpenAIModelToWindsurf(model string, cfg *windsurfmodels.Config) string {
	base := baseModelUID(model, cfg.DefaultModelID)
	return withReasoningVariant(base, model, cfg)
}

func baseModelUID(model, defaultModelID string) string {
	model = strings.TrimSpace(model)
	switch model {
	case "devin/glm-5.2":
		return "glm-5-2"
	case "devin/glm-5.1":
		return "glm-5-1"
	case "devin/default":
		return defaultModelID
	case "devin/gpt-5.5", "gpt-5.5":
		return "gpt-5-5"
	case "devin/gpt-5.4", "gpt-5.4":
		return "gpt-5-4"
	case "devin/gpt-5.4-mini", "gpt-5.4-mini":
		return "gpt-5-4-mini"
	case "devin/gpt-5.3-codex", "gpt-5.3-codex-spark":
		return "gpt-5-3-codex"
	case "devin/gpt-5.2":
		return "gpt-5-2"
	case "devin/claude-opus-4.8":
		return "claude-opus-4-8"
	case "devin/claude-fable-5":
		return "claude-5-fable"
	case "devin/claude-sonnet-5":
		return "claude-sonnet-5"
	case "devin/claude-opus-4.7":
		return "claude-opus-4-7"
	case "devin/claude-opus-4.6":
		return "claude-opus-4-6"
	case "devin/claude-opus-4.5":
		return "claude-opus-4.5"
	case "devin/claude-sonnet-4.6":
		return "claude-sonnet-4-6"
	case "devin/claude-sonnet-4.5":
		return "claude-sonnet-4.5"
	case "devin/claude-haiku-4.5":
		return "MODEL_PRIVATE_11"
	case "devin/gemini-3.5-flash":
		return "gemini-3-5-flash"
	case "devin/gemini-3-pro":
		return "gemini-3.1-pro"
	case "devin/gemini-3-flash":
		return "gemini-3.0-flash"
	case "devin/swe-1.6":
		return "swe-1-6"
	case "devin/swe-1.5":
		return "MODEL_SWE_1_5_SLOW"
	case "devin/kimi-k2.7":
		return "kimi-k2-7"
	case "devin/kimi-k2.6":
		return "kimi-k2-6"
	case "devin/deepseek-v4":
		return "deepseek-v4"
	}
	if strings.HasPrefix(model, "devin/") {
		return strings.TrimPrefix(model, "devin/")
	}
	return defaultModelID
}

// withReasoningVariant applies reasoning/service-tier variants. For the minimal
// implementation we keep the base UID. Full variant logic can be layered later.
func withReasoningVariant(base, model string, cfg *windsurfmodels.Config) string {
	if contains(cfg.ModelUIDs, base) {
		return base
	}
	// Try a few normalizations.
	candidates := []string{
		base,
		strings.ReplaceAll(base, ".", "-"),
		strings.Replace(base, "-(", "(", 1),
	}
	for _, c := range candidates {
		if contains(cfg.ModelUIDs, c) {
			return c
		}
	}
	return base
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func isReadableString(s string) bool {
	return regexp.MustCompile(`^[\x09\x0a\x0d\x20-\x7e]{2,}$`).MatchString(s)
}
