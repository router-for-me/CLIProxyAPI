package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// isSubstantiveAudio reports whether a Chat Completions audio node carries output
// (base64 data, transcript, id, or a non-empty string payload).
func isSubstantiveAudio(node gjson.Result) bool {
	if !node.Exists() || node.Type == gjson.Null {
		return false
	}
	if node.Type == gjson.String {
		return len(node.String()) > 0
	}
	if !node.IsObject() {
		return false
	}
	for _, path := range []string{"data", "transcript", "id", "audio"} {
		if len(node.Get(path).String()) > 0 {
			return true
		}
	}
	return false
}

func hasSubstantiveAudioContentParts(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "audio", "output_audio":
			if isSubstantiveAudio(part) || isSubstantiveAudio(part.Get("audio")) {
				return true
			}
		}
	}
	return false
}

func hasSubstantiveAudioOutput(node gjson.Result) bool {
	if isSubstantiveAudio(node.Get("audio")) || isSubstantiveAudio(node.Get("output_audio")) {
		return true
	}
	return hasSubstantiveAudioContentParts(node.Get("content"))
}

// IsChatTokenEvent reports whether the given OpenAI-compatible Chat Completions SSE or JSON chunk
// carries substantive output token content (text, audio, reasoning, refusal, or tool-call arguments).
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
			if hasSubstantiveAudioOutput(delta) {
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
			if hasSubstantiveAudioOutput(message) {
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
