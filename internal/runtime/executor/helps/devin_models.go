package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// Family effort configurations extracted from GetCliModelConfigs (215 models).
var (
	swe2Efforts    = []string{"medium", "high", "max"}
	fable51Efforts = []string{"low", "medium", "high", "xhigh", "max"}
	astraEfforts   = []string{"low", "medium", "high", "xhigh", "max"}
	glm53Efforts   = []string{"low", "high", "max"}
)

// knownDevinSuffixes lists recognized model uid suffixes.
var knownDevinSuffixes = []string{
	"-low",
	"-medium",
	"-high",
	"-xhigh",
	"-max",
	"-fast",
	"-lightning",
	"-priority",
	"-low-priority",
	"-medium-priority",
	"-high-priority",
	"-xhigh-priority",
	"-max-priority",
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
	case "minimal", "low", "medium", "high", "xhigh", "max":
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
// It prioritizes already-formed UIDs, strips CPA suffixes, resolves body effort/budgets,
// and clamps efforts to the specific family whitelist.
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
	}

	// 3. Normalize requested effort
	effort := NormalizeThinkingLevel(thinkingLevel, budgetTokens)

	// 4. Family-specific mapping and clamping
	lowerBase := strings.ToLower(baseModel)
	switch {
	case strings.Contains(lowerBase, "swe-2"):
		clamped := clampEffort(effort, swe2Efforts, "high")
		return "swe-2-" + clamped

	case strings.Contains(lowerBase, "fable-5-1") || strings.Contains(lowerBase, "fable-5.1"):
		clamped := clampEffort(effort, fable51Efforts, "medium")
		return "claude-fable-5-1-" + clamped

	case strings.Contains(lowerBase, "fable-5") || strings.Contains(lowerBase, "5-fable"):
		clamped := clampEffort(effort, fable51Efforts, "medium")
		return "claude-5-fable-" + clamped

	case strings.Contains(lowerBase, "astra"):
		clamped := clampEffort(effort, astraEfforts, "medium")
		return "gpt-6-astra-" + clamped

	case strings.Contains(lowerBase, "glm-5-2") || strings.Contains(lowerBase, "glm-5.2"):
		if effort == "max" {
			return "glm-5-2-max"
		}
		if effort == "none" {
			return "glm-5-2-none"
		}
		return "glm-5-2"

	case strings.Contains(lowerBase, "glm-5-3") || strings.Contains(lowerBase, "glm-5.3"):
		clamped := clampEffort(effort, glm53Efforts, "high")
		return "glm-5-3-" + clamped

	default:
		// Default generic fallback: append effort if present, else return base
		if effort != "" && effort != "none" {
			return baseModel + "-" + effort
		}
		return baseModel
	}
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
	if requested == "" || requested == "none" {
		return defaultEffort
	}
	reqLower := strings.ToLower(strings.TrimSpace(requested))
	for _, a := range allowed {
		if reqLower == strings.ToLower(strings.TrimSpace(a)) {
			return a
		}
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
