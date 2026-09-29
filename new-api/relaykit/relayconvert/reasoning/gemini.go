package reasoning

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type GeminiRender struct {
	Config          *dto.GeminiThinkingConfig
	EffectiveEffort Effort
	Diagnostics     []types.ConversionDiagnostic
}

type geminiThinkingKind int

const (
	geminiThinkingUnknown geminiThinkingKind = iota
	geminiThinkingNotConfigurable
	geminiThinkingBudget
	geminiThinkingLevel
)

type geminiCapabilities struct {
	kind                    geminiThinkingKind
	supportsDisable         bool
	supportsIncludeThoughts bool
	minBudget               int
	maxBudget               int
}

func geminiCapabilitiesFor(model string) geminiCapabilities {
	model = strings.ToLower(model)
	switch {
	case strings.HasPrefix(model, "gemini-2.5-flash-native-audio"),
		strings.HasPrefix(model, "gemini-live-2.5-flash-preview-native-audio"):
		return geminiCapabilities{kind: geminiThinkingBudget, supportsDisable: true, maxBudget: 24576}
	case strings.HasPrefix(model, "gemini-2.5-flash-image"),
		strings.Contains(model, "-tts"),
		strings.Contains(model, "-native-audio"),
		strings.Contains(model, "-live"):
		return geminiCapabilities{kind: geminiThinkingNotConfigurable}
	case strings.HasPrefix(model, "gemini-3-pro-image"),
		strings.HasPrefix(model, "nano-banana-pro"):
		return geminiCapabilities{kind: geminiThinkingNotConfigurable, supportsIncludeThoughts: true}
	case model == "gemini-flash-latest", model == "gemini-flash-lite-latest":
		return geminiCapabilities{kind: geminiThinkingLevel}
	case model == "gemini-pro-latest":
		return geminiCapabilities{kind: geminiThinkingLevel}
	case strings.HasPrefix(model, "gemini-2.5-pro"):
		return geminiCapabilities{kind: geminiThinkingBudget, minBudget: 128, maxBudget: 32768}
	case strings.HasPrefix(model, "gemini-2.5-flash-lite"):
		return geminiCapabilities{kind: geminiThinkingBudget, supportsDisable: true, minBudget: 512, maxBudget: 24576}
	case strings.HasPrefix(model, "gemini-2.5-"):
		return geminiCapabilities{kind: geminiThinkingBudget, supportsDisable: true, maxBudget: 24576}
	case strings.HasPrefix(model, "gemini-3"):
		return geminiCapabilities{kind: geminiThinkingLevel}
	default:
		return geminiCapabilities{}
	}
}

