package util

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

// GetThinkingTextFromJSON extracts thinking text from a raw JSON string.
func GetThinkingTextFromJSON(jsonStr string) string {
	return GetThinkingText(gjson.Parse(jsonStr))
}

// SanitizeThinkingPart normalizes a thinking part to a canonical form.
// Strips cache_control and other non-essential fields.
// Returns the sanitized part as JSON string.
func SanitizeThinkingPart(part gjson.Result) string {
	// Gemini-style: { thought: true, text, thoughtSignature }
	if part.Get("thought").Bool() {
		result := `{"thought":true}`
		if text := GetThinkingText(part); text != "" {
			result, _ = sjson.Set(result, "text", text)
		}
		if sig := part.Get("thoughtSignature"); sig.Exists() && sig.Type == gjson.String {
			result, _ = sjson.Set(result, "thoughtSignature", sig.String())
		}
		return result
	}

	// Anthropic-style: { type: "thinking", thinking, signature }
	if part.Get("type").String() == "thinking" || part.Get("thinking").Exists() {
		result := `{"type":"thinking"}`
		if text := GetThinkingText(part); text != "" {
			result, _ = sjson.Set(result, "thinking", text)
		}
		if sig := part.Get("signature"); sig.Exists() && sig.Type == gjson.String {
			result, _ = sjson.Set(result, "signature", sig.String())
		}
		return result
	}

	// Not a thinking part, return as-is but strip cache_control
	return StripCacheControl(part.Raw)
}

// StripCacheControl removes cache_control and providerOptions from a JSON object.
func StripCacheControl(jsonStr string) string {
	result := jsonStr
	result, _ = sjson.Delete(result, "cache_control")
	result, _ = sjson.Delete(result, "providerOptions")
	return result
}
