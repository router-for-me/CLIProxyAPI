// Package claude provides streaming SSE event building for Claude format.
// This package handles the construction of Claude-compatible Server-Sent Events (SSE)
// for streaming responses from Kiro API.
package claude

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// BuildClaudeMessageStartEvent creates the message_start SSE event
func BuildClaudeMessageStartEvent(model string, inputTokens int64) []byte {
	event := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            "msg_" + uuid.New().String()[:24],
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"model":         model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]interface{}{"input_tokens": inputTokens, "output_tokens": 0},
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: message_start\ndata: " + string(result))
}

// BuildClaudeContentBlockStartEvent creates a content_block_start SSE event
func BuildClaudeContentBlockStartEvent(index int, blockType, toolUseID, toolName string) []byte {
	var contentBlock map[string]interface{}
	switch blockType {
	case "tool_use":
		contentBlock = map[string]interface{}{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  toolName,
			"input": map[string]interface{}{},
		}
	case "thinking":
		contentBlock = map[string]interface{}{
			"type":     "thinking",
			"thinking": "",
		}
	default:
		contentBlock = map[string]interface{}{
			"type": "text",
			"text": "",
		}
	}

	event := map[string]interface{}{
		"type":          "content_block_start",
		"index":         index,
		"content_block": contentBlock,
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_start\ndata: " + string(result))
}

// BuildClaudeStreamEvent creates a text_delta content_block_delta SSE event
func BuildClaudeStreamEvent(contentDelta string, index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": contentDelta,
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_delta\ndata: " + string(result))
}

// BuildClaudeInputJsonDeltaEvent creates an input_json_delta event for tool use streaming
func BuildClaudeInputJsonDeltaEvent(partialJSON string, index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": partialJSON,
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_delta\ndata: " + string(result))
}

// BuildClaudeContentBlockStopEvent creates a content_block_stop SSE event
func BuildClaudeContentBlockStopEvent(index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_stop",
		"index": index,
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_stop\ndata: " + string(result))
}

// BuildClaudeThinkingBlockStopEvent creates a content_block_stop SSE event for thinking blocks.
func BuildClaudeThinkingBlockStopEvent(index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_stop",
		"index": index,
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_stop\ndata: " + string(result))
}

// BuildClaudeMessageDeltaEvent creates the message_delta event with stop_reason and usage
func BuildClaudeMessageDeltaEvent(stopReason string, usageInfo usage.Detail) []byte {
	deltaEvent := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"input_tokens":  usageInfo.InputTokens,
			"output_tokens": usageInfo.OutputTokens,
		},
	}
	deltaResult, _ := json.Marshal(deltaEvent)
	return []byte("event: message_delta\ndata: " + string(deltaResult))
}

// BuildClaudeMessageStopOnlyEvent creates only the message_stop event
func BuildClaudeMessageStopOnlyEvent() []byte {
	stopEvent := map[string]interface{}{
		"type": "message_stop",
	}
	stopResult, _ := json.Marshal(stopEvent)
	return []byte("event: message_stop\ndata: " + string(stopResult))
}

// BuildClaudePingEventWithUsage creates a ping event with embedded usage information.
// This is used for real-time usage estimation during streaming.
func BuildClaudePingEventWithUsage(inputTokens, outputTokens int64) []byte {
	event := map[string]interface{}{
		"type": "ping",
		"usage": map[string]interface{}{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
			"estimated":     true,
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: ping\ndata: " + string(result))
}

// BuildClaudeThinkingDeltaEvent creates a thinking_delta event for Claude API compatibility.
// This is used when streaming thinking content wrapped in <thinking> tags.
func BuildClaudeThinkingDeltaEvent(thinkingDelta string, index int) []byte {
	event := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type":     "thinking_delta",
			"thinking": thinkingDelta,
		},
	}
	result, _ := json.Marshal(event)
	return []byte("event: content_block_delta\ndata: " + string(result))
}

// PendingTagSuffix detects if the buffer ends with a partial prefix of the given tag.
// Returns the length of the partial match (0 if no match).
// Based on amq2api implementation for handling cross-chunk tag boundaries.
func PendingTagSuffix(buffer, tag string) int {
	if buffer == "" || tag == "" {
		return 0
	}
	maxLen := len(buffer)
	if maxLen > len(tag)-1 {
		maxLen = len(tag) - 1
	}
	for length := maxLen; length > 0; length-- {
		if len(buffer) >= length && buffer[len(buffer)-length:] == tag[:length] {
			return length
		}
	}
	return 0
}