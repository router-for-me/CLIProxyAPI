package thinking

import (
	"github.com/tidwall/gjson"
)

// GetThinkingText extracts the thinking text from a content part.
// Handles various formats:
// - Simple string: { "thinking": "text" } or { "text": "text" }
// - Wrapped object: { "thinking": { "text": "text", "cache_control": {...} } }
// - Gemini-style: { "thought": true, "text": "text" }
// Returns the extracted text string.
func GetThinkingText(part gjson.Result) string {
	// Try direct text field first (Gemini-style)
	if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
		return text.String()
	}

	// Try thinking field
	thinkingField := part.Get("thinking")
	if !thinkingField.Exists() {
		return ""
	}

	// thinking is a string
	if thinkingField.Type == gjson.String {
		return thinkingField.String()
	}

	// thinking is an object with inner text/thinking
	if thinkingField.IsObject() {
		if inner := thinkingField.Get("text"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
		if inner := thinkingField.Get("thinking"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
	}

	return ""
}
