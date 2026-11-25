package thinking

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

func extractThinkingFieldsFromConfig(cfg ojson.Result) (ojson.Result, ojson.Result) {
	if !cfg.Exists() || !cfg.IsObject() {
		return ojson.Result{}, ojson.Result{}
	}
	var level, budget ojson.Result
	for _, key := range []string{"thinking_level", "thinkingLevel"} {
		if value := cfg.Get(key); value.Exists() && value.Type == ojson.String {
			if normalized := strings.ToLower(strings.TrimSpace(value.String())); normalized != "" {
				level = ojson.Result{Type: ojson.String, Str: normalized}
				break
			}
		}
	}
	for _, key := range []string{"thinking_budget", "thinkingBudget"} {
		if value := cfg.Get(key); value.Exists() && value.Type == ojson.Number {
			budget = value
			break
		}
	}
	return level, budget
}

func extractThinkingFieldsFromGoogleObject(google ojson.Result) (ojson.Result, ojson.Result) {
	if !google.Exists() || !google.IsObject() {
		return ojson.Result{}, ojson.Result{}
	}
	for _, key := range []string{"thinking_config", "thinkingConfig"} {
		if level, budget := extractThinkingFieldsFromConfig(google.Get(key)); level.Exists() || budget.Exists() {
			return level, budget
		}
	}
	return ojson.Result{}, ojson.Result{}
}

func extractOpenAIGoogleThinkingFields(get func(string) ojson.Result) (ojson.Result, ojson.Result) {
	if extraBody := get("extra_body"); extraBody.Exists() && extraBody.IsObject() {
		if level, budget := extractThinkingFieldsFromGoogleObject(extraBody.Get("google")); level.Exists() || budget.Exists() {
			return level, budget
		}
		if nestedExtra := extraBody.Get("extra_body"); nestedExtra.Exists() && nestedExtra.IsObject() {
			if level, budget := extractThinkingFieldsFromGoogleObject(nestedExtra.Get("google")); level.Exists() || budget.Exists() {
				return level, budget
			}
		}
	}
	if level, budget := extractThinkingFieldsFromGoogleObject(get("google")); level.Exists() || budget.Exists() {
		return level, budget
	}
	return ojson.Result{}, ojson.Result{}
}

func extractOpenAIGenerationThinkingFields(get func(string) ojson.Result) (ojson.Result, ojson.Result) {
	if gen := get("generationConfig"); gen.Exists() && gen.IsObject() {
		for _, key := range []string{"thinkingConfig", "thinking_config"} {
			if level, budget := extractThinkingFieldsFromConfig(gen.Get(key)); level.Exists() || budget.Exists() {
				return level, budget
			}
		}
	}
	if gen := get("generation_config"); gen.Exists() && gen.IsObject() {
		for _, key := range []string{"thinking_config", "thinkingConfig"} {
			if level, budget := extractThinkingFieldsFromConfig(gen.Get(key)); level.Exists() || budget.Exists() {
				return level, budget
			}
		}
	}
	return ojson.Result{}, ojson.Result{}
}

func extractOpenAIGoogleThinkingFallbackFields(get func(string) ojson.Result) (ojson.Result, ojson.Result) {
	if level, budget := extractOpenAIGenerationThinkingFields(get); level.Exists() || budget.Exists() {
		return level, budget
	}
	return extractOpenAIGoogleThinkingFields(get)
}

func extractOpenAIGoogleThinkingFallbackConfigFromGetter(get func(string) ojson.Result) ThinkingConfig {
	level, budget := extractOpenAIGoogleThinkingFallbackFields(get)
	return geminiThinkingConfigFromValues(level, budget)
}

// ApplyOpenAIGoogleThinkingConfigDoc applies thinking_level/thinkingLevel or
// thinking_budget/thinkingBudget from OpenAI Chat compatibility fields
// (generationConfig/generation_config before extra_body.google.thinking_config)
// onto doc at thinkingPath.
func ApplyOpenAIGoogleThinkingConfigDoc(doc *ojson.Doc, rawJSON []byte, thinkingPath string) bool {
	if doc == nil || len(rawJSON) == 0 || !ojson.ValidBytes(rawJSON) || thinkingPath == "" {
		return false
	}
	level, budget := extractOpenAIGoogleThinkingFallbackFields(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(rawJSON, path)
	})
	if level.Exists() {
		if level.Str == "auto" {
			_ = doc.Set(thinkingPath+".thinkingBudget", -1)
		} else {
			_ = doc.Set(thinkingPath+".thinkingLevel", level.Str)
		}
		return true
	}
	if budget.Exists() {
		_ = doc.Set(thinkingPath+".thinkingBudget", budget.Int())
		return true
	}
	return false
}