// RenderGemini maps a portable intent onto the target model's thinkingConfig.
// The capability table only maps and clamps: budgets become levels on Gemini 3,
// efforts become budgets on Gemini 2.5, out-of-range values are clamped, and
// models that cannot disable thinking get their lowest setting. Every such
// coercion is reported in Diagnostics. Errors are reserved for unknown effort
// values and numeric overflow.
func RenderGemini(model string, intent Intent, maxOutputTokens *uint, adapterBudgetPercentage float64) (GeminiRender, error) {
	intent, diagnostics, err := normalizeIntent(intent)
	if err != nil {
		return GeminiRender{}, err
	}
	if intent.IsEmpty() {
		return GeminiRender{Diagnostics: diagnostics}, nil
	}

	capabilities := geminiCapabilitiesFor(model)
	if capabilities.kind == geminiThinkingNotConfigurable {
		render := GeminiRender{Diagnostics: diagnostics}
		if capabilities.supportsIncludeThoughts {
			render.EffectiveEffort = EffortHigh
			if intent.IncludeThoughts != nil {
				render.Config = &dto.GeminiThinkingConfig{IncludeThoughts: intent.IncludeThoughts}
			}
		}
		if intent.HasStrength() || (intent.IncludeThoughts != nil && !capabilities.supportsIncludeThoughts) {
			render.Diagnostics = append(render.Diagnostics, geminiReasoningDiagnostic(
				"gemini_thinking_unsupported",
				fmt.Sprintf("model %q does not support configurable thinking; the requested thinking controls were dropped", model),
			))
		}
		return render, nil
	}
	if capabilities.kind == geminiThinkingUnknown {
		// Without a capability entry there is no way to know whether the model
		// takes a budget or a level, so no strength is synthesized. Provider-
		// native controls the client sent stay untouched by the caller.
		render := GeminiRender{Diagnostics: diagnostics}
		if intent.HasStrength() {
			render.Diagnostics = append(render.Diagnostics, geminiReasoningDiagnostic(
				"gemini_unknown_capability",
				fmt.Sprintf("model %q has no known Gemini thinking configuration; the requested thinking strength was not applied", model),
			))
		}
		if intent.IncludeThoughts != nil {
			render.Config = &dto.GeminiThinkingConfig{IncludeThoughts: intent.IncludeThoughts}
		}
		return render, nil
	}

	config := &dto.GeminiThinkingConfig{IncludeThoughts: intent.IncludeThoughts}
	if capabilities.kind == geminiThinkingBudget {
		if intent.Mode == ModeDisabled || intent.Effort == EffortNone {
			budget := 0
			effort := EffortNone
			if !capabilities.supportsDisable {
				diagnostics = append(diagnostics, geminiReasoningDiagnostic(
					"gemini_thinking_disable_unsupported",
					fmt.Sprintf("model %q cannot disable thinking; using its minimum thinking budget %d", model, capabilities.minBudget),
				))
				budget = capabilities.minBudget
				effort = EffortFromBudget(budget)
			}
			config.ThinkingBudget = &budget
			return GeminiRender{Config: config, EffectiveEffort: effort, Diagnostics: diagnostics}, nil
		}

		budget := 0
		hasBudget := false
		if intent.BudgetTokens != nil {
			budget = *intent.BudgetTokens
			if budget != -1 {
				clamped := clampGeminiBudget(budget, capabilities)
				if clamped != budget {
					diagnostics = append(diagnostics, geminiReasoningDiagnostic(
						"gemini_budget_clamped",
						fmt.Sprintf("thinking budget %d is outside the supported range [%d,%d] for model %q; using %d", budget, capabilities.minBudget, capabilities.maxBudget, model, clamped),
					))
					budget = clamped
				}
			}
			hasBudget = true
		} else if intent.Effort != "" {
			budget = gemini25BudgetForEffort(intent.Effort)
			hasBudget = true
		} else if intent.Mode != ModeUnset && maxOutputTokens != nil && *maxOutputTokens > 0 {
			if uint64(*maxOutputTokens) > uint64(math.MaxInt) {
				return GeminiRender{}, fmt.Errorf("max_output_tokens is too large for a thinking budget")
			}
			percentage := adapterBudgetPercentage
			if percentage <= 0 {
				percentage = 0.6
			} else if percentage > 1 {
				percentage = 1
			}
			budget = int(math.Round(float64(*maxOutputTokens) * percentage))
			budget = clampGeminiBudget(budget, capabilities)
			hasBudget = true
		}
		if hasBudget {
			config.ThinkingBudget = &budget
		}
		effort := intent.Effort
		if hasBudget {
			effort = EffortFromBudget(budget)
		} else if intent.Mode == ModeEnabled || intent.Mode == ModeAdaptive {
			effort = geminiDefaultEffort(model)
		}
		return GeminiRender{Config: config, EffectiveEffort: effort, Diagnostics: diagnostics}, nil
	}

	if intent.Mode == ModeDisabled || intent.Effort == EffortNone {
		level, err := geminiLevelForEffort(model, EffortMinimal)
		if err != nil {
			return GeminiRender{}, err
		}
		diagnostics = append(diagnostics, geminiReasoningDiagnostic(
			"gemini_thinking_disable_unsupported",
			fmt.Sprintf("model %q cannot disable thinking; using its lowest thinking level %q", model, level),
		))
		config.ThinkingLevel = level
		return GeminiRender{Config: config, EffectiveEffort: Effort(level), Diagnostics: diagnostics}, nil
	}
	effort := intent.Effort
	if effort == "" && intent.BudgetTokens != nil {
		effort = EffortFromBudget(*intent.BudgetTokens)
		diagnostics = append(diagnostics, geminiReasoningDiagnostic(
			"gemini_budget_to_level",
			fmt.Sprintf("model %q uses thinkingLevel; thinking budget %d was converted to effort %q", model, *intent.BudgetTokens, effort),
		))
	}
	if effort != "" {
		level, err := geminiLevelForEffort(model, effort)
		if err != nil {
			return GeminiRender{}, err
		}
		if !strings.EqualFold(level, string(effort)) {
			diagnostics = append(diagnostics, geminiReasoningDiagnostic(
				"gemini_level_adjusted",
				fmt.Sprintf("model %q does not support thinking level %q; using %q", model, effort, level),
			))
		}
		config.ThinkingLevel = level
		effort = Effort(level)
	} else if intent.Mode == ModeEnabled || intent.Mode == ModeAdaptive {
		effort = geminiDefaultEffort(model)
	}
	return GeminiRender{Config: config, EffectiveEffort: effort, Diagnostics: diagnostics}, nil
}

