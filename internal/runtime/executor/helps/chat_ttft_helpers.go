package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// IsChatTokenEvent reports whether the given OpenAI-compatible Chat Completions SSE or JSON chunk
// carries substantive output token content (such as text delta, reasoning content, or tool call arguments).
// It filters out container metadata, role announcements, and empty delta frames.
func IsChatTokenEvent(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
	}

	// Handle SSE stream terminal marker
	if bytes.Equal(payload, []byte("data: [DONE]")) || bytes.Equal(payload, []byte("[DONE]")) {
		return true
	}

	// Handle SSE line format (e.g., "data: {...}")
	if bytes.HasPrefix(payload, []byte("data:")) {
		prefixLen := len("data:")
		if bytes.HasPrefix(payload, []byte("data: ")) {
			prefixLen = len("data: ")
		}
		payload = bytes.TrimSpace(payload[prefixLen:])
		if len(payload) == 0 {
			return false
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			return true
		}
	}

	// Terminal error envelope fallback
	if gjson.GetBytes(payload, "error.message").Exists() || gjson.GetBytes(payload, "error").Exists() {
		return true
	}

	choices := gjson.GetBytes(payload, "choices").Array()
	if len(choices) == 0 {
		return false
	}

	for _, choice := range choices {
		// Streaming delta checks
		delta := choice.Get("delta")
		if delta.Exists() {
			if len(delta.Get("content").String()) > 0 {
				return true
			}
			if len(delta.Get("reasoning_content").String()) > 0 {
				return true
			}
			if len(delta.Get("reasoning").String()) > 0 {
				return true
			}
			if len(delta.Get("refusal").String()) > 0 {
				return true
			}
			for _, tc := range delta.Get("tool_calls").Array() {
				if len(tc.Get("function.arguments").String()) > 0 || len(tc.Get("function.name").String()) > 0 {
					return true
				}
				if len(tc.Get("custom.input").String()) > 0 {
					return true
				}
			}
		}

		// Non-streaming / completed message fallback
		message := choice.Get("message")
		if message.Exists() {
			if len(message.Get("content").String()) > 0 {
				return true
			}
			if len(message.Get("reasoning_content").String()) > 0 {
				return true
			}
			if len(message.Get("refusal").String()) > 0 {
				return true
			}
			for _, tc := range message.Get("tool_calls").Array() {
				if len(tc.Get("function.arguments").String()) > 0 || len(tc.Get("function.name").String()) > 0 {
					return true
				}
			}
		}

		// Finish reason terminal fallback
		if len(choice.Get("finish_reason").String()) > 0 {
			return true
		}
	}

	return false
}

// ObserveChatTokenEvent inspects an OpenAI Chat Completions chunk and records TTFT if the frame
// represents the first meaningful token event. It records first-packet arrival time as fallback
// and returns immediately with zero allocations once effective token TTFT is set.
func ObserveChatTokenEvent(reporter *UsageReporter, payload []byte) {
	if reporter == nil || len(payload) == 0 {
		return
	}
	if reporter.IsTTFTSet() {
		return
	}
	reporter.ObserveTokenEvent(IsChatTokenEvent(payload))
}
