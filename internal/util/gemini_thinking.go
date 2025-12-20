package util

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	GeminiThinkingBudgetMetadataKey  = "gemini_thinking_budget"
	GeminiIncludeThoughtsMetadataKey = "gemini_include_thoughts"
	GeminiOriginalModelMetadataKey   = "gemini_original_model"
)

// Gemini model family detection patterns
var (
	gemini3Pattern      = regexp.MustCompile(`(?i)^gemini[_-]?3[_-]`)
	gemini3ProPattern   = regexp.MustCompile(`(?i)^gemini[_-]?3[_-]pro`)
	gemini3FlashPattern = regexp.MustCompile(`(?i)^gemini[_-]?3[_-]flash`)
	gemini25Pattern     = regexp.MustCompile(`(?i)^gemini[_-]?2\.5[_-]`)
)

// IsGemini3Model returns true if the model is a Gemini 3 family model.
// Gemini 3 models should use thinkingLevel (string) instead of thinkingBudget (number).
func IsGemini3Model(model string) bool {
	return gemini3Pattern.MatchString(model)
}

// IsGemini3ProModel returns true if the model is a Gemini 3 Pro variant.
// Gemini 3 Pro supports thinkingLevel: "low", "high" (default: "high")
func IsGemini3ProModel(model string) bool {
	return gemini3ProPattern.MatchString(model)
}

// IsGemini3FlashModel returns true if the model is a Gemini 3 Flash variant.
// Gemini 3 Flash supports thinkingLevel: "minimal", "low", "medium", "high" (default: "high")
func IsGemini3FlashModel(model string) bool {
	return gemini3FlashPattern.MatchString(model)
}

// IsGemini25Model returns true if the model is a Gemini 2.5 family model.
// Gemini 2.5 models should use thinkingBudget (number).
func IsGemini25Model(model string) bool {
	return gemini25Pattern.MatchString(model)
}

// Gemini3ProThinkingLevels are the valid thinkingLevel values for Gemini 3 Pro models.
var Gemini3ProThinkingLevels = []string{"low", "high"}

// Gemini3FlashThinkingLevels are the valid thinkingLevel values for Gemini 3 Flash models.
var Gemini3FlashThinkingLevels = []string{"minimal", "low", "medium", "high"}

func ApplyGeminiThinkingConfig(body []byte, budget *int, includeThoughts *bool) []byte {
	if budget == nil && includeThoughts == nil {
		return body
	}
	updated := body
	if budget != nil {
		valuePath := "generationConfig.thinkingConfig.thinkingBudget"
		rewritten, err := sjson.SetBytes(updated, valuePath, *budget)
		if err == nil {
			updated = rewritten
		}
	}
	// Default to including thoughts when a budget override is present but no explicit include flag is provided.
	incl := includeThoughts
	if incl == nil && budget != nil && *budget != 0 {
		defaultInclude := true
		incl = &defaultInclude
	}
	if incl != nil {
		valuePath := "generationConfig.thinkingConfig.include_thoughts"
		rewritten, err := sjson.SetBytes(updated, valuePath, *incl)
		if err == nil {
			updated = rewritten
		}
	}
	return updated
}

func ApplyGeminiCLIThinkingConfig(body []byte, budget *int, includeThoughts *bool) []byte {
	if budget == nil && includeThoughts == nil {
		return body
	}
	updated := body
	if budget != nil {
		valuePath := "request.generationConfig.thinkingConfig.thinkingBudget"
		rewritten, err := sjson.SetBytes(updated, valuePath, *budget)
		if err == nil {
			updated = rewritten
		}
	}
	// Default to including thoughts when a budget override is present but no explicit include flag is provided.
	incl := includeThoughts
	if incl == nil && budget != nil && *budget != 0 {
		defaultInclude := true
		incl = &defaultInclude
	}
	if incl != nil {
		valuePath := "request.generationConfig.thinkingConfig.include_thoughts"
		rewritten, err := sjson.SetBytes(updated, valuePath, *incl)
		if err == nil {
			updated = rewritten
		}
	}
	return updated
}

