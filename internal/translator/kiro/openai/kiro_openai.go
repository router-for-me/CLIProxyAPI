// Package openai provides translation between OpenAI Chat Completions and Kiro formats.
// This package enables direct OpenAI → Kiro translation, bypassing the Claude intermediate layer.
//
// The Kiro executor generates Claude-compatible SSE format internally, so the streaming response
// translation converts from Claude SSE format to OpenAI SSE format.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	kirocommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/common"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// ConvertKiroStreamToOpenAI converts Kiro streaming response to OpenAI format.
// The Kiro executor emits Claude-compatible SSE events, so this function translates
// from Claude SSE format to OpenAI SSE format.
//
// Claude SSE format:
//   - event: message_start\ndata: {...}
//   - event: content_block_start\ndata: {...}
//   - event: content_block_delta\ndata: {...}
//   - event: content_block_stop\ndata: {...}
//   - event: message_delta\ndata: {...}
//   - event: message_stop\ndata: {...}
//
// OpenAI SSE format:
//   - data: {"id":"...","object":"chat.completion.chunk",...}
//   - data: [DONE]
func ConvertKiroStreamToOpenAI(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) []string {
	// Initialize state if needed
	if *param == nil {
		*param = NewOpenAIStreamState(model)
	}
	state := (*param).(*OpenAIStreamState)

	// Parse the Claude SSE event
	responseStr := string(rawResponse)

	// Handle raw event format (event: xxx\ndata: {...})
	var eventType string
	var eventData string

	if strings.HasPrefix(responseStr, "event:") {
		// Parse event type and data
		lines := strings.SplitN(responseStr, "\n", 2)
		if len(lines) >= 1 {
			eventType = strings.TrimSpace(strings.TrimPrefix(lines[0], "event:"))
		}
		if len(lines) >= 2 && strings.HasPrefix(lines[1], "data:") {
			eventData = strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))
		}
	} else if strings.HasPrefix(responseStr, "data:") {
		// Just data line
		eventData = strings.TrimSpace(strings.TrimPrefix(responseStr, "data:"))
	} else {
		// Try to parse as raw JSON
		eventData = strings.TrimSpace(responseStr)
	}

	if eventData == "" {
		return []string{}
	}

	// Parse the event data as JSON
	eventJSON := gjson.Parse(eventData)
	if !eventJSON.Exists() {
		return []string{}
	}

	// Determine event type from JSON if not already set
	if eventType == "" {
		eventType = eventJSON.Get("type").String()
	}

	var results []string

	switch eventType {
	case "message_start":
		// Send first chunk with role
		firstChunk := BuildOpenAISSEFirstChunk(state)
		results = append(results, firstChunk)

	case "content_block_start":
		// Check block type
		blockType := eventJSON.Get("content_block.type").String()
		switch blockType {
		case "text":
			// Text block starting - nothing to emit yet
		case "thinking":
			// Thinking block starting - nothing to emit yet for OpenAI
		case "tool_use":
			// Tool use block starting
			toolUseID := eventJSON.Get("content_block.id").String()
			toolName := eventJSON.Get("content_block.name").String()
			chunk := BuildOpenAISSEToolCallStart(state, toolUseID, toolName)
			results = append(results, chunk)
			state.ToolCallIndex++
		}

	case "content_block_delta":
		deltaType := eventJSON.Get("delta.type").String()
		switch deltaType {
		case "text_delta":
			textDelta := eventJSON.Get("delta.text").String()
			if textDelta != "" {
				chunk := BuildOpenAISSETextDelta(state, textDelta)
				results = append(results, chunk)
			}
		case "thinking_delta":
			// Convert thinking to reasoning_content for o1-style compatibility
			thinkingDelta := eventJSON.Get("delta.thinking").String()
			if thinkingDelta != "" {
				chunk := BuildOpenAISSEReasoningDelta(state, thinkingDelta)
				results = append(results, chunk)
			}
		case "input_json_delta":
			// Tool call arguments delta
			partialJSON := eventJSON.Get("delta.partial_json").String()
			if partialJSON != "" {
				// Get the tool index from content block index
				blockIndex := int(eventJSON.Get("index").Int())
				chunk := BuildOpenAISSEToolCallArgumentsDelta(state, partialJSON, blockIndex-1) // Adjust for 0-based tool index
				results = append(results, chunk)
			}
		}

	case "content_block_stop":
		// Content block ended - nothing to emit for OpenAI

	case "message_delta":
		// Message delta with stop_reason
		stopReason := eventJSON.Get("delta.stop_reason").String()
		finishReason := mapKiroStopReasonToOpenAI(stopReason)
		if finishReason != "" {
			chunk := BuildOpenAISSEFinish(state, finishReason)
			results = append(results, chunk)
		}

		// Extract usage if present
		if eventJSON.Get("usage").Exists() {
			inputTokens := eventJSON.Get("usage.input_tokens").Int()
			outputTokens := eventJSON.Get("usage.output_tokens").Int()
			usageInfo := usage.Detail{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				TotalTokens:  inputTokens + outputTokens,
			}
			chunk := BuildOpenAISSEUsage(state, usageInfo)
			results = append(results, chunk)
		}

	case "message_stop":
		// Final event - do NOT emit [DONE] here
		// The handler layer (openai_handlers.go) will send [DONE] when the stream closes
		// Emitting [DONE] here would cause duplicate [DONE] markers

	case "ping":
		// Ping event with usage - optionally emit usage chunk
		if eventJSON.Get("usage").Exists() {
			inputTokens := eventJSON.Get("usage.input_tokens").Int()
			outputTokens := eventJSON.Get("usage.output_tokens").Int()
			usageInfo := usage.Detail{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				TotalTokens:  inputTokens + outputTokens,
			}
			chunk := BuildOpenAISSEUsage(state, usageInfo)
			results = append(results, chunk)
		}
	}

	return results
}