func geminiDefaultEffort(model string) Effort {
	model = strings.ToLower(model)
	switch {
	case model == "gemini-flash-latest",
		strings.HasPrefix(model, "gemini-3.5-flash") && !strings.HasPrefix(model, "gemini-3.5-flash-lite"),
		strings.HasPrefix(model, "gemini-3.6-flash"):
		return EffortMedium
	case model == "gemini-flash-lite-latest",
		strings.HasPrefix(model, "gemini-3.5-flash-lite"),
		strings.HasPrefix(model, "gemini-3.1-flash-lite"):
		return EffortMinimal
	case model == "gemini-pro-latest",
		strings.HasPrefix(model, "gemini-3.1-pro"),
		strings.HasPrefix(model, "gemini-3-pro"),
		strings.HasPrefix(model, "gemini-3-flash"):
		return EffortHigh
	default:
		return ""
	}
}

// NormalizeGeminiThinkingConfig rewrites a provider-native thinkingConfig in
// place into the dialect the target model accepts: budgets become levels on
// Gemini 3, levels become budgets on Gemini 2.5, out-of-range budgets are
// clamped, unsupported levels move to the nearest supported one, and models
// without configurable thinking lose the config entirely. Models missing from
// the capability table keep their config verbatim. The returned effort is the
// accounting label for whatever is now in the config.
//
// The only error is a config that sets both thinkingBudget and thinkingLevel
// on a model whose dialect is unknown; when the dialect is known the field the
// model does not use is dropped with a diagnostic.
func NormalizeGeminiThinkingConfig(model string, generation *dto.GeminiChatGenerationConfig) (Effort, []types.ConversionDiagnostic, error) {
	if generation == nil || generation.ThinkingConfig == nil {
		return "", nil, nil
	}
	config := generation.ThinkingConfig
	capabilities := geminiCapabilitiesFor(model)
	var diagnostics []types.ConversionDiagnostic
	if config.ThinkingBudget != nil && config.ThinkingLevel != "" {
		switch capabilities.kind {
		case geminiThinkingBudget:
			diagnostics = append(diagnostics, geminiReasoningDiagnostic(
				"gemini_budget_level_conflict",
				fmt.Sprintf("model %q uses thinkingBudget; thinkingLevel %q was dropped because both were set", model, config.ThinkingLevel),
			))
			config.ThinkingLevel = ""
		case geminiThinkingLevel:
			diagnostics = append(diagnostics, geminiReasoningDiagnostic(
				"gemini_budget_level_conflict",
				fmt.Sprintf("model %q uses thinkingLevel; thinkingBudget %d was dropped because both were set", model, *config.ThinkingBudget),
			))
			config.ThinkingBudget = nil
		}
	}
	if capabilities.kind == geminiThinkingNotConfigurable {
		dropped := config.ThinkingBudget != nil || config.ThinkingLevel != "" || (config.IncludeThoughts != nil && !capabilities.supportsIncludeThoughts)
		if dropped {
			diagnostics = append(diagnostics, geminiReasoningDiagnostic(
				"gemini_thinking_unsupported",
				fmt.Sprintf("model %q does not support configurable thinking; the thinking configuration was dropped", model),
			))
		}
		if !capabilities.supportsIncludeThoughts || config.IncludeThoughts == nil {
			generation.ThinkingConfig = nil
			return "", diagnostics, nil
		}
		config.ThinkingBudget = nil
		config.ThinkingLevel = ""
		return EffortHigh, diagnostics, nil
	}

	intent, intentDiagnostics, err := FromGemini(&dto.GeminiChatRequest{GenerationConfig: *generation})
	if err != nil {
		return "", diagnostics, err
	}
	diagnostics = append(diagnostics, intentDiagnostics...)
	if capabilities.kind == geminiThinkingUnknown {
		return EffectiveEffort(intent), diagnostics, nil
	}

	if capabilities.kind == geminiThinkingBudget {
		if config.ThinkingLevel != "" {
			budget := gemini25BudgetForEffort(intent.Effort)
			diagnostics = append(diagnostics, geminiReasoningDiagnostic(
				"gemini_level_to_budget",
				fmt.Sprintf("model %q uses thinkingBudget; thinkingLevel %q was converted to budget %d", model, config.ThinkingLevel, budget),
			))
			config.ThinkingLevel = ""
			config.ThinkingBudget = &budget
		}
		if config.ThinkingBudget == nil {
			return EffectiveEffort(intent), diagnostics, nil
		}
		budget := *config.ThinkingBudget
		switch {
		case budget < -1:
			budget = -1
		case budget == 0 && !capabilities.supportsDisable:
			diagnostics = append(diagnostics, geminiReasoningDiagnostic(
				"gemini_thinking_disable_unsupported",
				fmt.Sprintf("model %q cannot disable thinking; using its minimum thinking budget %d", model, capabilities.minBudget),
			))
			budget = capabilities.minBudget
		case budget != 0 && budget != -1:
			clamped := clampGeminiBudget(budget, capabilities)
			if clamped != budget {
				diagnostics = append(diagnostics, geminiReasoningDiagnostic(
					"gemini_budget_clamped",
					fmt.Sprintf("thinking budget %d is outside the supported range [%d,%d] for model %q; using %d", budget, capabilities.minBudget, capabilities.maxBudget, model, clamped),
				))
				budget = clamped
			}
		}
		config.ThinkingBudget = &budget
		return EffortFromBudget(budget), diagnostics, nil
	}

	if config.ThinkingBudget != nil {
		budget := *config.ThinkingBudget
		config.ThinkingBudget = nil
		if budget == 0 {
			level, err := geminiLevelForEffort(model, EffortMinimal)
			if err != nil {
				return "", diagnostics, err
			}
			diagnostics = append(diagnostics, geminiReasoningDiagnostic(
				"gemini_thinking_disable_unsupported",
				fmt.Sprintf("model %q cannot disable thinking; using its lowest thinking level %q", model, level),
			))
			config.ThinkingLevel = level
			return Effort(level), diagnostics, nil
		}
		level, err := geminiLevelForEffort(model, EffortFromBudget(budget))
		if err != nil {
			return "", diagnostics, err
		}
		diagnostics = append(diagnostics, geminiReasoningDiagnostic(
			"gemini_budget_to_level",
			fmt.Sprintf("model %q uses thinkingLevel; thinkingBudget %d was converted to thinkingLevel %q", model, budget, level),
		))
		config.ThinkingLevel = level
		return Effort(level), diagnostics, nil
	}
	if config.ThinkingLevel == "" {
		return "", diagnostics, nil
	}
	if intent.Effort == EffortNone {
		level, err := geminiLevelForEffort(model, EffortMinimal)
		if err != nil {
			return "", diagnostics, err
		}
		diagnostics = append(diagnostics, geminiReasoningDiagnostic(
			"gemini_thinking_disable_unsupported",
			fmt.Sprintf("model %q cannot disable thinking; using its lowest thinking level %q", model, level),
		))
		config.ThinkingLevel = level
		return Effort(level), diagnostics, nil
	}
	level, err := geminiLevelForEffort(model, intent.Effort)
	if err != nil {
		return "", diagnostics, err
	}
	if level != string(intent.Effort) {
		diagnostics = append(diagnostics, geminiReasoningDiagnostic(
			"gemini_level_adjusted",
			fmt.Sprintf("model %q does not support thinkingLevel %q; using %q", model, config.ThinkingLevel, level),
		))
	}
	config.ThinkingLevel = level
	return Effort(level), diagnostics, nil
}