// ApplyGeminiThinkingLevel applies thinkingLevel config for Gemini 3 models.
// For standard Gemini API format (generationConfig.thinkingConfig path).
// Per Google's documentation, Gemini 3 models should use thinkingLevel instead of thinkingBudget.
func ApplyGeminiThinkingLevel(body []byte, level string, includeThoughts *bool) []byte {
	if level == "" && includeThoughts == nil {
		return body
	}
	updated := body
	if level != "" {
		valuePath := "generationConfig.thinkingConfig.thinkingLevel"
		rewritten, err := sjson.SetBytes(updated, valuePath, level)
		if err == nil {
			updated = rewritten
		}
	}
	// Default to including thoughts when a level is set but no explicit include flag is provided.
	incl := includeThoughts
	if incl == nil && level != "" {
		defaultInclude := true
		incl = &defaultInclude
	}
	if incl != nil {
		valuePath := "generationConfig.thinkingConfig.includeThoughts"
		rewritten, err := sjson.SetBytes(updated, valuePath, *incl)
		if err == nil {
			updated = rewritten
		}
	}
	if it := gjson.GetBytes(body, "generationConfig.thinkingConfig.include_thoughts"); it.Exists() {
		updated, _ = sjson.DeleteBytes(updated, "generationConfig.thinkingConfig.include_thoughts")
	}
	if tb := gjson.GetBytes(body, "generationConfig.thinkingConfig.thinkingBudget"); tb.Exists() {
		updated, _ = sjson.DeleteBytes(updated, "generationConfig.thinkingConfig.thinkingBudget")
	}
	return updated
}

// ApplyGeminiCLIThinkingLevel applies thinkingLevel config for Gemini 3 models.
// For Gemini CLI API format (request.generationConfig.thinkingConfig path).
// Per Google's documentation, Gemini 3 models should use thinkingLevel instead of thinkingBudget.
func ApplyGeminiCLIThinkingLevel(body []byte, level string, includeThoughts *bool) []byte {
	if level == "" && includeThoughts == nil {
		return body
	}
	updated := body
	if level != "" {
		valuePath := "request.generationConfig.thinkingConfig.thinkingLevel"
		rewritten, err := sjson.SetBytes(updated, valuePath, level)
		if err == nil {
			updated = rewritten
		}
	}
	// Default to including thoughts when a level is set but no explicit include flag is provided.
	incl := includeThoughts
	if incl == nil && level != "" {
		defaultInclude := true
		incl = &defaultInclude
	}
	if incl != nil {
		valuePath := "request.generationConfig.thinkingConfig.includeThoughts"
		rewritten, err := sjson.SetBytes(updated, valuePath, *incl)
		if err == nil {
			updated = rewritten
		}
	}
	if it := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.include_thoughts"); it.Exists() {
		updated, _ = sjson.DeleteBytes(updated, "request.generationConfig.thinkingConfig.include_thoughts")
	}
	if tb := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.thinkingBudget"); tb.Exists() {
		updated, _ = sjson.DeleteBytes(updated, "request.generationConfig.thinkingConfig.thinkingBudget")
	}
	return updated
}

// ValidateGemini3ThinkingLevel validates that the thinkingLevel is valid for the Gemini 3 model variant.
// Returns the validated level (normalized to lowercase) and true if valid, or empty string and false if invalid.
func ValidateGemini3ThinkingLevel(model, level string) (string, bool) {
	if level == "" {
		return "", false
	}
	normalized := strings.ToLower(strings.TrimSpace(level))

	var validLevels []string
	if IsGemini3ProModel(model) {
		validLevels = Gemini3ProThinkingLevels
	} else if IsGemini3FlashModel(model) {
		validLevels = Gemini3FlashThinkingLevels
	} else if IsGemini3Model(model) {
		// Unknown Gemini 3 variant - allow all levels as fallback
		validLevels = Gemini3FlashThinkingLevels
	} else {
		return "", false
	}

	for _, valid := range validLevels {
		if normalized == valid {
			return normalized, true
		}
	}
	return "", false
}

// ThinkingBudgetToGemini3Level converts a thinkingBudget to a thinkingLevel for Gemini 3 models.
// This provides backward compatibility when thinkingBudget is provided for Gemini 3 models.
// Returns the appropriate thinkingLevel and true if conversion is possible.
func ThinkingBudgetToGemini3Level(model string, budget int) (string, bool) {
	if !IsGemini3Model(model) {
		return "", false
	}

	// Map budget to level based on Google's documentation
	// Gemini 3 Pro: "low", "high" (default: "high")
	// Gemini 3 Flash: "minimal", "low", "medium", "high" (default: "high")
	switch {
	case budget == -1:
		// Dynamic budget maps to "high" (API default)
		return "high", true
	case budget == 0:
		// Zero budget - Gemini 3 doesn't support disabling thinking
		// Map to lowest available level
		if IsGemini3FlashModel(model) {
			return "minimal", true
		}
		return "low", true
	case budget > 0 && budget <= 512:
		if IsGemini3FlashModel(model) {
			return "minimal", true
		}
		return "low", true
	case budget <= 1024:
		return "low", true
	case budget <= 8192:
		if IsGemini3FlashModel(model) {
			return "medium", true
		}
		return "low", true // Pro doesn't have medium, use low
	default:
		return "high", true
	}
}

