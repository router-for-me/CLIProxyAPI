package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ShouldUseMaxCompletionTokensForModel reports whether the resolved model under an
// OpenAI compatibility configuration has use-max-completion-tokens set to true.
func ShouldUseMaxCompletionTokensForModel(compat *config.OpenAICompatibility, upstreamModel, requestedModel string) bool {
	if compat == nil {
		return false
	}
	if useMCT, matched := openAICompatibilityModelUsesMaxCompletionTokens(compat.Models, upstreamModel); matched {
		return useMCT
	}
	useMCT, _ := openAICompatibilityModelUsesMaxCompletionTokens(compat.Models, requestedModel)
	return useMCT
}

func openAICompatibilityModelUsesMaxCompletionTokens(models []config.OpenAICompatibilityModel, model string) (bool, bool) {
	model = normalizeOpenAICompatibilityModelName(model)
	if model == "" {
		return false, false
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Name)) {
			return models[i].UseMaxCompletionTokens, true
		}
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Alias)) {
			return models[i].UseMaxCompletionTokens, true
		}
	}
	return false, false
}

// NormalizeOpenAIMaxTokens normalizes max_tokens and max_completion_tokens according to the model's preference.
// When useMaxCompletionTokens is true, it ensures max_completion_tokens is set and max_tokens is removed.
// When useMaxCompletionTokens is false, it ensures max_tokens is used and max_completion_tokens is removed.
func NormalizeOpenAIMaxTokens(payload []byte, useMaxCompletionTokens bool) []byte {
	if len(payload) == 0 {
		return payload
	}

	hasMaxTokens := gjson.GetBytes(payload, "max_tokens").Exists()
	hasMaxCompletionTokens := gjson.GetBytes(payload, "max_completion_tokens").Exists()
	if !hasMaxTokens && !hasMaxCompletionTokens {
		return payload
	}

	if useMaxCompletionTokens {
		if hasMaxTokens && !hasMaxCompletionTokens {
			val := gjson.GetBytes(payload, "max_tokens")
			if val.Raw != "" {
				payload, _ = sjson.SetRawBytes(payload, "max_completion_tokens", []byte(val.Raw))
			} else {
				payload, _ = sjson.SetBytes(payload, "max_completion_tokens", val.Value())
			}
		}
		if hasMaxTokens {
			payload, _ = sjson.DeleteBytes(payload, "max_tokens")
		}
	} else {
		if hasMaxCompletionTokens && !hasMaxTokens {
			val := gjson.GetBytes(payload, "max_completion_tokens")
			if val.Raw != "" {
				payload, _ = sjson.SetRawBytes(payload, "max_tokens", []byte(val.Raw))
			} else {
				payload, _ = sjson.SetBytes(payload, "max_tokens", val.Value())
			}
		}
		if hasMaxCompletionTokens {
			payload, _ = sjson.DeleteBytes(payload, "max_completion_tokens")
		}
	}
	return payload
}
