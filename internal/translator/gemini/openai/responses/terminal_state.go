package responses

import (
	"strings"

	"github.com/tidwall/gjson"
)

// geminiResponsesUsageTokens is the latest usageMetadata snapshot seen on the
// stream. Gemini reports cumulative counts, so a later frame replaces the
// fields it carries instead of being summed onto the previous ones.
type geminiResponsesUsageTokens struct {
	PromptTokens     int64
	CachedTokens     int64
	CandidatesTokens int64
	ThoughtsTokens   int64
	TotalTokens      int64
	HasUsage         bool
}

// Merge copies every field present in usage. An absent field leaves the
// previously observed value untouched, while an explicit zero overwrites it.
func (u *geminiResponsesUsageTokens) Merge(usage gjson.Result) {
	if !usage.Exists() {
		return
	}
	u.HasUsage = true
	if v := usage.Get("promptTokenCount"); v.Exists() {
		u.PromptTokens = v.Int()
	}
	if v := usage.Get("cachedContentTokenCount"); v.Exists() {
		u.CachedTokens = v.Int()
	}
	if v := usage.Get("candidatesTokenCount"); v.Exists() {
		u.CandidatesTokens = v.Int()
	}
	if v := usage.Get("thoughtsTokenCount"); v.Exists() {
		u.ThoughtsTokens = v.Int()
	}
	if v := usage.Get("totalTokenCount"); v.Exists() {
		u.TotalTokens = v.Int()
	}
}

// geminiResponsesUsageFromRoot prefers the public usageMetadata and falls back
// to the internal cpaUsageMetadata spelling used for non-terminal chunks.
func geminiResponsesUsageFromRoot(root gjson.Result) gjson.Result {
	if um := root.Get("usageMetadata"); um.Exists() {
		return um
	}
	return root.Get("cpaUsageMetadata")
}

// geminiResponsesTerminalState maps an upstream finish reason to the Responses
// terminal event. Only MAX_TOKENS is treated as a truncation; every other
// reason (including an empty one) completes the response.
func geminiResponsesTerminalState(finishReason string) (eventType, status string, incompleteDetails []byte) {
	if strings.EqualFold(strings.TrimSpace(finishReason), "MAX_TOKENS") {
		return "response.incomplete", "incomplete", []byte(`{"reason":"max_output_tokens"}`)
	}
	return "response.completed", "completed", nil
}

func geminiResponsesOutputStatus(finishReason string) string {
	_, status, _ := geminiResponsesTerminalState(finishReason)
	return status
}
