package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeOpenAICompletionTokenLimit moves the caller's max_tokens value to
// max_completion_tokens when the selected upstream model explicitly requires it.
// Apply this after payload rules and only to Chat Completions requests.
func NormalizeOpenAICompletionTokenLimit(payload []byte, compat *config.OpenAICompatibility, upstreamModel string) []byte {
	if compat == nil {
		return payload
	}
	legacy := gjson.GetBytes(payload, "max_tokens")
	if !legacy.Exists() {
		return payload
	}
	modelName := normalizeOpenAICompatibilityModelName(upstreamModel)
	if modelName == "" {
		return payload
	}
	// The executor receives the resolved upstream name. Matching a client alias
	// could apply one pool member's setting to a different upstream model.
	for _, model := range compat.Models {
		if !strings.EqualFold(modelName, normalizeOpenAICompatibilityModelName(model.Name)) {
			continue
		}
		if !model.UseMaxCompletionTokens {
			return payload
		}
		out := payload
		if !gjson.GetBytes(out, "max_completion_tokens").Exists() {
			var err error
			out, err = sjson.SetRawBytes(out, "max_completion_tokens", []byte(legacy.Raw))
			if err != nil {
				return payload
			}
		}
		if updated, err := sjson.DeleteBytes(out, "max_tokens"); err == nil {
			return updated
		}
		return payload
	}
	return payload
}
