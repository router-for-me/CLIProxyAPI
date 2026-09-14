package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// knownDevinSuffixes lists recognized model uid suffixes.
var knownDevinSuffixes = []string{
	"-none",
	"-low",
	"-medium",
	"-high",
	"-xhigh",
	"-max",
	"-fast",
	"-priority",
	"-low-priority",
	"-medium-priority",
	"-high-priority",
	"-xhigh-priority",
	"-max-priority",
}

// Special private Devin upstream aliases that cannot be dynamically inferred.
var specialDevinAliases = map[string]string{
	"claude-haiku-4-5": "MODEL_PRIVATE_11",
	"gpt-4-1":          "MODEL_CHAT_GPT_4_1_2025_04_14",
}

// HasDevinEffortSuffix reports whether the model name already ends with a known Devin effort suffix.
func HasDevinEffortSuffix(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, s := range knownDevinSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// NormalizeThinkingLevel converts numeric budgets or loose effort strings to canonical Devin efforts.
func NormalizeThinkingLevel(level string, budgetTokens int) string {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "minimal", "low", "medium", "high", "xhigh", "max", "fast":
		return normalized
	case "none", "off", "disabled":
		return "none"
	case "auto", "adaptive":
		return "high"
	}

	if budgetTokens > 0 {
		switch {
		case budgetTokens <= 4096:
			return "low"
		case budgetTokens <= 16384:
			return "medium"
		case budgetTokens <= 32768:
			return "high"
		default:
			return "max"
		}
	}

	return ""
}

// ResolveDevinChatModelUID resolves a model identifier into a valid upstream Devin chat_model_uid.
// It prioritizes dynamic catalog metadata from devin_models.json, automatically clamps
// requested efforts to supported levels, applies sensible default efforts for thinking models,
// and ensures bare non-thinking models remain bare.
func ResolveDevinChatModelUID(rawModel string, thinkingLevel string, budgetTokens int) string {
	model := strings.TrimSpace(rawModel)
	if model == "" {
		return "swe-2-high"
	}

	// 1. Strip devin/ prefix if present (case-insensitive)
	cleanModel := model
	if strings.HasPrefix(strings.ToLower(cleanModel), "devin/") {
		cleanModel = cleanModel[6:]
	}

	// 2. If already ends with an exact Devin effort suffix, use directly
	if HasDevinEffortSuffix(cleanModel) {
		return cleanModel
	}

	// 3. Strip CPA colon or parenthesis suffix (suffix overrides body per CPA convention)
	parsedSuffix := thinking.ParseSuffix(cleanModel)
	baseModel := strings.TrimSpace(parsedSuffix.ModelName)
	if parsedSuffix.HasSuffix {
		thinkingLevel = parsedSuffix.RawSuffix
	} else if colonIdx := strings.LastIndex(cleanModel, ":"); colonIdx != -1 {
		baseModel = strings.TrimSpace(cleanModel[:colonIdx])
		thinkingLevel = strings.TrimSpace(cleanModel[colonIdx+1:])
	}

	// 4. Normalize requested effort
	effort := NormalizeThinkingLevel(thinkingLevel, budgetTokens)
	lowerBase := strings.ToLower(baseModel)
	canonicalBase := strings.ReplaceAll(lowerBase, ".", "-")

	// 5. Check special private upstream aliases
	if alias, exists := specialDevinAliases[canonicalBase]; exists {
		return alias
	}
	if canonicalBase == "claude-sonnet-4-5" || strings.Contains(canonicalBase, "sonnet-4-5") {
		if effort != "" && effort != "none" {
			return "MODEL_PRIVATE_3"
		}
		return "MODEL_PRIVATE_2"
	}
	if canonicalBase == "gemini-3-flash" {
		canonicalBase = "gemini-3-8-flash"
	}

	// 6. Look up dynamic model metadata from the Devin catalog (devin_models.json)
	modelInfo := registry.LookupDevinModel(canonicalBase)
	if modelInfo == nil && canonicalBase != lowerBase {
		modelInfo = registry.LookupDevinModel(lowerBase)
	}

	var allowedLevels []string
	if modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0 {
		allowedLevels = modelInfo.Thinking.Levels
	}

	// 7. Special base models that default to bare name unless specific variant requested
	switch canonicalBase {
	case "swe-1-7":
		if effort == "medium" {
			return "swe-1-7-medium"
		}
		return "swe-1-7"
	case "swe-1-6":
		if effort == "fast" {
			return "swe-1-6-fast"
		}
		return "swe-1-6"
	case "glm-5-2":
		if effort == "none" {
			return "glm-5-2-none"
		}
		if effort == "max" {
			return "glm-5-2-max"
		}
		return "glm-5-2"
	}

	// 8. If model has no thinking levels defined in catalog, treat as bare model
	if len(allowedLevels) == 0 {
		return canonicalBase
	}

	// 9. Model has thinking levels: determine default effort and clamp
	defaultEffort := selectDefaultDevinEffort(canonicalBase, allowedLevels)
	clamped := clampEffort(effort, allowedLevels, defaultEffort)
	return canonicalBase + "-" + clamped
}

