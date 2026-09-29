package helper

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type parsedModelModifiers struct {
	base           string
	hasSyntax      bool
	intent         reasoning.Intent
	hasThinking    bool
	temperature    *float64
	topP           *float64
	hasTemperature bool
	hasTopP        bool
	diagnostics    []types.ConversionDiagnostic
}

const modelModifierExemptionHint = `If this segment is part of the real model name, add the model to the "Models that skip thinking suffix processing" setting (re: regex entries are supported)`

func modelModifierClientError(message string) error {
	return fmt.Errorf("%s. %s", message, modelModifierExemptionHint)
}

func parseExplicitModelModifiers(modelName string) (parsedModelModifiers, error) {
	spec := reasoning.ParseModelModifiers(modelName)
	parsed := parsedModelModifiers{base: spec.Base, hasSyntax: spec.HasModifiers()}
	last := make(map[string]int, len(spec.Modifiers))

	for index, modifier := range spec.Modifiers {
		if _, duplicate := last[modifier.Key]; duplicate {
			parsed.diagnostics = append(parsed.diagnostics, modelModifierDiagnostic(
				"duplicate_model_modifier",
				modifier.Key,
				fmt.Sprintf("model modifier %q is repeated; the rightmost value is used", modifier.Key),
			))
		}
		last[modifier.Key] = index
	}

	for index, modifier := range spec.Modifiers {
		if last[modifier.Key] != index {
			continue
		}
		switch modifier.Key {
		case "thinking":
			intent, ok := reasoning.ParseThinkingModifier(modifier.Value)
			if !ok {
				return parsedModelModifiers{}, modelModifierClientError(
					fmt.Sprintf("invalid thinking modifier value %q", modifier.Value),
				)
			}
			if parsed.hasThinking && intent.Mode != reasoning.ModeDisabled && intent.BudgetTokens == nil {
				parsed.intent.Mode = intent.Mode
				parsed.intent.Source = reasoning.SourceSuffix
			} else if parsed.hasThinking && intent.Mode != reasoning.ModeDisabled {
				parsed.intent.Mode = intent.Mode
				parsed.intent.BudgetTokens = intent.BudgetTokens
				parsed.intent.BudgetSource = intent.BudgetSource
				parsed.intent.Source = reasoning.SourceSuffix
			} else {
				parsed.intent = intent
			}
			parsed.hasThinking = true
		case "effort":
			effort, err := reasoning.ParseEffort(modifier.Value)
			if err != nil || effort == "" {
				return parsedModelModifiers{}, modelModifierClientError(
					fmt.Sprintf("invalid effort modifier value %q: must be one of none/low/medium/high/xhigh/max", modifier.Value),
				)
			}
			if effort == reasoning.EffortNone {
				parsed.intent = reasoning.Intent{Mode: reasoning.ModeDisabled, Effort: reasoning.EffortNone, Source: reasoning.SourceSuffix}
			} else {
				if parsed.intent.Mode == reasoning.ModeUnset || parsed.intent.Mode == reasoning.ModeDisabled {
					parsed.intent.Mode = reasoning.ModeEnabled
				}
				parsed.intent.Effort = effort
				parsed.intent.Source = reasoning.SourceSuffix
			}
			parsed.hasThinking = true
		case "temperature":
			value, ok := parseFiniteFloat(modifier.Value)
			if !ok {
				return parsedModelModifiers{}, modelModifierClientError(
					fmt.Sprintf("invalid temperature modifier value %q: must be a finite number", modifier.Value),
				)
			}
			parsed.temperature = &value
			parsed.hasTemperature = true
		case "topp":
			value, ok := parseFiniteFloat(modifier.Value)
			if !ok {
				return parsedModelModifiers{}, modelModifierClientError(
					fmt.Sprintf("invalid topp modifier value %q: must be a finite number", modifier.Value),
				)
			}
			parsed.topP = &value
			parsed.hasTopP = true
		default:
			return parsedModelModifiers{}, modelModifierClientError(
				fmt.Sprintf("unsupported model modifier %q", modifier.Key),
			)
		}
	}

	return parsed, nil
}

