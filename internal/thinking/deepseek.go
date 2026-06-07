package thinking

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// ReasoningEffortNormalization captures the post-route reasoning effort outcome.
type ReasoningEffortNormalization struct {
	Original   string
	Normalized string
	Stripped   bool
}

// NormalizeDeepSeekOfficialReasoningEffort normalizes the public reasoning effort
// vocabulary accepted by DeepSeek official compatibility endpoints.
func NormalizeDeepSeekOfficialReasoningEffort(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low", "medium":
		return "high"
	case "high":
		return "high"
	case "xhigh", "max":
		return "max"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}

// StripPublicModelHint removes a trailing public hint suffix such as "[1m]".
func StripPublicModelHint(model string) (baseModel string, hint string) {
	model = strings.TrimSpace(model)
	if model == "" || !strings.HasSuffix(model, "]") {
		return model, ""
	}
	start := strings.LastIndex(model, "[")
	if start <= 0 {
		return model, ""
	}
	hint = strings.TrimSpace(model[start+1 : len(model)-1])
	baseModel = strings.TrimSpace(model[:start])
	if hint == "" || baseModel == "" {
		return model, ""
	}
	return baseModel, hint
}

// IsDeepSeekReasoningIntentModel reports whether the requested public model should
// be treated as a DeepSeek strongest-reasoning intent alias.
func IsDeepSeekReasoningIntentModel(model string) bool {
	baseModel, _ := StripPublicModelHint(model)
	baseModel = strings.ToLower(strings.TrimSpace(ParseSuffix(baseModel).ModelName))
	return strings.HasPrefix(baseModel, "deepseek-v4-pro") || strings.HasPrefix(baseModel, "deepseek-v4-flash")
}

// ShouldNormalizeStrongestReasoningIntent reports whether the request should be
// normalized after route resolution instead of rejected locally.
func ShouldNormalizeStrongestReasoningIntent(requestedModel, clientProfile, original string) bool {
	original = strings.ToLower(strings.TrimSpace(original))
	if original == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(clientProfile), "claude_code") {
		return true
	}
	return IsDeepSeekReasoningIntentModel(requestedModel)
}

// StrongestSupportedReasoningEffort returns the strongest discrete effort level
// that the target model can accept in OpenAI-compatible form.
func StrongestSupportedReasoningEffort(support *registry.ThinkingSupport) string {
	if support == nil {
		return ""
	}
	for _, candidate := range []string{
		string(LevelMax),
		string(LevelXHigh),
		string(LevelHigh),
		string(LevelMedium),
		string(LevelLow),
		string(LevelMinimal),
	} {
		if HasLevel(support.Levels, candidate) {
			return candidate
		}
	}
	return ""
}

// NormalizeReasoningEffortForTarget normalizes a client-facing effort value
// after the final upstream model has been resolved.
func NormalizeReasoningEffortForTarget(original string, support *registry.ThinkingSupport, deepSeekOfficial bool) ReasoningEffortNormalization {
	normalizedOriginal := strings.ToLower(strings.TrimSpace(original))
	result := ReasoningEffortNormalization{Original: normalizedOriginal}
	if normalizedOriginal == "" {
		return result
	}

	if support == nil {
		result.Stripped = true
		return result
	}

	if deepSeekOfficial {
		result.Normalized = NormalizeDeepSeekOfficialReasoningEffort(normalizedOriginal)
		return result
	}

	switch normalizedOriginal {
	case "off", "disabled", string(LevelNone):
		if support.ZeroAllowed || HasLevel(support.Levels, string(LevelNone)) {
			result.Normalized = string(LevelNone)
			return result
		}
		result.Stripped = true
		return result
	case string(LevelAuto):
		if support.DynamicAllowed || HasLevel(support.Levels, string(LevelAuto)) {
			result.Normalized = string(LevelAuto)
			return result
		}
		if strongest := StrongestSupportedReasoningEffort(support); strongest != "" {
			result.Normalized = strongest
			return result
		}
		result.Stripped = true
		return result
	case string(LevelMax), string(LevelXHigh):
		if strongest := StrongestSupportedReasoningEffort(support); strongest != "" {
			result.Normalized = strongest
			return result
		}
		result.Stripped = true
		return result
	default:
		if HasLevel(support.Levels, normalizedOriginal) {
			result.Normalized = normalizedOriginal
		}
		return result
	}
}
