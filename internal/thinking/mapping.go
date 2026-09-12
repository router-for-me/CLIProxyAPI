package thinking

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelmatch"
)

// EffortMappingRule maps one canonical effort to a forced destination effort.
type EffortMappingRule struct {
	From           string
	To             string
	SourceProtocol string
	TargetProtocol string
	TargetProvider string
	Models         []string
}

// ApplyOptions supplies optional runtime policy context to ApplyThinkingWithOptions.
type ApplyOptions struct {
	EffortMapping  []EffortMappingRule
	RequestedModel string
}

type effortMappingContext struct {
	SourceProtocol string
	TargetProtocol string
	TargetProvider string
	ResolvedModel  string
	RequestedModel string
}

func selectEffortMapping(rules []EffortMappingRule, effort string, context effortMappingContext) (ThinkingConfig, bool) {
	effort = normalizeMappingValue(effort)
	if effort == "" {
		return ThinkingConfig{}, false
	}

	for _, rule := range rules {
		if normalizeMappingValue(rule.From) != effort {
			continue
		}
		if !optionalMappingScopeMatches(rule.SourceProtocol, context.SourceProtocol) ||
			!optionalMappingScopeMatches(rule.TargetProtocol, context.TargetProtocol) ||
			!optionalMappingScopeMatches(rule.TargetProvider, context.TargetProvider) ||
			!mappingModelsMatch(rule.Models, context.ResolvedModel, context.RequestedModel) {
			continue
		}
		return mappedEffortConfig(rule.To), true
	}
	return ThinkingConfig{}, false
}

func normalizeMappingValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func optionalMappingScopeMatches(scope, value string) bool {
	scope = normalizeMappingValue(scope)
	return scope == "" || scope == normalizeMappingValue(value)
}

func mappingModelsMatch(patterns []string, models ...string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, model := range models {
		candidate := strings.TrimSpace(ParseSuffix(model).ModelName)
		if candidate == "" {
			continue
		}
		for _, pattern := range patterns {
			if modelmatch.MatchFold(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

func mappedEffortConfig(effort string) ThinkingConfig {
	effort = normalizeMappingValue(effort)
	switch effort {
	case string(LevelNone):
		return ThinkingConfig{Mode: ModeNone}
	case string(LevelAuto):
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	default:
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(effort)}
	}
}

func canonicalNamedEffort(config ThinkingConfig) (string, bool) {
	switch config.Mode {
	case ModeNone:
		return string(LevelNone), true
	case ModeAuto:
		return string(LevelAuto), true
	case ModeLevel:
		effort := normalizeMappingValue(string(config.Level))
		if _, ok := ConvertLevelToBudget(effort); !ok {
			return "", false
		}
		return effort, true
	case ModeBudget:
		effort, ok := ConvertBudgetToLevel(config.Budget)
		return normalizeMappingValue(effort), ok
	default:
		return "", false
	}
}