func parseFiniteFloat(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func extractTemperature(req dto.Request) (float64, bool) {
	switch request := req.(type) {
	case *dto.GeneralOpenAIRequest:
		if request != nil && request.Temperature != nil {
			return *request.Temperature, true
		}
	case *dto.OpenAIResponsesRequest:
		if request != nil && request.Temperature != nil {
			return *request.Temperature, true
		}
	case *dto.ClaudeRequest:
		if request != nil && request.Temperature != nil {
			return *request.Temperature, true
		}
	case *dto.GeminiChatRequest:
		if request != nil && request.GenerationConfig.Temperature != nil {
			return *request.GenerationConfig.Temperature, true
		}
	}
	return 0, false
}

func extractTopP(req dto.Request) (float64, bool) {
	switch request := req.(type) {
	case *dto.GeneralOpenAIRequest:
		if request != nil && request.TopP != nil {
			return *request.TopP, true
		}
	case *dto.OpenAIResponsesRequest:
		if request != nil && request.TopP != nil {
			return *request.TopP, true
		}
	case *dto.ClaudeRequest:
		if request != nil && request.TopP != nil {
			return *request.TopP, true
		}
	case *dto.GeminiChatRequest:
		if request != nil && request.GenerationConfig.TopP != nil {
			return *request.GenerationConfig.TopP, true
		}
	}
	return 0, false
}

func modelModifierDiagnostic(code string, key string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{
		Code:     code,
		Path:     "model.@" + key,
		Message:  message,
		Severity: types.ConversionDiagnosticWarning,
	}
}

func applyModelControls(req dto.Request, parsed parsedModelModifiers) error {
	if req == nil {
		return nil
	}

	switch request := req.(type) {
	case *dto.GeneralOpenAIRequest:
		if parsed.hasTemperature {
			request.Temperature = parsed.temperature
		}
		if parsed.hasTopP {
			request.TopP = parsed.topP
		}
		if parsed.hasThinking {
			request.ReasoningConversion = reasoning.StateFromIntent(parsed.intent)
			reasoningConfig := make(map[string]any)
			if len(request.Reasoning) > 0 {
				if common.GetJsonType(request.Reasoning) != "object" {
					return fmt.Errorf("OpenAI reasoning must be a JSON object")
				}
				if err := common.Unmarshal(request.Reasoning, &reasoningConfig); err != nil {
					return fmt.Errorf("invalid OpenAI reasoning config: %w", err)
				}
			}
			if parsed.intent.BudgetTokens != nil {
				reasoningConfig["enabled"] = parsed.intent.Mode != reasoning.ModeDisabled
				reasoningConfig["max_tokens"] = *parsed.intent.BudgetTokens
				delete(reasoningConfig, "effort")
				request.ReasoningEffort = ""
			} else {
				delete(reasoningConfig, "enabled")
				delete(reasoningConfig, "effort")
				delete(reasoningConfig, "max_tokens")
				request.ReasoningEffort = ""
				if parsed.intent.Effort != "" {
					request.ReasoningEffort = string(parsed.intent.Effort)
				}
			}
			if len(reasoningConfig) == 0 {
				request.Reasoning = nil
			} else {
				encoded, err := common.Marshal(reasoningConfig)
				if err != nil {
					return err
				}
				request.Reasoning = encoded
			}
		}
	case *dto.OpenAIResponsesRequest:
		if parsed.hasTemperature {
			request.Temperature = parsed.temperature
		}
		if parsed.hasTopP {
			request.TopP = parsed.topP
		}
		if parsed.hasThinking {
			request.ReasoningConversion = reasoning.StateFromIntent(parsed.intent)
			if parsed.intent.Effort != "" {
				if request.Reasoning == nil {
					request.Reasoning = &dto.Reasoning{}
				}
				request.Reasoning.Effort = string(parsed.intent.Effort)
			} else if request.Reasoning != nil && parsed.intent.BudgetTokens == nil {
				request.Reasoning.Effort = ""
			}
		}
	case *dto.ClaudeRequest:
		if parsed.hasTemperature {
			request.Temperature = parsed.temperature
		}
		if parsed.hasTopP {
			request.TopP = parsed.topP
		}
		if parsed.hasThinking {
			request.Thinking = nil
			if len(request.OutputConfig) > 0 && common.GetJsonType(request.OutputConfig) == "object" {
				var output map[string]any
				if err := common.Unmarshal(request.OutputConfig, &output); err == nil {
					delete(output, "effort")
					encoded, err := common.Marshal(output)
					if err != nil {
						return err
					}
					request.OutputConfig = encoded
				}
			}
		}
	case *dto.GeminiChatRequest:
		if parsed.hasTemperature {
			request.GenerationConfig.Temperature = parsed.temperature
		}
		if parsed.hasTopP {
			request.GenerationConfig.TopP = parsed.topP
		}
		if parsed.hasThinking {
			request.GenerationConfig.ThinkingConfig = nil
		}
	}
	return nil
}