// ConvertKiroNonStreamToOpenAI converts Kiro non-streaming response to OpenAI format.
// The Kiro executor returns Claude-compatible JSON responses, so this function translates
// from Claude format to OpenAI format.
func ConvertKiroNonStreamToOpenAI(ctx context.Context, model string, originalRequest, request, rawResponse []byte, param *any) string {
	// Parse the Claude-format response
	response := gjson.ParseBytes(rawResponse)

	// Extract content
	var content string
	var reasoningContent string
	var toolUses []KiroToolUse
	var stopReason string

	// Get stop_reason
	stopReason = response.Get("stop_reason").String()

	// Process content blocks
	contentBlocks := response.Get("content")
	if contentBlocks.IsArray() {
		for _, block := range contentBlocks.Array() {
			blockType := block.Get("type").String()
			switch blockType {
			case "text":
				content += block.Get("text").String()
			case "thinking":
				// Convert thinking blocks to reasoning_content for OpenAI format
				reasoningContent += block.Get("thinking").String()
			case "tool_use":
				toolUseID := block.Get("id").String()
				toolName := block.Get("name").String()
				toolInput := block.Get("input")

				var inputMap map[string]interface{}
				if toolInput.IsObject() {
					inputMap = make(map[string]interface{})
					toolInput.ForEach(func(key, value gjson.Result) bool {
						inputMap[key.String()] = value.Value()
						return true
					})
				}

				toolUses = append(toolUses, KiroToolUse{
					ToolUseID: toolUseID,
					Name:      toolName,
					Input:     inputMap,
				})
			}
		}
	}

	// Extract usage
	usageInfo := usage.Detail{
		InputTokens:  response.Get("usage.input_tokens").Int(),
		OutputTokens: response.Get("usage.output_tokens").Int(),
	}
	usageInfo.TotalTokens = usageInfo.InputTokens + usageInfo.OutputTokens

	// Build OpenAI response with reasoning_content support
	openaiResponse := BuildOpenAIResponseWithReasoning(content, reasoningContent, toolUses, model, usageInfo, stopReason)
	return string(openaiResponse)
}

