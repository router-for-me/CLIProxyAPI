package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// IsGeminiTokenEvent reports whether a Google Gemini / Antigravity streaming chunk carries
// substantive output token content (such as text parts, thought traces, or function calls).
// It filters out metadata-only chunks (e.g. standalone usageMetadata) and empty parts.
func IsGeminiTokenEvent(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
	}

	// Strip "data:" SSE prefix if present
	if bytes.HasPrefix(payload, []byte("data:")) {
		prefixLen := len("data:")
		if bytes.HasPrefix(payload, []byte("data: ")) {
			prefixLen = len("data: ")
		}
		payload = bytes.TrimSpace(payload[prefixLen:])
		if len(payload) == 0 {
			return false
		}
	}

	// Terminal error fallback
	if gjson.GetBytes(payload, "error.message").Exists() || gjson.GetBytes(payload, "error").Exists() || gjson.GetBytes(payload, "response.error").Exists() {
		return true
	}

	candidatesNode := gjson.GetBytes(payload, "candidates")
	if !candidatesNode.Exists() {
		candidatesNode = gjson.GetBytes(payload, "response.candidates")
	}
	candidates := candidatesNode.Array()
	if len(candidates) == 0 {
		return false
	}

	for _, candidate := range candidates {
		// Inspect content parts
		parts := candidate.Get("content.parts").Array()
		for _, part := range parts {
			if len(part.Get("text").String()) > 0 {
				return true
			}
			if len(part.Get("thoughtText").String()) > 0 {
				return true
			}
			if thought := part.Get("thought"); thought.Type == gjson.String && len(thought.String()) > 0 {
				return true
			}
			if len(part.Get("functionCall.name").String()) > 0 {
				return true
			}
			if len(part.Get("inlineData.data").String()) > 0 {
				return true
			}
		}

		// Terminal finishReason fallback (e.g., STOP, MAX_TOKENS, SAFETY)
		if len(candidate.Get("finishReason").String()) > 0 {
			return true
		}
	}

	return false
}

// ObserveGeminiTokenEvent inspects a Google Gemini / Antigravity streaming chunk and records TTFT
// if the frame represents the first meaningful token event. It records first-packet arrival time
// as fallback and returns immediately with zero allocations once effective token TTFT is set.
func ObserveGeminiTokenEvent(reporter *UsageReporter, payload []byte) {
	if reporter == nil || len(payload) == 0 {
		return
	}
	if reporter.IsTTFTSet() {
		return
	}
	reporter.ObserveTokenEvent(IsGeminiTokenEvent(payload))
}