func geminiReasoningDiagnostic(code string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{
		Code:     code,
		Path:     "generationConfig.thinkingConfig",
		Message:  message,
		Severity: types.ConversionDiagnosticWarning,
	}
}

// ResolveGeminiDefault materializes documented family defaults when a
// conversion targets another protocol. Dynamic 2.5 defaults retain their -1
// budget in the in-process pivot; Flash-Lite's default is explicitly off.
func ResolveGeminiDefault(model string, intent Intent) Intent {
	if intent.HasStrength() {
		return intent
	}
	capabilities := geminiCapabilitiesFor(model)
	if capabilities.kind == geminiThinkingBudget {
		if strings.HasPrefix(strings.ToLower(model), "gemini-2.5-flash-lite") {
			budget := 0
			intent.Mode = ModeDisabled
			intent.Effort = EffortNone
			intent.BudgetTokens = &budget
			intent.BudgetSource = SourceNative
			return intent
		}
		budget := -1
		intent.Mode = ModeEnabled
		intent.BudgetTokens = &budget
		intent.BudgetSource = SourceNative
		return intent
	}
	if capabilities.kind != geminiThinkingLevel {
		return intent
	}
	effort := geminiDefaultEffort(model)
	if effort == "" {
		return intent
	}
	intent.Mode = ModeEnabled
	intent.Effort = effort
	return intent
}

