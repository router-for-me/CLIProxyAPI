// Package reasoning re-exports the pure model-name effort-suffix helpers,
// which moved to the conversion kit (relaykit/relayconvert/reasoning) as part
// of the relaykit extraction. Host code keeps importing this path unchanged.
package reasoning

import (
	"strings"

	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/setting/model_setting"
)

var (
	EffortSuffixes           = kitreasoning.EffortSuffixes
	OpenAIEffortSuffixes     = kitreasoning.OpenAIEffortSuffixes
	DeepSeekV4EffortSuffixes = kitreasoning.DeepSeekV4EffortSuffixes
)

var (
	TrimEffortSuffixWithSuffixes  = kitreasoning.TrimEffortSuffixWithSuffixes
	ParseDeepSeekV4ThinkingSuffix = kitreasoning.ParseDeepSeekV4ThinkingSuffix
	TrimGeminiThinkingSuffix      = kitreasoning.TrimGeminiThinkingSuffix
)

// ParseOpenAIReasoningEffortFromModelSuffix applies RelayKit's positive family
// whitelist and the host EffortTailModelIDs escape hatch so real model IDs
// such as gpt-5.1-codex-max remain opaque.
func ParseOpenAIReasoningEffortFromModelSuffix(modelName string) (string, string) {
	return kitreasoning.ParseOpenAIReasoningEffortFromModelSuffix(modelName, model_setting.ShouldPreserveEffortTail)
}

// ParseLegacyModelSuffix parses the old naked aliases only for positively
// matched GPT/o-series, Claude, and Gemini model families. The provider prefix
// before the final path segment is kept opaque.
func ParseLegacyModelSuffix(modelName string, allowClaudeThinkingAlias bool, allowGeminiThinkingAlias bool) (string, kitreasoning.Intent, bool, error) {
	prefix, bare := splitModelNamespace(modelName)

	var (
		base   string
		intent kitreasoning.Intent
		found  bool
		err    error
	)
	switch {
	case strings.HasPrefix(bare, "claude-"):
		base, intent, found, err = kitreasoning.ParseClaudeModelSuffix(bare, allowClaudeThinkingAlias)
	case strings.HasPrefix(bare, "gemini-"):
		base, intent, found, err = kitreasoning.ParseGeminiModelSuffix(bare, allowGeminiThinkingAlias)
	default:
		effort, openAIBase := ParseOpenAIReasoningEffortFromModelSuffix(bare)
		if effort == "" {
			return modelName, kitreasoning.Intent{}, false, nil
		}
		parsedEffort, parseErr := kitreasoning.ParseEffort(effort)
		if parseErr != nil {
			return modelName, kitreasoning.Intent{}, false, parseErr
		}
		mode := kitreasoning.ModeEnabled
		if parsedEffort == kitreasoning.EffortNone {
			mode = kitreasoning.ModeDisabled
		}
		base = openAIBase
		intent = kitreasoning.Intent{Mode: mode, Effort: parsedEffort, Source: kitreasoning.SourceSuffix}
		found = true
	}
	if err != nil || !found {
		return modelName, kitreasoning.Intent{}, false, err
	}
	return prefix + base, intent, true, nil
}

// BaseModelName strips explicit model modifiers and any enabled legacy alias.
// Names on the thinking-suffix blacklist stay verbatim, including @ tails.
// Malformed legacy aliases stay intact so request conversion can report the
// precise validation error later.
func BaseModelName(modelName string) string {
	if model_setting.ShouldPreserveThinkingSuffix(modelName) {
		return modelName
	}
	base := kitreasoning.ParseModelModifiers(modelName).Base
	if model_setting.ShouldPreserveThinkingSuffix(base) {
		return base
	}
	legacyBase, _, found, err := ParseLegacyModelSuffix(
		base,
		model_setting.GetClaudeSettings().ThinkingAdapterEnabled,
		model_setting.GetGeminiSettings().ThinkingAdapterEnabled,
	)
	if err != nil {
		return base
	}
	if found {
		return legacyBase
	}
	return base
}

func splitModelNamespace(modelName string) (string, string) {
	if slash := strings.LastIndex(modelName, "/"); slash >= 0 {
		return modelName[:slash+1], modelName[slash+1:]
	}
	return "", modelName
}

