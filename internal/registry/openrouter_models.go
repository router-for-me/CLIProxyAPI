package registry

import (
	"strings"
)

var staticOpenRouterModels = []*ModelInfo{
	{
		ID:                  "deepseek/deepseek-r1:free",
		Object:              "model",
		OwnedBy:             "deepseek",
		Type:                "openrouter",
		DisplayName:         "DeepSeek R1 (Free)",
		ContextLength:       131072,
		MaxCompletionTokens: 32768,
		Thinking: &ThinkingSupport{
			Levels: []string{"high"},
		},
	},
	{
		ID:                  "deepseek/deepseek-chat",
		Object:              "model",
		OwnedBy:             "deepseek",
		Type:                "openrouter",
		DisplayName:         "DeepSeek V3",
		ContextLength:       131072,
		MaxCompletionTokens: 32768,
	},
	{
		ID:                  "deepseek/deepseek-r1",
		Object:              "model",
		OwnedBy:             "deepseek",
		Type:                "openrouter",
		DisplayName:         "DeepSeek R1",
		ContextLength:       131072,
		MaxCompletionTokens: 32768,
		Thinking: &ThinkingSupport{
			Levels: []string{"high"},
		},
	},
	{
		ID:                  "meta-llama/llama-3.3-70b-instruct:free",
		Object:              "model",
		OwnedBy:             "meta",
		Type:                "openrouter",
		DisplayName:         "Llama 3.3 70B Instruct (Free)",
		ContextLength:       131072,
		MaxCompletionTokens: 8192,
	},
	{
		ID:                  "qwen/qwen-2.5-coder-32b-instruct:free",
		Object:              "model",
		OwnedBy:             "alibaba",
		Type:                "openrouter",
		DisplayName:         "Qwen 2.5 Coder 32B (Free)",
		ContextLength:       131072,
		MaxCompletionTokens: 8192,
	},
	{
		ID:                  "stepfun/step-5-preview",
		Object:              "model",
		OwnedBy:             "stepfun",
		Type:                "openrouter",
		DisplayName:         "StepFun Step 5 Preview",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
	},
	{
		ID:                  "anthropic/claude-3.7-sonnet",
		Object:              "model",
		OwnedBy:             "anthropic",
		Type:                "openrouter",
		DisplayName:         "Claude 3.7 Sonnet",
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	},
	{
		ID:                  "anthropic/claude-3.5-sonnet",
		Object:              "model",
		OwnedBy:             "anthropic",
		Type:                "openrouter",
		DisplayName:         "Claude 3.5 Sonnet",
		ContextLength:       200000,
		MaxCompletionTokens: 8192,
	},
	{
		ID:                  "google/gemini-2.5-pro",
		Object:              "model",
		OwnedBy:             "google",
		Type:                "openrouter",
		DisplayName:         "Gemini 2.5 Pro",
		ContextLength:       1048576,
		MaxCompletionTokens: 65536,
	},
	{
		ID:                  "google/gemini-2.5-flash",
		Object:              "model",
		OwnedBy:             "google",
		Type:                "openrouter",
		DisplayName:         "Gemini 2.5 Flash",
		ContextLength:       1048576,
		MaxCompletionTokens: 65536,
	},
}

// GetOpenRouterModels returns baseline OpenRouter models.
func GetOpenRouterModels() []*ModelInfo {
	return cloneModelInfos(staticOpenRouterModels)
}

// IsOpenRouterTarget checks if a provider/URL targets OpenRouter.
func IsOpenRouterTarget(baseURL, name string) bool {
	u := strings.ToLower(strings.TrimSpace(baseURL))
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(u, "openrouter.ai") || n == "openrouter"
}