// ResolveGeminiEnabledDefault fills the strength implied by an explicit
// enable-only control such as the legacy -thinking model alias.
func ResolveGeminiEnabledDefault(model string, intent Intent, maxOutputTokens *uint) Intent {
	if intent.Mode != ModeEnabled || intent.Effort != "" || intent.BudgetTokens != nil {
		return intent
	}
	capabilities := geminiCapabilitiesFor(model)
	if capabilities.kind == geminiThinkingBudget {
		if intent.Source == SourceSuffix && maxOutputTokens != nil && *maxOutputTokens > 0 {
			return intent
		}
		budget := -1
		intent.BudgetTokens = &budget
		intent.BudgetSource = SourceSuffix
		return intent
	}
	if capabilities.kind == geminiThinkingLevel {
		intent.Effort = geminiDefaultEffort(model)
	}
	return intent
}

// EquivalentGeminiStrength compares two controls after applying the target
// model's budget/level mapping. This accepts distinct canonical labels that
// are identical on the Gemini wire (for example minimal and low on 2.5).
func EquivalentGeminiStrength(model string, left Intent, right Intent) (bool, error) {
	leftRendered, err := RenderGemini(model, left, nil, 0)
	if err != nil {
		return false, err
	}
	rightRendered, err := RenderGemini(model, right, nil, 0)
	if err != nil {
		return false, err
	}
	if leftRendered.Config == nil || rightRendered.Config == nil {
		return leftRendered.Config == nil && rightRendered.Config == nil, nil
	}
	leftConfig, rightConfig := leftRendered.Config, rightRendered.Config
	if leftConfig.ThinkingLevel != rightConfig.ThinkingLevel {
		return false, nil
	}
	if (leftConfig.ThinkingBudget == nil) != (rightConfig.ThinkingBudget == nil) {
		return false, nil
	}
	return leftConfig.ThinkingBudget == nil || *leftConfig.ThinkingBudget == *rightConfig.ThinkingBudget, nil
}

func gemini25BudgetForEffort(effort Effort) int {
	switch effort {
	case EffortMinimal, EffortLow:
		return 1024
	case EffortMedium:
		return 8192
	case EffortHigh, EffortXHigh, EffortMax:
		return 24576
	default:
		return 0
	}
}

func geminiLevelForEffort(model string, effort Effort) (string, error) {
	model = strings.ToLower(model)
	switch {
	case strings.HasPrefix(model, "gemini-3.1-flash-image"),
		strings.HasPrefix(model, "gemini-3.1-flash-lite-image"):
		if effort == EffortMinimal || effort == EffortLow {
			return string(EffortMinimal), nil
		}
		return string(EffortHigh), nil
	case (strings.HasPrefix(model, "gemini-3-pro") && !strings.HasPrefix(model, "gemini-3.1-pro")):
		if effort == EffortMinimal || effort == EffortLow {
			return string(EffortLow), nil
		}
		return string(EffortHigh), nil
	case strings.HasPrefix(model, "gemini-3.1-pro"), model == "gemini-pro-latest":
		if effort == EffortMinimal {
			return string(EffortLow), nil
		}
	}
	switch effort {
	case EffortMinimal, EffortLow, EffortMedium, EffortHigh:
		return string(effort), nil
	case EffortXHigh, EffortMax:
		return string(EffortHigh), nil
	case EffortNone:
		return "", fmt.Errorf("%w for model %q", ErrThinkingNotDisabled, model)
	default:
		return "", fmt.Errorf("%w %q for model %q", ErrUnsupportedEffort, effort, model)
	}
}

func clampGeminiBudget(budget int, capabilities geminiCapabilities) int {
	return min(max(budget, capabilities.minBudget), capabilities.maxBudget)
}