// CanonicalBillingModelNames returns specificity-descending canonical billing
// name candidates (without the raw request name or the bare base). Explicit
// @ modifiers and legacy aliases normalize through the same Intent, so order,
// duplicates, and case do not matter. Temperature and topp never appear.
func CanonicalBillingModelNames(modelName string) []string {
	if model_setting.ShouldPreserveThinkingSuffix(modelName) {
		return nil
	}
	spec := kitreasoning.ParseModelModifiers(modelName)
	base := spec.Base
	intent, hasThinking := billingIntentFromModifiers(spec)

	if !model_setting.ShouldPreserveThinkingSuffix(base) {
		legacyBase, legacyIntent, found, err := ParseLegacyModelSuffix(
			base,
			model_setting.GetClaudeSettings().ThinkingAdapterEnabled,
			model_setting.GetGeminiSettings().ThinkingAdapterEnabled,
		)
		if err == nil && found {
			base = legacyBase
			if !hasThinking {
				intent = legacyIntent
				hasThinking = true
			}
		}
	}
	if !hasThinking {
		return nil
	}
	return canonicalNamesFromIntent(base, intent)
}

func billingIntentFromModifiers(spec kitreasoning.ModelModifierSpec) (kitreasoning.Intent, bool) {
	last := make(map[string]int, len(spec.Modifiers))
	for index, modifier := range spec.Modifiers {
		last[modifier.Key] = index
	}

	var (
		intent      kitreasoning.Intent
		hasThinking bool
	)
	for index, modifier := range spec.Modifiers {
		if last[modifier.Key] != index {
			continue
		}
		switch modifier.Key {
		case "thinking":
			parsed, ok := kitreasoning.ParseThinkingModifier(modifier.Value)
			if !ok {
				continue
			}
			if hasThinking && parsed.Mode != kitreasoning.ModeDisabled && parsed.BudgetTokens == nil {
				intent.Mode = parsed.Mode
				intent.Source = kitreasoning.SourceSuffix
			} else if hasThinking && parsed.Mode != kitreasoning.ModeDisabled {
				intent.Mode = parsed.Mode
				intent.BudgetTokens = parsed.BudgetTokens
				intent.BudgetSource = parsed.BudgetSource
				intent.Source = kitreasoning.SourceSuffix
			} else {
				intent = parsed
			}
			hasThinking = true
		case "effort":
			effort, err := kitreasoning.ParseEffort(modifier.Value)
			if err != nil || effort == "" {
				continue
			}
			if effort == kitreasoning.EffortNone {
				intent = kitreasoning.Intent{Mode: kitreasoning.ModeDisabled, Effort: kitreasoning.EffortNone, Source: kitreasoning.SourceSuffix}
			} else {
				if intent.Mode == kitreasoning.ModeUnset || intent.Mode == kitreasoning.ModeDisabled {
					intent.Mode = kitreasoning.ModeEnabled
				}
				intent.Effort = effort
				intent.Source = kitreasoning.SourceSuffix
			}
			hasThinking = true
		}
	}
	return intent, hasThinking
}

func canonicalNamesFromIntent(base string, intent kitreasoning.Intent) []string {
	thinking, effort, ok := normalizeBillingThinking(intent)
	if !ok || base == "" {
		return nil
	}

	var names []string
	if effort != "" {
		names = append(names, base+"@effort:"+effort+"@thinking:"+thinking)
	}
	thinkingForm := base + "@thinking:" + thinking
	if thinkingForm != base {
		names = append(names, thinkingForm)
	}

	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || name == base {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func normalizeBillingThinking(intent kitreasoning.Intent) (thinking string, effort string, ok bool) {
	if intent.Effort == kitreasoning.EffortNone || intent.Mode == kitreasoning.ModeDisabled {
		return "off", "", true
	}
	if intent.BudgetTokens != nil && *intent.BudgetTokens == 0 {
		return "off", "", true
	}
	if intent.Effort != "" && intent.Effort != kitreasoning.EffortNone {
		return "on", strings.ToLower(string(intent.Effort)), true
	}
	if intent.Mode == kitreasoning.ModeEnabled || intent.Mode == kitreasoning.ModeAdaptive || intent.BudgetTokens != nil {
		return "on", "", true
	}
	return "", "", false
}
