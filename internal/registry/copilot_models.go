package registry

import "time"

const CopilotModelPrefix = "copilot-"

// GenerateCopilotAliases creates copilot- prefixed aliases for explicit routing.
// This allows users to explicitly route to Copilot when model names might conflict
// with other providers (e.g., "copilot-gpt-4o" vs "gpt-4o").
func GenerateCopilotAliases(models []*ModelInfo) []*ModelInfo {
	result := make([]*ModelInfo, 0, len(models)*2)
	result = append(result, models...)

	for _, m := range models {
		alias := *m
		alias.ID = CopilotModelPrefix + m.ID
		alias.DisplayName = m.DisplayName + " (Copilot)"
		alias.Description = m.Description + " - explicit routing alias"
		result = append(result, &alias)
	}

	return result
}

// GetCopilotModels returns a conservative set of fallback models for GitHub Copilot.
// These are used when dynamic model fetching from the Copilot API fails.
func GetCopilotModels() []*ModelInfo {
	now := time.Now().Unix()
	defaultParams := []string{"temperature", "top_p", "max_tokens", "stream", "tools"}

	baseModels := []*ModelInfo{
		{
			ID:                  "gpt-4.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-4.1",
			Description:         "Azure OpenAI model via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-4o",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-4o",
			Description:         "Azure OpenAI model via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 4096,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-41-copilot",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-4.1 Copilot",
			Description:         "Azure OpenAI fine-tuned model via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-5",
			Description:         "Azure OpenAI model via GitHub Copilot",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-5-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-5 mini",
			Description:         "Azure OpenAI model via GitHub Copilot",
			ContextLength:       264000,
			MaxCompletionTokens: 64000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-5-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-5-Codex (Preview)",
			Description:         "OpenAI model via GitHub Copilot (Preview)",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-5.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-5.1",
			Description:         "OpenAI model via GitHub Copilot (Preview)",
			ContextLength:       264000,
			MaxCompletionTokens: 64000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-5.1-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-5.1-Codex",
			Description:         "OpenAI model via GitHub Copilot (Preview)",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gpt-5.1-codex-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "GPT-5.1-Codex-Mini",
			Description:         "OpenAI model via GitHub Copilot (Preview)",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "claude-haiku-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Claude Haiku 4.5",
			Description:         "Anthropic model via GitHub Copilot",
			ContextLength:       144000,
			MaxCompletionTokens: 16000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "claude-opus-4.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Claude Opus 4.1",
			Description:         "Anthropic model via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 16000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "claude-sonnet-4",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Claude Sonnet 4",
			Description:         "Anthropic model via GitHub Copilot",
			ContextLength:       216000,
			MaxCompletionTokens: 16000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "claude-sonnet-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Claude Sonnet 4.5",
			Description:         "Anthropic model via GitHub Copilot",
			ContextLength:       144000,
			MaxCompletionTokens: 16000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "claude-opus-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Claude Opus 4.5 (Preview)",
			Description:         "Anthropic model via GitHub Copilot (Preview)",
			ContextLength:       144000,
			MaxCompletionTokens: 16000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gemini-2.5-pro",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Gemini 2.5 Pro",
			Description:         "Google model via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 64000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "gemini-3-pro-preview",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Gemini 3 Pro (Preview)",
			Description:         "Google model via GitHub Copilot (Preview)",
			ContextLength:       128000,
			MaxCompletionTokens: 64000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "grok-code-fast-1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Grok Code Fast 1",
			Description:         "xAI model via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 64000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "oswe-vscode-prime",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Raptor mini (Preview)",
			Description:         "Azure OpenAI fine-tuned model via GitHub Copilot (Preview)",
			ContextLength:       264000,
			MaxCompletionTokens: 64000,
			SupportedParameters: defaultParams,
		},
		{
			ID:                  "raptor-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "copilot",
			Type:                "copilot",
			DisplayName:         "Raptor mini (Preview)",
			Description:         "Azure OpenAI fine-tuned model via GitHub Copilot (Preview) - alias for oswe-vscode-prime",
			ContextLength:       264000,
			MaxCompletionTokens: 64000,
			SupportedParameters: defaultParams,
		},
	}

	return GenerateCopilotAliases(baseModels)
}
