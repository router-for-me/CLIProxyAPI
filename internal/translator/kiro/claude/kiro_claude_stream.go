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

// GenerateSearchIndicatorEvents generates ONLY the search indicator SSE events
// (server_tool_use + web_search_tool_result) without text summary or message termination.
// These events trigger Claude Code's search indicator UI.
// The caller is responsible for sending message_start before and message_delta/stop after.
func GenerateSearchIndicatorEvents(
	query string,
	toolUseID string,
	searchResults *WebSearchResults,
	startIndex int,
) [][]byte {
	events := make([][]byte, 0, 5)

	// 1. content_block_start (server_tool_use)
	event1 := map[string]interface{}{
		"type":  "content_block_start",
		"index": startIndex,
		"content_block": map[string]interface{}{
			"id":    toolUseID,
			"type":  "server_tool_use",
			"name":  "web_search",
			"input": map[string]interface{}{},
		},
	}
	data1, _ := json.Marshal(event1)
	events = append(events, []byte("event: content_block_start\ndata: "+string(data1)+"\n\n"))

	// 2. content_block_delta (input_json_delta)
	inputJSON, _ := json.Marshal(map[string]string{"query": query})
	event2 := map[string]interface{}{
		"type":  "content_block_delta",
		"index": startIndex,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": string(inputJSON),
		},
	}
	data2, _ := json.Marshal(event2)
	events = append(events, []byte("event: content_block_delta\ndata: "+string(data2)+"\n\n"))

	// 3. content_block_stop (server_tool_use)
	event3 := map[string]interface{}{
		"type":  "content_block_stop",
		"index": startIndex,
	}
	data3, _ := json.Marshal(event3)
	events = append(events, []byte("event: content_block_stop\ndata: "+string(data3)+"\n\n"))

	// 4. content_block_start (web_search_tool_result)
	searchContent := make([]map[string]interface{}, 0)
	if searchResults != nil {
		for _, r := range searchResults.Results {
			snippet := ""
			if r.Snippet != nil {
				snippet = *r.Snippet
			}
			searchContent = append(searchContent, map[string]interface{}{
				"type":              "web_search_result",
				"title":             r.Title,
				"url":               r.URL,
				"encrypted_content": snippet,
				"page_age":          nil,
			})
		}
	}
	event4 := map[string]interface{}{
		"type":  "content_block_start",
		"index": startIndex + 1,
		"content_block": map[string]interface{}{
			"type":        "web_search_tool_result",
			"tool_use_id": toolUseID,
			"content":     searchContent,
		},
	}
	data4, _ := json.Marshal(event4)
	events = append(events, []byte("event: content_block_start\ndata: "+string(data4)+"\n\n"))

	// 5. content_block_stop (web_search_tool_result)
	event5 := map[string]interface{}{
		"type":  "content_block_stop",
		"index": startIndex + 1,
	}
	data5, _ := json.Marshal(event5)
	events = append(events, []byte("event: content_block_stop\ndata: "+string(data5)+"\n\n"))

	return events
}

// BuildFallbackTextEvents generates SSE events for a fallback text response
// when the Kiro API fails during the search loop. Uses BuildClaude*Event()
// functions to align with streamToChannel patterns.
// Returns raw SSE byte slices ready to be sent to the client channel.
func BuildFallbackTextEvents(contentBlockIndex int, query string, results *WebSearchResults) [][]byte {
	summary := FormatSearchContextPrompt(query, results)
	outputTokens := len(summary) / 4
	if len(summary) > 0 && outputTokens == 0 {
		outputTokens = 1
	}

	var events [][]byte

	// content_block_start (text)
	events = append(events, BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", ""))

	// content_block_delta (text_delta)
	events = append(events, BuildClaudeStreamEvent(summary, contentBlockIndex))

	// content_block_stop
	events = append(events, BuildClaudeContentBlockStopEvent(contentBlockIndex))

	// message_delta with end_turn
	events = append(events, BuildClaudeMessageDeltaEvent("end_turn", usage.Detail{
		OutputTokens: int64(outputTokens),
	}))

	// message_stop
	events = append(events, BuildClaudeMessageStopOnlyEvent())

	return events
}
