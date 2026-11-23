package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
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
	"-slow",
	"-priority",
	"-low-priority",
	"-medium-priority",
	"-high-priority",
	"-xhigh-priority",
	"-max-priority",
	"-low-fast",
	"-medium-fast",
	"-high-fast",
	"-xhigh-fast",
	"-max-fast",
	"-none-fast",
	"-thinking-1m",
	"-thinking",
	"-max-1m",
	"-none-1m",
	"-minimal-lightning",
	"-low-lightning",
	"-medium-lightning",
	"-high-lightning",
	"-xhigh-lightning",
	"-max-lightning",
	"-none-lightning",
	"_none",
	"_minimal",
	"_low",
	"_medium",
	"_high",
	"_xhigh",
	"_max",
	"_thinking",
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

// normalizeDevinThinkingLevel resolves an effort and reports whether it came from a budget.
func normalizeDevinThinkingLevel(level string, budgetTokens int) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "minimal", "low", "medium", "high", "xhigh", "max", "fast":
		return normalized, false
	case "none", "off", "disabled":
		return "none", false
	case "auto", "adaptive":
		return string(thinking.LevelAuto), false
	}

	if budgetTokens > 0 {
		return thinking.ConvertBudgetToLevel(budgetTokens)
	}

	return "", false
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
		thinkingLevel = strings.TrimSpace(parsedSuffix.RawSuffix)
		if mode, ok := thinking.ParseSpecialSuffix(thinkingLevel); ok {
			thinkingLevel = mode.String()
		} else if budget, ok := thinking.ParseNumericSuffix(thinkingLevel); ok {
			budgetTokens = budget
			if budget == 0 {
				thinkingLevel = "none"
			}
		}
	} else if colonIdx := strings.LastIndex(cleanModel, ":"); colonIdx != -1 {
		baseModel = strings.TrimSpace(cleanModel[:colonIdx])
		thinkingLevel = strings.TrimSpace(cleanModel[colonIdx+1:])
	}

	// 4. Normalize requested effort. Compound "<level>-fast" requests select upstream
	// fast-mode variants (see step 10); normalization cannot represent them,
	// so keep the raw level alongside the normalized effort.
	rawLevel := strings.ToLower(strings.TrimSpace(thinkingLevel))
	effort, derivedFromBudget := normalizeDevinThinkingLevel(thinkingLevel, budgetTokens)
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

	// 6. Resolve automatic effort against the catalog before choosing a wire UID.
	modelInfo := registry.LookupDevinModel(canonicalBase)
	if modelInfo == nil && canonicalBase != lowerBase {
		modelInfo = registry.LookupDevinModel(lowerBase)
	}
	if effort == string(thinking.LevelAuto) {
		effort = string(thinking.ClampLevel(thinking.LevelMedium, modelInfo, "devin"))
	}

	normalizedUnder := strings.ReplaceAll(canonicalBase, "-", "_")
	switch normalizedUnder {
	case "model_gpt_5_2":
		gptLevels := []string{"none", "low", "medium", "high", "xhigh"}
		eff := clampEffort(effort, gptLevels, "low", derivedFromBudget)
		return "MODEL_GPT_5_2_" + strings.ToUpper(eff)
	case "model_google_gemini_3_0_flash":
		gemLevels := []string{"minimal", "low", "medium", "high"}
		eff := clampEffort(effort, gemLevels, "high", derivedFromBudget)
		return "MODEL_GOOGLE_GEMINI_3_0_FLASH_" + strings.ToUpper(eff)
	case "model_claude_4_5_opus":
		if effort != "" && effort != "none" {
			return "MODEL_CLAUDE_4_5_OPUS_THINKING"
		}
		return "MODEL_CLAUDE_4_5_OPUS"
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
	case "glm-5-2-1m":
		if effort == "none" {
			return "glm-5-2-none-1m"
		}
		if effort == "max" {
			return "glm-5-2-max-1m"
		}
		return "glm-5-2-1m"
	case "claude-opus-4-6", "claude-sonnet-4-6":
		if effort != "" && effort != "none" {
			return canonicalBase + "-thinking"
		}
		return canonicalBase
	case "claude-opus-4-6-1m":
		if effort != "" && effort != "none" {
			return "claude-opus-4-6-thinking-1m"
		}
		return canonicalBase
	case "claude-sonnet-4-6-1m":
		if effort != "" && effort != "none" {
			return "claude-sonnet-4-6-thinking-1m"
		}
		return canonicalBase
	}

	// 8. Lightning tier: upstream UIDs keep the effort before the suffix
	// (swe-2-high-lightning), unlike every other model which appends effort.
	if strings.HasSuffix(canonicalBase, "-lightning") {
		stem := strings.TrimSuffix(canonicalBase, "-lightning")
		if HasDevinEffortSuffix(stem) {
			// Already a full upstream UID such as swe-2-high-lightning
			return canonicalBase
		}
		if len(allowedLevels) == 0 {
			return canonicalBase
		}
		defaultEffort := selectDefaultDevinEffort(stem, allowedLevels)
		clamped := clampEffort(effort, allowedLevels, defaultEffort, derivedFromBudget)
		return stem + "-" + clamped + "-lightning"
	}

	// 9. If model has no thinking levels defined in catalog, treat as bare model
	if len(allowedLevels) == 0 {
		return canonicalBase
	}

	// 10. Fast-mode variants: a strict "<level>-fast" request keeps its level
	// clamped to the catalog, never to the plain default.
	if base, isFast := strings.CutSuffix(rawLevel, "-fast"); isFast && devinLevelIndex(base) != -1 {
		defaultEffort := selectDefaultDevinEffort(canonicalBase, allowedLevels)
		clamped := clampEffort(base, allowedLevels, defaultEffort, false)
		return canonicalBase + "-" + clamped + "-fast"
	}

	// 11. Model has thinking levels: determine default effort and clamp
	defaultEffort := selectDefaultDevinEffort(canonicalBase, allowedLevels)
	clamped := clampEffort(effort, allowedLevels, defaultEffort, derivedFromBudget)
	if effort == "fast" {
		return canonicalBase + "-" + clamped + "-fast"
	}
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

func clampEffort(requested string, allowed []string, defaultEffort string, derivedFromBudget bool) string {
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
		// Keep Devin's higher-effort tie break for explicit requests. Budget
		// conversions use the canonical lower-effort tie break.
		preferOnTie := (derivedFromBudget && aIdx < bestIdx) || (!derivedFromBudget && aIdx > bestIdx)
		if dist < bestDist || (dist == bestDist && preferOnTie) {
			bestDist = dist
			bestMatch = a
			bestIdx = aIdx
		}
	}

	return bestMatch
}