// ParseClaudeEvent parses a Claude SSE event and returns the event type and data
func ParseClaudeEvent(rawEvent []byte) (eventType string, eventData []byte) {
	lines := bytes.Split(rawEvent, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("event:")) {
			eventType = string(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:"))))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			eventData = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		}
	}
	return eventType, eventData
}

// ExtractThinkingFromContent parses content to extract thinking blocks.
// Returns cleaned content (without thinking tags) and whether thinking was found.
func ExtractThinkingFromContent(content string) (string, string, bool) {
	if !strings.Contains(content, kirocommon.ThinkingStartTag) {
		return content, "", false
	}

	var cleanedContent strings.Builder
	var thinkingContent strings.Builder
	hasThinking := false
	remaining := content

	for len(remaining) > 0 {
		startIdx := strings.Index(remaining, kirocommon.ThinkingStartTag)
		if startIdx == -1 {
			cleanedContent.WriteString(remaining)
			break
		}

		// Add content before thinking tag
		cleanedContent.WriteString(remaining[:startIdx])

		// Move past opening tag
		remaining = remaining[startIdx+len(kirocommon.ThinkingStartTag):]

		// Find closing tag
		endIdx := strings.Index(remaining, kirocommon.ThinkingEndTag)
		if endIdx == -1 {
			// No closing tag - treat rest as thinking
			thinkingContent.WriteString(remaining)
			hasThinking = true
			break
		}

		// Extract thinking content
		thinkingContent.WriteString(remaining[:endIdx])
		hasThinking = true
		remaining = remaining[endIdx+len(kirocommon.ThinkingEndTag):]
	}

	return strings.TrimSpace(cleanedContent.String()), strings.TrimSpace(thinkingContent.String()), hasThinking
}

// ConvertOpenAIToolsToKiroFormat is a helper that converts OpenAI tools format to Kiro format
func ConvertOpenAIToolsToKiroFormat(tools []map[string]interface{}) []KiroToolWrapper {
	var kiroTools []KiroToolWrapper

	for _, tool := range tools {
		toolType, _ := tool["type"].(string)
		if toolType != "function" {
			continue
		}

		fn, ok := tool["function"].(map[string]interface{})
		if !ok {
			continue
		}

		name := kirocommon.GetString(fn, "name")
		description := kirocommon.GetString(fn, "description")
		parameters := ensureKiroInputSchema(fn["parameters"])

		if name == "" {
			continue
		}

		if description == "" {
			description = "Tool: " + name
		}

		kiroTools = append(kiroTools, KiroToolWrapper{
			ToolSpecification: KiroToolSpecification{
				Name:        name,
				Description: description,
				InputSchema: KiroInputSchema{JSON: parameters},
			},
		})
	}

	return kiroTools
}

// OpenAIStreamParams holds parameters for OpenAI streaming conversion
type OpenAIStreamParams struct {
	State            *OpenAIStreamState
	ThinkingState    *ThinkingTagState
	ToolCallsEmitted map[string]bool
}

// NewOpenAIStreamParams creates new streaming parameters
func NewOpenAIStreamParams(model string) *OpenAIStreamParams {
	return &OpenAIStreamParams{
		State:            NewOpenAIStreamState(model),
		ThinkingState:    NewThinkingTagState(),
		ToolCallsEmitted: make(map[string]bool),
	}
}

// ConvertClaudeToolUseToOpenAI converts a Claude tool_use block to OpenAI tool_calls format
func ConvertClaudeToolUseToOpenAI(toolUseID, toolName string, input map[string]interface{}) map[string]interface{} {
	inputJSON, _ := json.Marshal(input)
	return map[string]interface{}{
		"id":   toolUseID,
		"type": "function",
		"function": map[string]interface{}{
			"name":      toolName,
			"arguments": string(inputJSON),
		},
	}
}

// LogStreamEvent logs a streaming event for debugging
func LogStreamEvent(eventType, data string) {
	log.Debugf("kiro-openai: stream event type=%s, data_len=%d", eventType, len(data))
}
