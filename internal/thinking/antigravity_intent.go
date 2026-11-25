package thinking

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// Match the request converters' precedence without using their inferred defaults.
func extractAntigravityOpenAIThinking(body []byte) ThinkingConfig {
	root := ojson.ParseBytes(body)
	generation := firstThinkingField(root, "generationConfig", "generation_config")
	level := generation.Get("thinkingConfig.thinkingLevel")
	budget := generation.Get("thinkingConfig.thinkingBudget")
	applyAntigravityRawEffort(root.Get("reasoning_effort").String(), &level, &budget)
	if explicit := firstThinkingField(generation, "thinkingConfig.thinking_level", "thinking_config.thinking_level", "thinking_config.thinkingLevel"); explicit.Exists() {
		level = explicit
	}
	if explicit := firstThinkingField(generation, "thinkingConfig.thinking_budget", "thinking_config.thinking_budget", "thinking_config.thinkingBudget"); explicit.Exists() {
		budget = explicit
	}
	if !level.Exists() && !budget.Exists() {
		level, budget = extractOpenAIGoogleThinkingFields(root.Get)
	}
	return geminiThinkingConfigFromValues(level, budget)
}

func extractAntigravityInteractionsThinking(body []byte) ThinkingConfig {
	root := ojson.ParseBytes(body)
	generation := firstThinkingField(root, "generation_config", "generationConfig")
	level := firstThinkingField(generation, "thinking_level", "thinkingLevel", "thinking_config.thinking_level", "thinking_config.thinkingLevel", "thinkingConfig.thinking_level", "thinkingConfig.thinkingLevel")
	budget := firstThinkingField(generation, "thinking_budget", "thinkingBudget", "thinking_config.thinking_budget", "thinking_config.thinkingBudget", "thinkingConfig.thinking_budget", "thinkingConfig.thinkingBudget")
	effort := strings.TrimSpace(root.Get("reasoning.effort").String())
	if effort == "" {
		effort = root.Get("reasoning.thinking_level").String()
	}
	applyAntigravityRawEffort(effort, &level, &budget)
	return geminiThinkingConfigFromValues(level, budget)
}

func firstThinkingField(root ojson.Result, paths ...string) ojson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}

func applyAntigravityRawEffort(effort string, level, budget *ojson.Result) {
	switch value := strings.ToLower(strings.TrimSpace(effort)); value {
	case "":
	case "auto":
		*budget = ojson.Result{Type: ojson.Number, Raw: "-1", Num: -1}
	default:
		*level = ojson.Result{Type: ojson.String, Str: value}
	}
}
