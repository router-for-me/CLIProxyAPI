package auth

import (
	"bytes"
)

// IsCompletionFormatRecognized reports whether payload uses a wire format the
// empty-completion detection understands (OpenAI chat, OpenAI Responses,
// Anthropic Claude, or Gemini). It supports representative format-contract
// tests without claiming registry-wide executor coverage.
func IsCompletionFormatRecognized(payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return false
	}
	var acc emptyCompletionAccum
	if bytes.Contains(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		acc.evalSSE(trimmed)
	} else {
		acc.evalJSON(trimmed)
	}
	return acc.recognized
}
