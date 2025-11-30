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

// GetCopilotModels returns the Copilot models (raptor-mini and oswe-vscode-prime).
func GetCopilotModels() []*ModelInfo {
	now := time.Now().Unix()
	defaultParams := []string{"temperature", "top_p", "max_tokens", "stream", "tools"}

	baseModels := []*ModelInfo{
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
