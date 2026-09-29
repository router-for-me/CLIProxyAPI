package reasoning

import (
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type ClaudeRender struct {
	Thinking                  *dto.Thinking
	OutputEffort              Effort
	EffectiveEffort           Effort
	ClearSampling             bool
	ConstrainThinkingSampling bool
	// MaxTokens is set when manual thinking needed a larger max_tokens than
	// the request carried; callers apply it to the outgoing request.
	MaxTokens   *uint
	Diagnostics []types.ConversionDiagnostic
}

type claudeCapabilities struct {
	adaptive        bool
	supportsManual  bool
	defaultThinking bool
	supportsDisable bool
	supportsEffort  bool
	supportsXHigh   bool
	supportsMax     bool
	strictSampling  bool
}

func claudeCapabilitiesFor(model string) claudeCapabilities {
	model = strings.ToLower(model)
	capabilities := claudeCapabilities{supportsManual: true, supportsDisable: true}

	switch {
	case strings.HasPrefix(model, "claude-fable-5"),
		strings.HasPrefix(model, "claude-mythos-5"):
		capabilities.adaptive = true
		capabilities.supportsManual = false
		capabilities.defaultThinking = true
		capabilities.supportsDisable = false
		capabilities.supportsXHigh = true
		capabilities.supportsMax = true
		capabilities.strictSampling = true
	case strings.HasPrefix(model, "claude-mythos-preview"):
		capabilities.adaptive = true
		capabilities.defaultThinking = true
		capabilities.supportsDisable = false
		capabilities.supportsMax = true
		capabilities.strictSampling = true
	case strings.HasPrefix(model, "claude-opus-5"),
		strings.HasPrefix(model, "claude-sonnet-5"),
		strings.HasPrefix(model, "claude-opus-4-8"),
		strings.HasPrefix(model, "claude-opus-4-7"):
		capabilities.adaptive = true
		capabilities.supportsManual = false
		if strings.HasPrefix(model, "claude-opus-5") || strings.HasPrefix(model, "claude-sonnet-5") {
			capabilities.defaultThinking = true
		}
		capabilities.supportsEffort = true
		capabilities.supportsXHigh = true
		capabilities.supportsMax = true
		capabilities.strictSampling = true
	case strings.HasPrefix(model, "claude-opus-4-6"),
		strings.HasPrefix(model, "claude-sonnet-4-6"):
		capabilities.adaptive = true
		capabilities.supportsEffort = true
		capabilities.supportsMax = true
	case strings.HasPrefix(model, "claude-opus-4-5"):
		capabilities.supportsEffort = true
	}

	return capabilities
}

func RenderClaude(model string, intent Intent, maxTokens *uint, adapterBudgetPercentage float64) (ClaudeRender, error) {
	disabledWithEffort := intent.Mode == ModeDisabled && intent.Effort != "" && intent.Effort != EffortNone
	if disabledWithEffort {
		effort, err := ParseEffort(string(intent.Effort))
		if err != nil {
			return ClaudeRender{}, err
		}
		intent.Effort = effort
	}
	diagnostics := make([]types.ConversionDiagnostic, 0, 1)
	if !disabledWithEffort {
		var normalizeDiagnostics []types.ConversionDiagnostic
		var err error
		intent, normalizeDiagnostics, err = normalizeIntent(intent)
		if err != nil {
			return ClaudeRender{}, err
		}
		diagnostics = append(diagnostics, normalizeDiagnostics...)
	}
	capabilities := claudeCapabilitiesFor(model)
	if disabledWithEffort {
		diagnostics = append(diagnostics, claudeReasoningDiagnostic(
			"claude_disabled_effort_ignored",
			fmt.Sprintf("model %q cannot apply effort %q while thinking is disabled; the effort was ignored", model, intent.Effort),
		))
	}
	if !intent.HasStrength() {
		if intent.IncludeThoughts != nil && capabilities.adaptive && capabilities.defaultThinking {
			thinking := &dto.Thinking{Type: "adaptive"}
			if *intent.IncludeThoughts {
				thinking.Display = "summarized"
			} else {
				thinking.Display = "omitted"
			}
			return ClaudeRender{
				Thinking:        thinking,
				EffectiveEffort: EffortHigh,
				ClearSampling:   capabilities.strictSampling,
			}, nil
		}
		if capabilities.defaultThinking {
			return ClaudeRender{EffectiveEffort: EffortHigh, ClearSampling: capabilities.strictSampling}, nil
		}
		return ClaudeRender{ClearSampling: capabilities.strictSampling}, nil
	}

	if intent.Mode == ModeDisabled || intent.Effort == EffortNone {
		if !capabilities.supportsDisable {
			diagnostics = append(diagnostics, claudeReasoningDiagnostic(
				"claude_thinking_disable_unsupported",
				fmt.Sprintf("model %q cannot disable thinking; using the lowest representable thinking mode", model),
			))
			if capabilities.adaptive {
				thinking := &dto.Thinking{Type: "adaptive"}
				if intent.IncludeThoughts != nil {
					if *intent.IncludeThoughts {
						thinking.Display = "summarized"
					} else {
						thinking.Display = "omitted"
					}
				}
				outputEffort := Effort("")
				effectiveEffort := EffortHigh
				if capabilities.supportsEffort {
					outputEffort = EffortLow
					effectiveEffort = EffortLow
				}
				return ClaudeRender{
					Thinking:        thinking,
					OutputEffort:    outputEffort,
					EffectiveEffort: effectiveEffort,
					ClearSampling:   capabilities.strictSampling,
					Diagnostics:     diagnostics,
				}, nil
			}
			return ClaudeRender{Diagnostics: diagnostics}, nil
		}
		return ClaudeRender{
			Thinking:        &dto.Thinking{Type: "disabled"},
			EffectiveEffort: EffortNone,
			ClearSampling:   capabilities.strictSampling,
			Diagnostics:     diagnostics,
		}, nil
	}

	preferManual := capabilities.supportsManual && intent.BudgetTokens != nil && intent.Mode != ModeAdaptive
	if !capabilities.supportsManual && intent.BudgetTokens != nil && intent.Mode == ModeEnabled {
		diagnostics = append(diagnostics, claudeReasoningDiagnostic(
			"claude_budget_to_adaptive",
			fmt.Sprintf("model %q uses adaptive thinking; budget_tokens was converted to an effort level", model),
		))
	}
	if capabilities.adaptive && !preferManual {
		effort := intent.Effort
		if effort == "" && intent.BudgetTokens != nil {
			effort = EffortFromBudget(*intent.BudgetTokens)
		}
		if effort == "" && intent.Mode == ModeEnabled {
			effort = EffortHigh
		}
		normalizedEffort := normalizeClaudeEffort(effort, capabilities)
		if effort != "" && normalizedEffort != effort {
			diagnostics = append(diagnostics, claudeReasoningDiagnostic(
				"claude_effort_adjusted",
				fmt.Sprintf("model %q does not support effort %q; using %q", model, effort, normalizedEffort),
			))
		}
		effort = normalizedEffort
		effectiveEffort := effort
		if effectiveEffort == "" && intent.Mode == ModeAdaptive {
			effectiveEffort = EffortHigh
		}

		// Claude effort can be used without enabling thinking. Preserve that
		// distinction for native Claude requests; OpenAI extractors explicitly
		// mark reasoning efforts as ModeEnabled.
		if intent.Mode == ModeUnset {
			return ClaudeRender{
				OutputEffort:    effort,
				EffectiveEffort: effectiveEffort,
				ClearSampling:   capabilities.strictSampling,
				Diagnostics:     diagnostics,
			}, nil
		}

		thinking := &dto.Thinking{Type: "adaptive"}
		if intent.IncludeThoughts != nil {
			if *intent.IncludeThoughts {
				thinking.Display = "summarized"
			} else {
				thinking.Display = "omitted"
			}
		}
		return ClaudeRender{
			Thinking:                  thinking,
			OutputEffort:              effort,
			EffectiveEffort:           effectiveEffort,
			ClearSampling:             capabilities.strictSampling,
			ConstrainThinkingSampling: !capabilities.strictSampling,
			Diagnostics:               diagnostics,
		}, nil
	}

	if intent.Mode == ModeAdaptive {
		diagnostics = append(diagnostics, claudeReasoningDiagnostic(
			"claude_adaptive_to_manual",
			fmt.Sprintf("model %q does not support adaptive thinking; using manual thinking", model),
		))
		intent.Mode = ModeEnabled
		if intent.Effort == "" {
			intent.Effort = EffortHigh
		}
	}
	if intent.Mode == ModeUnset {
		return ClaudeRender{OutputEffort: intent.Effort, EffectiveEffort: intent.Effort, Diagnostics: diagnostics}, nil
	}
	var raisedMaxTokens *uint
	if maxTokens == nil || *maxTokens <= 1024 {
		// Manual thinking needs room above the 1024-token minimum budget.
		// Raising max_tokens keeps the request usable instead of rejecting it.
		raised := uint(1280)
		if intent.BudgetTokens != nil && *intent.BudgetTokens >= 0 && *intent.BudgetTokens < math.MaxInt32/2 {
			raised = max(raised, uint(*intent.BudgetTokens)+1)
		}
		current := "a missing max_tokens"
		if maxTokens != nil {
			current = fmt.Sprintf("max_tokens %d", *maxTokens)
		}
		diagnostics = append(diagnostics, claudeReasoningDiagnostic(
			"claude_max_tokens_raised",
			fmt.Sprintf("model %q requires max_tokens greater than 1024 for manual thinking; raised %s to %d", model, current, raised),
		))
		raisedMaxTokens = &raised
		maxTokens = &raised
	}
	if uint64(*maxTokens) > uint64(math.MaxInt) {
		return ClaudeRender{}, fmt.Errorf("max_tokens is too large for a thinking budget")
	}

	budget := 0
	if intent.BudgetTokens != nil && *intent.BudgetTokens >= 0 {
		requestedBudget := *intent.BudgetTokens
		budget = max(requestedBudget, 1024)
		if uint(budget) >= *maxTokens {
			budget = int(*maxTokens) - 1
		}
		if budget != requestedBudget {
			diagnostics = append(diagnostics, claudeReasoningDiagnostic(
				"claude_budget_adjusted",
				fmt.Sprintf("model %q requires 1024 <= budget_tokens < max_tokens; adjusted %d to %d", model, requestedBudget, budget),
			))
		}
	} else {
		if intent.BudgetTokens != nil {
			diagnostics = append(diagnostics, claudeReasoningDiagnostic(
				"claude_dynamic_budget_converted",
				fmt.Sprintf("model %q does not support a dynamic budget; derived a manual budget from reasoning effort", model),
			))
		}
		percentage := effortPercentage(intent.Effort, adapterBudgetPercentage)
		budget = max(int(*maxTokens)*percentage/100, 1024)
		if uint(budget) >= *maxTokens {
			budget = int(*maxTokens) - 1
		}
	}

	effectiveEffort := intent.Effort
	if intent.BudgetTokens != nil && !capabilities.supportsEffort {
		effectiveEffort = EffortFromBudget(budget)
	} else if effectiveEffort == "" {
		effectiveEffort = EffortFromBudget(budget)
	}
	outputEffort := Effort("")
	if capabilities.supportsEffort && intent.Effort != "" {
		outputEffort = normalizeClaudeEffort(intent.Effort, capabilities)
		effectiveEffort = outputEffort
	}
	thinking := &dto.Thinking{Type: "enabled", BudgetTokens: &budget}
	if intent.IncludeThoughts != nil {
		if *intent.IncludeThoughts {
			thinking.Display = "summarized"
		} else {
			thinking.Display = "omitted"
		}
	}
	return ClaudeRender{
		Thinking:                  thinking,
		OutputEffort:              outputEffort,
		EffectiveEffort:           effectiveEffort,
		ConstrainThinkingSampling: true,
		MaxTokens:                 raisedMaxTokens,
		Diagnostics:               diagnostics,
	}, nil
}

func claudeReasoningDiagnostic(code string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{
		Code:     code,
		Path:     "thinking",
		Message:  message,
		Severity: types.ConversionDiagnosticWarning,
		To:       types.RelayFormatClaude,
	}
}

// ClaudeUsesManualThinking reports whether an exact numeric budget is rendered
// as legacy extended thinking rather than being reduced to adaptive effort.
func ClaudeUsesManualThinking(model string, intent Intent) bool {
	capabilities := claudeCapabilitiesFor(model)
	return capabilities.supportsManual && intent.BudgetTokens != nil && intent.Mode != ModeAdaptive
}

func IsKnownClaudeModel(model string) bool {
	return isKnownClaudeModel(model)
}

func ResolveClaudeDefault(model string, intent Intent) Intent {
	if intent.HasStrength() || !claudeCapabilitiesFor(model).defaultThinking {
		return intent
	}
	intent.Mode = ModeAdaptive
	intent.Effort = EffortHigh
	return intent
}

func normalizeClaudeEffort(effort Effort, capabilities claudeCapabilities) Effort {
	switch effort {
	case EffortMinimal:
		return EffortLow
	case EffortXHigh:
		if capabilities.supportsXHigh {
			return effort
		}
		if capabilities.supportsMax {
			return EffortMax
		}
		return EffortHigh
	case EffortMax:
		if !capabilities.supportsMax {
			return EffortHigh
		}
	}
	return effort
}

func effortPercentage(effort Effort, adapterBudgetPercentage float64) int {
	switch effort {
	case EffortMinimal:
		return 5
	case EffortLow:
		return 20
	case EffortMedium:
		return 50
	case EffortHigh:
		return 80
	case EffortXHigh, EffortMax:
		return 95
	}
	percentage := int(math.Round(adapterBudgetPercentage * 100))
	if percentage <= 0 {
		return 80
	}
	if percentage >= 100 {
		return 99
	}
	return percentage
}
