package helps

import (
	"strings"

	"github.com/tiktoken-go/tokenizer"
)

// CodexInputTokenizer selects the local estimator for the effective upstream model.
// Unknown Codex models use the same modern fallback as translated Claude streams.
// This is an estimate, not a claim about an undocumented model's exact encoding.
func CodexInputTokenizer(model string) (tokenizer.Codec, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "ft:gpt-4o"), strings.HasPrefix(model, "ft:gpt-4.1"),
		strings.HasPrefix(model, "gpt-4.1"), strings.HasPrefix(model, "gpt-4.5"),
		strings.HasPrefix(model, "gpt-4o"), strings.HasPrefix(model, "chatgpt-4o"),
		model == "o1", strings.HasPrefix(model, "o1-"),
		model == "o3", strings.HasPrefix(model, "o3-"),
		model == "o4-mini", strings.HasPrefix(model, "o4-mini-"):
		return tokenizer.Get(tokenizer.O200kBase)
	case model == "davinci-002", model == "babbage-002",
		strings.HasPrefix(model, "gpt-4"), strings.HasPrefix(model, "gpt-3"),
		strings.HasPrefix(model, "ft:gpt-4"), strings.HasPrefix(model, "ft:gpt-3.5-turbo"),
		strings.HasPrefix(model, "ft:davinci-002"), strings.HasPrefix(model, "ft:babbage-002"):
		return tokenizer.Get(tokenizer.Cl100kBase)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}
