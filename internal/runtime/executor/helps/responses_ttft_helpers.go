package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// IsResponsesTokenEvent reports whether the given OpenAI/xAI Responses API WebSocket or SSE event
// payload carries an actual output token, reasoning trace, tool call argument, or multimodal delta,
// according to the official Responses API streaming / websocket specification plus Codex-compatible private events.
func IsResponsesTokenEvent(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
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
	}

	eventType := gjson.GetBytes(payload, "type").String()
	switch eventType {
	// Substantive text, reasoning, and tool argument streaming deltas
	case "response.reasoning_summary_text.delta",
		"response.reasoning.delta",
		"response.reasoning_text.delta",
		"response.output_text.delta",
		"response.text.delta",
		"response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta",
		"response.code_interpreter_call_code.delta",
		"response.mcp_call_arguments.delta",
		"response.shell_call_command.delta",
		"response.refusal.delta",
		"response.audio.transcript.delta":
		return len(gjson.GetBytes(payload, "delta").String()) > 0

	// Multimodal streaming deltas (audio and image generation preview)
	case "response.audio.delta":
		return len(gjson.GetBytes(payload, "delta").String()) > 0 || len(gjson.GetBytes(payload, "data").String()) > 0
	case "response.image_generation_call.partial_image":
		return len(gjson.GetBytes(payload, "partial_image_b64").String()) > 0

	// Shell call command added event when command is populated upfront
	case "response.shell_call_command.added":
		return len(gjson.GetBytes(payload, "command").String()) > 0

	// Single-chunk / non-stream completed items
	case "response.reasoning_summary_text.done",
		"response.reasoning_text.done",
		"response.output_text.done":
		return len(gjson.GetBytes(payload, "text").String()) > 0
	case "response.refusal.done":
		return len(gjson.GetBytes(payload, "refusal").String()) > 0
	case "response.function_call_arguments.done",
		"response.mcp_call_arguments.done":
		return len(gjson.GetBytes(payload, "arguments").String()) > 0
	case "response.custom_tool_call_input.done":
		return len(gjson.GetBytes(payload, "input").String()) > 0
	case "response.code_interpreter_call_code.done":
		return len(gjson.GetBytes(payload, "code").String()) > 0
	case "response.shell_call_command.done":
		return len(gjson.GetBytes(payload, "command").String()) > 0
	case "response.reasoning_summary_part.done":
		return len(gjson.GetBytes(payload, "part.text").String()) > 0
	case "response.content_part.done":
		return len(gjson.GetBytes(payload, "part.text").String()) > 0 || len(gjson.GetBytes(payload, "part.refusal").String()) > 0
	case "response.output_item.done":
		itemType := gjson.GetBytes(payload, "item.type").String()
		switch itemType {
		case "function_call":
			return len(gjson.GetBytes(payload, "item.arguments").String()) > 0
		case "custom_tool_call":
			return len(gjson.GetBytes(payload, "item.input").String()) > 0
		case "message":
			for _, content := range gjson.GetBytes(payload, "item.content").Array() {
				if len(content.Get("text").String()) > 0 || len(content.Get("refusal").String()) > 0 {
					return true
				}
			}
		}
		return false

	// Terminal events fallback to guarantee TTFT is never missed on truncated, failed, or fast turns
	case "response.completed",
		"response.done",
		"response.incomplete",
		"response.failed",
		"error":
		return true

	// All other container lifecycles (added, created, in_progress, search state machines, metadata)
	default:
		return false
	}
}

// ObserveResponsesTokenEvent inspects a Responses API frame payload and records TTFT if the frame
// represents the first meaningful token event. It records the first packet arrival time as a fallback
// and exits immediately with zero allocations once effective token TTFT is set.
func ObserveResponsesTokenEvent(reporter *UsageReporter, payload []byte) {
	if reporter == nil || len(payload) == 0 {
		return
	}
	if reporter.IsTTFTSet() {
		return
	}
	reporter.ObserveTokenEvent(IsResponsesTokenEvent(payload))
}