// modelsWithDefaultThinking lists models that should have thinking enabled by default
// when no explicit thinkingConfig is provided.
var modelsWithDefaultThinking = map[string]bool{
	"gemini-3-pro-preview":       true,
	"gemini-3-pro-image-preview": true,
	// "gemini-3-flash-preview":     true,
}

// ModelHasDefaultThinking returns true if the model should have thinking enabled by default.
func ModelHasDefaultThinking(model string) bool {
	return modelsWithDefaultThinking[model]
}

// ApplyDefaultThinkingIfNeeded injects default thinkingConfig for models that require it.
// For standard Gemini API format (generationConfig.thinkingConfig path).
// Returns the modified body if thinkingConfig was added, otherwise returns the original.
// For Gemini 3 models, uses thinkingLevel instead of thinkingBudget per Google's documentation.
func ApplyDefaultThinkingIfNeeded(model string, body []byte) []byte {
	if !ModelHasDefaultThinking(model) {
		return body
	}
	if gjson.GetBytes(body, "generationConfig.thinkingConfig").Exists() {
		return body
	}
	// Gemini 3 models use thinkingLevel instead of thinkingBudget
	if IsGemini3Model(model) {
		// Don't set a default - let the API use its dynamic default ("high")
		// Only set includeThoughts
		updated, _ := sjson.SetBytes(body, "generationConfig.thinkingConfig.includeThoughts", true)
		return updated
	}
	// Gemini 2.5 and other models use thinkingBudget
	updated, _ := sjson.SetBytes(body, "generationConfig.thinkingConfig.thinkingBudget", -1)
	updated, _ = sjson.SetBytes(updated, "generationConfig.thinkingConfig.include_thoughts", true)
	return updated
}

// ApplyGemini3ThinkingLevelFromMetadata applies thinkingLevel from metadata for Gemini 3 models.
// For standard Gemini API format (generationConfig.thinkingConfig path).
// This handles the case where reasoning_effort is specified via model name suffix (e.g., model(minimal)).
func ApplyGemini3ThinkingLevelFromMetadata(model string, metadata map[string]any, body []byte) []byte {
	if !IsGemini3Model(model) {
		return body
	}
	effort, ok := ReasoningEffortFromMetadata(metadata)
	if !ok || effort == "" {
		return body
	}
	// Validate and apply the thinkingLevel
	if level, valid := ValidateGemini3ThinkingLevel(model, effort); valid {
		return ApplyGeminiThinkingLevel(body, level, nil)
	}
	return body
}

// ApplyGemini3ThinkingLevelFromMetadataCLI applies thinkingLevel from metadata for Gemini 3 models.
// For Gemini CLI API format (request.generationConfig.thinkingConfig path).
// This handles the case where reasoning_effort is specified via model name suffix (e.g., model(minimal)).
func ApplyGemini3ThinkingLevelFromMetadataCLI(model string, metadata map[string]any, body []byte) []byte {
	if !IsGemini3Model(model) {
		return body
	}
	effort, ok := ReasoningEffortFromMetadata(metadata)
	if !ok || effort == "" {
		return body
	}
	// Validate and apply the thinkingLevel
	if level, valid := ValidateGemini3ThinkingLevel(model, effort); valid {
		return ApplyGeminiCLIThinkingLevel(body, level, nil)
	}
	return body
}