func selectDefaultDevinEffort(baseModel string, levels []string) string {
	if strings.Contains(baseModel, "swe-2") {
		return "high"
	}
	hasNone := false
	hasLow := false
	hasMedium := false
	hasHigh := false
	for _, l := range levels {
		switch l {
		case "none":
			hasNone = true
		case "low":
			hasLow = true
		case "medium":
			hasMedium = true
		case "high":
			hasHigh = true
		}
	}
	// For OpenAI GPT-5.x families in Devin CLI (which support "none" and "low"), default to low
	if hasNone && hasLow && strings.HasPrefix(baseModel, "gpt-5") {
		return "low"
	}
	// For models with high thinking (e.g. deepseek, gemini, grok, glm, kimi, nemotron), prefer high
	if hasHigh && (strings.Contains(baseModel, "gemini") ||
		strings.Contains(baseModel, "grok") ||
		strings.Contains(baseModel, "glm") ||
		strings.Contains(baseModel, "deepseek") ||
		strings.Contains(baseModel, "kimi") ||
		strings.Contains(baseModel, "nemotron")) {
		return "high"
	}
	if hasMedium {
		return "medium"
	}
	if hasHigh {
		return "high"
	}
	if hasLow {
		return "low"
	}
	return levels[0]
}

var devinStandardLevelOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func devinLevelIndex(level string) int {
	lower := strings.ToLower(strings.TrimSpace(level))
	for i, l := range devinStandardLevelOrder {
		if l == lower {
			return i
		}
	}
	return -1
}

func clampEffort(requested string, allowed []string, defaultEffort string) string {
	if requested == "" {
		return defaultEffort
	}
	reqLower := strings.ToLower(strings.TrimSpace(requested))
	for _, a := range allowed {
		if reqLower == strings.ToLower(strings.TrimSpace(a)) {
			return a
		}
	}
	if reqLower == "none" {
		return defaultEffort
	}

	reqIdx := devinLevelIndex(reqLower)
	if reqIdx == -1 {
		return defaultEffort
	}

	bestMatch := defaultEffort
	bestDist := 999
	bestIdx := -1
	for _, a := range allowed {
		aIdx := devinLevelIndex(a)
		if aIdx == -1 {
			continue
		}
		dist := reqIdx - aIdx
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist = dist
			bestMatch = a
			bestIdx = aIdx
		} else if dist == bestDist && aIdx > bestIdx {
			// On tie, prefer the higher effort (e.g. medium -> high for glm-5-3; xhigh -> max for swe-2)
			bestMatch = a
			bestIdx = aIdx
		}
	}

	return bestMatch
}
