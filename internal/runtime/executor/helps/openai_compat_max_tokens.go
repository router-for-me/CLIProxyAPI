package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
	doc := ojson.NewDoc(payload)
	if !NormalizeOpenAIMaxTokensDoc(doc, useMaxCompletionTokens) {
		return payload
	}
	return doc.Bytes()
}

// NormalizeOpenAIMaxTokensDoc normalizes max_tokens and max_completion_tokens in place on doc.
func NormalizeOpenAIMaxTokensDoc(doc *ojson.Doc, useMaxCompletionTokens bool) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}

	maxTokens := doc.Get("max_tokens")
	maxCompletionTokens := doc.Get("max_completion_tokens")
	hasMaxTokens := maxTokens.Exists()
	hasMaxCompletionTokens := maxCompletionTokens.Exists()
	if !hasMaxTokens && !hasMaxCompletionTokens {
		return false
	}

	modified := false
	if useMaxCompletionTokens {
		if hasMaxTokens && !hasMaxCompletionTokens {
			if maxTokens.Raw != "" {
				if errSet := doc.SetRaw("max_completion_tokens", maxTokens.Raw); errSet == nil {
					modified = true
				}
			} else {
				if errSet := doc.Set("max_completion_tokens", maxTokens.Value()); errSet == nil {
					modified = true
				}
			}
		}
		if hasMaxTokens {
			if errDel := doc.Delete("max_tokens"); errDel == nil {
				modified = true
			}
		}
	} else {
		if hasMaxCompletionTokens && !hasMaxTokens {
			if maxCompletionTokens.Raw != "" {
				if errSet := doc.SetRaw("max_tokens", maxCompletionTokens.Raw); errSet == nil {
					modified = true
				}
			} else {
				if errSet := doc.Set("max_tokens", maxCompletionTokens.Value()); errSet == nil {
					modified = true
				}
			}
		}
		if hasMaxCompletionTokens {
			if errDel := doc.Delete("max_completion_tokens"); errDel == nil {
				modified = true
			}
		}
	}
	return modified
}