// ApplyDefaultThinkingIfNeededCLI injects default thinkingConfig for models that require it.
// For Gemini CLI API format (request.generationConfig.thinkingConfig path).
// Returns the modified body if thinkingConfig was added, otherwise returns the original.
// For Gemini 3 models, uses thinkingLevel instead of thinkingBudget per Google's documentation.
func ApplyDefaultThinkingIfNeededCLI(model string, body []byte) []byte {
	if !ModelHasDefaultThinking(model) {
		return body
	}
	if gjson.GetBytes(body, "request.generationConfig.thinkingConfig").Exists() {
		return body
	}
	// Gemini 3 models use thinkingLevel instead of thinkingBudget
	if IsGemini3Model(model) {
		// Don't set a default - let the API use its dynamic default ("high")
		// Only set includeThoughts
		updated, _ := sjson.SetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts", true)
		return updated
	}
	// Gemini 2.5 and other models use thinkingBudget
	updated, _ := sjson.SetBytes(body, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
	updated, _ = sjson.SetBytes(updated, "request.generationConfig.thinkingConfig.include_thoughts", true)
	return updated
}

// StripThinkingConfigIfUnsupported removes thinkingConfig from the request body
// when the target model does not advertise Thinking capability. It cleans both
// standard Gemini and Gemini CLI JSON envelopes. This acts as a final safety net
// in case upstream injected thinking for an unsupported model.
func StripThinkingConfigIfUnsupported(model string, body []byte) []byte {
	if ModelSupportsThinking(model) || len(body) == 0 {
		return body
	}
	updated := body
	// Gemini CLI path
	updated, _ = sjson.DeleteBytes(updated, "request.generationConfig.thinkingConfig")
	// Standard Gemini path
	updated, _ = sjson.DeleteBytes(updated, "generationConfig.thinkingConfig")
	return updated
}

// NormalizeGeminiThinkingBudget normalizes the thinkingBudget value in a standard Gemini
// request body (generationConfig.thinkingConfig.thinkingBudget path).
// For Gemini 3 models, converts thinkingBudget to thinkingLevel per Google's documentation,
// unless skipGemini3Check is provided and true.
func NormalizeGeminiThinkingBudget(model string, body []byte, skipGemini3Check ...bool) []byte {
	const budgetPath = "generationConfig.thinkingConfig.thinkingBudget"
	const levelPath = "generationConfig.thinkingConfig.thinkingLevel"

	budget := gjson.GetBytes(body, budgetPath)
	if !budget.Exists() {
		return body
	}

	// For Gemini 3 models, convert thinkingBudget to thinkingLevel
	skipGemini3 := len(skipGemini3Check) > 0 && skipGemini3Check[0]
	if IsGemini3Model(model) && !skipGemini3 {
		if level, ok := ThinkingBudgetToGemini3Level(model, int(budget.Int())); ok {
			updated, _ := sjson.SetBytes(body, levelPath, level)
			updated, _ = sjson.DeleteBytes(updated, budgetPath)
			return updated
		}
		// If conversion fails, just remove the budget (let API use default)
		updated, _ := sjson.DeleteBytes(body, budgetPath)
		return updated
	}

	// For Gemini 2.5 and other models, normalize the budget value
	normalized := NormalizeThinkingBudget(model, int(budget.Int()))
	updated, _ := sjson.SetBytes(body, budgetPath, normalized)
	return updated
}

// NormalizeGeminiCLIThinkingBudget normalizes the thinkingBudget value in a Gemini CLI
// request body (request.generationConfig.thinkingConfig.thinkingBudget path).
// For Gemini 3 models, converts thinkingBudget to thinkingLevel per Google's documentation,
// unless skipGemini3Check is provided and true.
func NormalizeGeminiCLIThinkingBudget(model string, body []byte, skipGemini3Check ...bool) []byte {
	const budgetPath = "request.generationConfig.thinkingConfig.thinkingBudget"
	const levelPath = "request.generationConfig.thinkingConfig.thinkingLevel"

	budget := gjson.GetBytes(body, budgetPath)
	if !budget.Exists() {
		return body
	}

	// For Gemini 3 models, convert thinkingBudget to thinkingLevel
	skipGemini3 := len(skipGemini3Check) > 0 && skipGemini3Check[0]
	if IsGemini3Model(model) && !skipGemini3 {
		if level, ok := ThinkingBudgetToGemini3Level(model, int(budget.Int())); ok {
			updated, _ := sjson.SetBytes(body, levelPath, level)
			updated, _ = sjson.DeleteBytes(updated, budgetPath)
			return updated
		}
		// If conversion fails, just remove the budget (let API use default)
		updated, _ := sjson.DeleteBytes(body, budgetPath)
		return updated
	}

	// For Gemini 2.5 and other models, normalize the budget value
	normalized := NormalizeThinkingBudget(model, int(budget.Int()))
	updated, _ := sjson.SetBytes(body, budgetPath, normalized)
	return updated
}

// ReasoningEffortBudgetMapping defines the thinkingBudget values for each reasoning effort level.
var ReasoningEffortBudgetMapping = map[string]int{
	"none":    0,
	"auto":    -1,
	"minimal": 512,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
}

// ApplyReasoningEffortToGemini applies OpenAI reasoning_effort to Gemini thinkingConfig
// for standard Gemini API format (generationConfig.thinkingConfig path).
// Returns the modified body with thinkingBudget and include_thoughts set.
func ApplyReasoningEffortToGemini(body []byte, effort string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	if normalized == "" {
		return body
	}

	budgetPath := "generationConfig.thinkingConfig.thinkingBudget"
	includePath := "generationConfig.thinkingConfig.include_thoughts"

	if normalized == "none" {
		body, _ = sjson.DeleteBytes(body, "generationConfig.thinkingConfig")
		return body
	}

	budget, ok := ReasoningEffortBudgetMapping[normalized]
	if !ok {
		return body
	}

	body, _ = sjson.SetBytes(body, budgetPath, budget)
	body, _ = sjson.SetBytes(body, includePath, true)
	return body
}

// ApplyReasoningEffortToGeminiCLI applies OpenAI reasoning_effort to Gemini CLI thinkingConfig
// for Gemini CLI API format (request.generationConfig.thinkingConfig path).
// Returns the modified body with thinkingBudget and include_thoughts set.
func ApplyReasoningEffortToGeminiCLI(body []byte, effort string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	if normalized == "" {
		return body
	}

	budgetPath := "request.generationConfig.thinkingConfig.thinkingBudget"
	includePath := "request.generationConfig.thinkingConfig.include_thoughts"

	if normalized == "none" {
		body, _ = sjson.DeleteBytes(body, "request.generationConfig.thinkingConfig")
		return body
	}

	budget, ok := ReasoningEffortBudgetMapping[normalized]
	if !ok {
		return body
	}

	body, _ = sjson.SetBytes(body, budgetPath, budget)
	body, _ = sjson.SetBytes(body, includePath, true)
	return body
}

// ConvertThinkingLevelToBudget checks for "generationConfig.thinkingConfig.thinkingLevel"
// and converts it to "thinkingBudget" for Gemini 2.5 models.
// For Gemini 3 models, preserves thinkingLevel unless skipGemini3Check is provided and true.
// Mappings for Gemini 2.5:
//   - "high" -> 32768
//   - "medium" -> 8192
//   - "low" -> 1024
//   - "minimal" -> 512
//
// It removes "thinkingLevel" after conversion (for Gemini 2.5 only).
func ConvertThinkingLevelToBudget(body []byte, model string, skipGemini3Check ...bool) []byte {
	levelPath := "generationConfig.thinkingConfig.thinkingLevel"
	res := gjson.GetBytes(body, levelPath)
	if !res.Exists() {
		return body
	}

	// For Gemini 3 models, preserve thinkingLevel unless explicitly skipped
	skipGemini3 := len(skipGemini3Check) > 0 && skipGemini3Check[0]
	if IsGemini3Model(model) && !skipGemini3 {
		return body
	}

	budget, ok := ThinkingLevelToBudget(res.String())
	if !ok {
		updated, _ := sjson.DeleteBytes(body, levelPath)
		return updated
	}

	budgetPath := "generationConfig.thinkingConfig.thinkingBudget"
	updated, err := sjson.SetBytes(body, budgetPath, budget)
	if err != nil {
		return body
	}

	updated, err = sjson.DeleteBytes(updated, levelPath)
	if err != nil {
		return body
	}
	return updated
}

// ConvertThinkingLevelToBudgetCLI checks for "request.generationConfig.thinkingConfig.thinkingLevel"
// and converts it to "thinkingBudget" for Gemini 2.5 models.
// For Gemini 3 models, preserves thinkingLevel as-is (does not convert).
func ConvertThinkingLevelToBudgetCLI(body []byte, model string) []byte {
	levelPath := "request.generationConfig.thinkingConfig.thinkingLevel"
	res := gjson.GetBytes(body, levelPath)
	if !res.Exists() {
		return body
	}

	// For Gemini 3 models, preserve thinkingLevel - don't convert to budget
	if IsGemini3Model(model) {
		return body
	}

	budget, ok := ThinkingLevelToBudget(res.String())
	if !ok {
		updated, _ := sjson.DeleteBytes(body, levelPath)
		return updated
	}

	budgetPath := "request.generationConfig.thinkingConfig.thinkingBudget"
	updated, err := sjson.SetBytes(body, budgetPath, budget)
	if err != nil {
		return body
	}

	updated, err = sjson.DeleteBytes(updated, levelPath)
	if err != nil {
		return body
	}
	return updated
}
