// Package gemini provides response translation functionality for Claude Code to Gemini API compatibility.
// This package handles the conversion of Claude Code API responses into Gemini-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Gemini API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertAnthropicResponseToGeminiParams holds parameters for response conversion
// It also carries minimal streaming state across calls to assemble tool_use input_json_delta.
// This structure maintains state information needed for proper conversion of streaming responses
// from Claude Code format to Gemini format, particularly for handling tool calls that span
// multiple streaming events.
type ConvertAnthropicResponseToGeminiParams struct {
	Model             string
	CreatedAt         int64
	ResponseID        string
	LastStorageOutput string
	IsStreaming       bool

	// Streaming state for tool_use assembly
	// Keyed by content_block index from Claude SSE events
	ToolUseNames map[int]string           // function/tool name per block index
	ToolUseArgs  map[int]*strings.Builder // accumulates partial_json across deltas
}

// ConvertClaudeResponseToGemini converts Claude Code streaming response format to Gemini format.
// This function processes various Claude Code event types and transforms them into Gemini-compatible JSON responses.
// It handles text content, tool calls, reasoning content, and usage metadata, outputting responses that match
// the Gemini API format. The function supports incremental updates for streaming responses and maintains
// state information to properly assemble multi-part tool calls.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - []string: A slice of strings, each containing a Gemini-compatible JSON response
func ConvertClaudeResponseToGemini(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &ConvertAnthropicResponseToGeminiParams{
			Model:      modelName,
			CreatedAt:  0,
			ResponseID: "",
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return []string{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	root := gjson.ParseBytes(rawJSON)
	eventType := root.Get("type").String()

	// Base Gemini response template with default values
	template := `{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`

	// Set model version
	if (*param).(*ConvertAnthropicResponseToGeminiParams).Model != "" {
		// Map Claude model names back to Gemini model names
		template, _ = sjson.Set(template, "modelVersion", (*param).(*ConvertAnthropicResponseToGeminiParams).Model)
	}

	// Set response ID and creation time
	if (*param).(*ConvertAnthropicResponseToGeminiParams).ResponseID != "" {
		template, _ = sjson.Set(template, "responseId", (*param).(*ConvertAnthropicResponseToGeminiParams).ResponseID)
	}

	// Set creation time to current time if not provided
	if (*param).(*ConvertAnthropicResponseToGeminiParams).CreatedAt == 0 {
		(*param).(*ConvertAnthropicResponseToGeminiParams).CreatedAt = time.Now().Unix()
	}
	template, _ = sjson.Set(template, "createTime", time.Unix((*param).(*ConvertAnthropicResponseToGeminiParams).CreatedAt, 0).Format(time.RFC3339Nano))

	switch eventType {
	case "message_start":
		// Initialize response with message metadata when a new message begins
		if message := root.Get("message"); message.Exists() {
			(*param).(*ConvertAnthropicResponseToGeminiParams).ResponseID = message.Get("id").String()
			(*param).(*ConvertAnthropicResponseToGeminiParams).Model = message.Get("model").String()
		}
		return []string{}

	case "content_block_start":
		// Start of a content block - record tool_use name by index for functionCall assembly
		if cb := root.Get("content_block"); cb.Exists() {
			if cb.Get("type").String() == "tool_use" {
				idx := int(root.Get("index").Int())
				if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames == nil {
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames = map[int]string{}
				}
				if name := cb.Get("name"); name.Exists() {
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames[idx] = name.String()
				}
			}
		}
		return []string{}

	case "content_block_delta":
		// Handle content delta (text, thinking, or tool use arguments)
		if delta := root.Get("delta"); delta.Exists() {
			deltaType := delta.Get("type").String()

			switch deltaType {
			case "text_delta":
				// Regular text content delta for normal response text
				if text := delta.Get("text"); text.Exists() && text.String() != "" {
					textPart := `{"text":""}`
					textPart, _ = sjson.Set(textPart, "text", text.String())
					template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", textPart)
				}
			case "thinking_delta":
				// Thinking/reasoning content delta for models with reasoning capabilities
				if text := delta.Get("thinking"); text.Exists() && text.String() != "" {
					thinkingPart := `{"thought":true,"text":""}`
					thinkingPart, _ = sjson.Set(thinkingPart, "text", text.String())
					template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", thinkingPart)
				}
			case "input_json_delta":
				// Tool use input delta - accumulate partial_json by index for later assembly at content_block_stop
				idx := int(root.Get("index").Int())
				if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs == nil {
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs = map[int]*strings.Builder{}
				}
				b, ok := (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs[idx]
				if !ok || b == nil {
					bb := &strings.Builder{}
					(*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs[idx] = bb
					b = bb
				}
				if pj := delta.Get("partial_json"); pj.Exists() {
					b.WriteString(pj.String())
				}
				return []string{}
			}
		}
		return []string{template}

	case "content_block_stop":
		// End of content block - finalize tool calls if any
		idx := int(root.Get("index").Int())
		// Claude's content_block_stop often doesn't include content_block payload (see docs/response-claude.txt)
		// So we finalize using accumulated state captured during content_block_start and input_json_delta.
		name := ""
		if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames != nil {
			name = (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames[idx]
		}
		var argsTrim string
		if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs != nil {
			if b := (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs[idx]; b != nil {
				argsTrim = strings.TrimSpace(b.String())
			}
		}
		if name != "" || argsTrim != "" {
			functionCall := `{"functionCall":{"name":"","args":{}}}`
			if name != "" {
				functionCall, _ = sjson.Set(functionCall, "functionCall.name", name)
			}
			if argsTrim != "" {
				functionCall, _ = sjson.SetRaw(functionCall, "functionCall.args", argsTrim)
			}
			template, _ = sjson.SetRaw(template, "candidates.0.content.parts.-1", functionCall)
			template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")
			(*param).(*ConvertAnthropicResponseToGeminiParams).LastStorageOutput = template
			// cleanup used state for this index
			if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs != nil {
				delete((*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseArgs, idx)
			}
			if (*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames != nil {
				delete((*param).(*ConvertAnthropicResponseToGeminiParams).ToolUseNames, idx)
			}
			return []string{template}
		}
		return []string{}

	case "message_delta":
		// Handle message-level changes (like stop reason and usage information)
		if delta := root.Get("delta"); delta.Exists() {
			if stopReason := delta.Get("stop_reason"); stopReason.Exists() {
				switch stopReason.String() {
				case "end_turn":
					template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")
				case "tool_use":
					template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")
				case "max_tokens":
					template, _ = sjson.Set(template, "candidates.0.finishReason", "MAX_TOKENS")
				case "stop_sequence":
					template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")
				default:
					template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")
				}
			}
		}

		if usage := root.Get("usage"); usage.Exists() {
			// Basic token counts for prompt and completion
			inputTokens := usage.Get("input_tokens").Int()
			outputTokens := usage.Get("output_tokens").Int()

			// Set basic usage metadata according to Gemini API specification
			template, _ = sjson.Set(template, "usageMetadata.promptTokenCount", inputTokens)
			template, _ = sjson.Set(template, "usageMetadata.candidatesTokenCount", outputTokens)
			template, _ = sjson.Set(template, "usageMetadata.totalTokenCount", inputTokens+outputTokens)

			// Add cache-related token counts if present (Claude Code API cache fields)
			if cacheCreationTokens := usage.Get("cache_creation_input_tokens"); cacheCreationTokens.Exists() {
				template, _ = sjson.Set(template, "usageMetadata.cachedContentTokenCount", cacheCreationTokens.Int())
			}
			if cacheReadTokens := usage.Get("cache_read_input_tokens"); cacheReadTokens.Exists() {
				// Add cache read tokens to cached content count
				existingCacheTokens := usage.Get("cache_creation_input_tokens").Int()
				totalCacheTokens := existingCacheTokens + cacheReadTokens.Int()
				template, _ = sjson.Set(template, "usageMetadata.cachedContentTokenCount", totalCacheTokens)
			}

			// Add thinking tokens if present (for models with reasoning capabilities)
			if thinkingTokens := usage.Get("thinking_tokens"); thinkingTokens.Exists() {
				template, _ = sjson.Set(template, "usageMetadata.thoughtsTokenCount", thinkingTokens.Int())
			}

			// Set traffic type (required by Gemini API)
			template, _ = sjson.Set(template, "usageMetadata.trafficType", "PROVISIONED_THROUGHPUT")
		}
		template, _ = sjson.Set(template, "candidates.0.finishReason", "STOP")

		return []string{template}
	case "message_stop":
		// Final message with usage information - no additional output needed
		return []string{}
	case "error":
		// Handle error responses and convert to Gemini error format
		errorMsg := root.Get("error.message").String()
		if errorMsg == "" {
			errorMsg = "Unknown error occurred"
		}

		// Create error response in Gemini format
		errorResponse := `{"error":{"code":400,"message":"","status":"INVALID_ARGUMENT"}}`
		errorResponse, _ = sjson.Set(errorResponse, "error.message", errorMsg)
		return []string{errorResponse}

	default:
		// Unknown event type, return empty response
		return []string{}
	}
}

// ConvertClaudeResponseToGeminiNonStream converts a non-streaming Claude Code response to a non-streaming Gemini response.
// This function processes the complete Claude Code response and transforms it into a single Gemini-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the Gemini API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - string: A Gemini-compatible JSON response containing all message content and metadata
func ConvertClaudeResponseToGeminiNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	// Base Gemini response template for non-streaming with default values
	template := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"},"modelVersion":"","createTime":"","responseId":""}`

	// Set model version
	template, _ = sjson.Set(template, "modelVersion", modelName)

	streamingEvents := make([][]byte, 0)

	scanner := bufio.NewScanner(bytes.NewReader(rawJSON))
	buffer := make([]byte, 52_428_800) // 50MB
	scanner.Buffer(buffer, 52_428_800)
	for scanner.Scan() {
		line := scanner.Bytes()
		// log.Debug(string(line))
		if bytes.HasPrefix(line, dataTag) {
			jsonData := bytes.TrimSpace(line[5:])
			streamingEvents = append(streamingEvents, jsonData)
		}
	}
	// log.Debug("streamingEvents: ", streamingEvents)
	// log.Debug("rawJSON: ", string(rawJSON))

	// Initialize parameters for streaming conversion with proper state management
	newParam := &ConvertAnthropicResponseToGeminiParams{
		Model:             modelName,
		CreatedAt:         0,
		ResponseID:        "",
		LastStorageOutput: "",
		IsStreaming:       false,
		ToolUseNames:      nil,
		ToolUseArgs:       nil,
	}

	// Process each streaming event and collect parts
	var allParts []string
	var finalUsageJSON string
	var responseID string
	var createdAt int64

	for _, eventData := range streamingEvents {
		if len(eventData) == 0 {
			continue
		}

		root := gjson.ParseBytes(eventData)
		eventType := root.Get("type").String()

		switch eventType {
		case "message_start":
			// Extract response metadata including ID, model, and creation time
			if message := root.Get("message"); message.Exists() {
				responseID = message.Get("id").String()
				newParam.ResponseID = responseID
				newParam.Model = message.Get("model").String()

				// Set creation time to current time if not provided
				createdAt = time.Now().Unix()
				newParam.CreatedAt = createdAt
			}

		case "content_block_start":
			// Prepare for content block; record tool_use name by index for later functionCall assembly
			idx := int(root.Get("index").Int())
			if cb := root.Get("content_block"); cb.Exists() {
				if cb.Get("type").String() == "tool_use" {
					if newParam.ToolUseNames == nil {
						newParam.ToolUseNames = map[int]string{}
					}
					if name := cb.Get("name"); name.Exists() {
						newParam.ToolUseNames[idx] = name.String()
					}
				}
			}
			continue

		case "content_block_delta":
			// Handle content delta (text, thinking, or tool input)
			if delta := root.Get("delta"); delta.Exists() {
				deltaType := delta.Get("type").String()
				switch deltaType {
				case "text_delta":
					// Process regular text content
					if text := delta.Get("text"); text.Exists() && text.String() != "" {
						partJSON := `{"text":""}`
						partJSON, _ = sjson.Set(partJSON, "text", text.String())
						allParts = append(allParts, partJSON)
					}
				case "thinking_delta":
					// Process reasoning/thinking content
					if text := delta.Get("thinking"); text.Exists() && text.String() != "" {
						partJSON := `{"thought":true,"text":""}`
						partJSON, _ = sjson.Set(partJSON, "text", text.String())
						allParts = append(allParts, partJSON)
					}
				case "input_json_delta":
					// accumulate args partial_json for this index
					idx := int(root.Get("index").Int())
					if newParam.ToolUseArgs == nil {
						newParam.ToolUseArgs = map[int]*strings.Builder{}
					}
					if _, ok := newParam.ToolUseArgs[idx]; !ok || newParam.ToolUseArgs[idx] == nil {
						newParam.ToolUseArgs[idx] = &strings.Builder{}
					}
					if pj := delta.Get("partial_json"); pj.Exists() {
						newParam.ToolUseArgs[idx].WriteString(pj.String())
					}
				}
			}

		case "content_block_stop":
			// Handle tool use completion by assembling accumulated arguments
			idx := int(root.Get("index").Int())
			// Claude's content_block_stop often doesn't include content_block payload (see docs/response-claude.txt)
			// So we finalize using accumulated state captured during content_block_start and input_json_delta.
			name := ""
			if newParam.ToolUseNames != nil {
				name = newParam.ToolUseNames[idx]
			}
			var argsTrim string
			if newParam.ToolUseArgs != nil {
				if b := newParam.ToolUseArgs[idx]; b != nil {
					argsTrim = strings.TrimSpace(b.String())
				}
			}
			if name != "" || argsTrim != "" {
				functionCallJSON := `{"functionCall":{"name":"","args":{}}}`
				if name != "" {
					functionCallJSON, _ = sjson.Set(functionCallJSON, "functionCall.name", name)
				}
				if argsTrim != "" {
					functionCallJSON, _ = sjson.SetRaw(functionCallJSON, "functionCall.args", argsTrim)
				}
				allParts = append(allParts, functionCallJSON)
				// cleanup used state for this index
				if newParam.ToolUseArgs != nil {
					delete(newParam.ToolUseArgs, idx)
				}
				if newParam.ToolUseNames != nil {
					delete(newParam.ToolUseNames, idx)
				}
			}

		case "message_delta":
			// Extract final usage information using sjson for token counts and metadata
			if usage := root.Get("usage"); usage.Exists() {
				usageJSON := `{}`

				// Basic token counts for prompt and completion
				inputTokens := usage.Get("input_tokens").Int()
				outputTokens := usage.Get("output_tokens").Int()

				// Set basic usage metadata according to Gemini API specification
				usageJSON, _ = sjson.Set(usageJSON, "promptTokenCount", inputTokens)
				usageJSON, _ = sjson.Set(usageJSON, "candidatesTokenCount", outputTokens)
				usageJSON, _ = sjson.Set(usageJSON, "totalTokenCount", inputTokens+outputTokens)

				// Add cache-related token counts if present (Claude Code API cache fields)
				if cacheCreationTokens := usage.Get("cache_creation_input_tokens"); cacheCreationTokens.Exists() {
					usageJSON, _ = sjson.Set(usageJSON, "cachedContentTokenCount", cacheCreationTokens.Int())
				}
				if cacheReadTokens := usage.Get("cache_read_input_tokens"); cacheReadTokens.Exists() {
					// Add cache read tokens to cached content count
					existingCacheTokens := usage.Get("cache_creation_input_tokens").Int()
					totalCacheTokens := existingCacheTokens + cacheReadTokens.Int()
					usageJSON, _ = sjson.Set(usageJSON, "cachedContentTokenCount", totalCacheTokens)
				}

				// Add thinking tokens if present (for models with reasoning capabilities)
				if thinkingTokens := usage.Get("thinking_tokens"); thinkingTokens.Exists() {
					usageJSON, _ = sjson.Set(usageJSON, "thoughtsTokenCount", thinkingTokens.Int())
				}

				// Set traffic type (required by Gemini API)
				usageJSON, _ = sjson.Set(usageJSON, "trafficType", "PROVISIONED_THROUGHPUT")

				finalUsageJSON = usageJSON
			}
		}
	}

	// Set response metadata
	if responseID != "" {
		template, _ = sjson.Set(template, "responseId", responseID)
	}
	if createdAt > 0 {
		template, _ = sjson.Set(template, "createTime", time.Unix(createdAt, 0).Format(time.RFC3339Nano))
	}

	// Consolidate consecutive text parts and thinking parts for cleaner output
	consolidatedParts := consolidateParts(allParts)

	// Set the consolidated parts array
	if len(consolidatedParts) > 0 {
		partsJSON := "[]"
		for _, partJSON := range consolidatedParts {
			partsJSON, _ = sjson.SetRaw(partsJSON, "-1", partJSON)
		}
		template, _ = sjson.SetRaw(template, "candidates.0.content.parts", partsJSON)
	}

	// Set usage metadata
	if finalUsageJSON != "" {
		template, _ = sjson.SetRaw(template, "usageMetadata", finalUsageJSON)
	}

	return template
}

func GeminiTokenCount(ctx context.Context, count int64) string {
	return fmt.Sprintf(`{"totalTokens":%d,"promptTokensDetails":[{"modality":"TEXT","tokenCount":%d}]}`, count, count)
}

// consolidateParts merges consecutive text parts and thinking parts to create a cleaner response.
// This function processes the parts array to combine adjacent text elements and thinking elements
// into single consolidated parts, which results in a more readable and efficient response structure.
// Tool calls and other non-text parts are preserved as separate elements.
func consolidateParts(parts []string) []string {
	if len(parts) == 0 {
		return parts
	}

	var consolidated []string
	var currentTextPart strings.Builder
	var currentThoughtPart strings.Builder
	var hasText, hasThought bool

	flushText := func() {
		// Flush accumulated text content to the consolidated parts array
		if hasText && currentTextPart.Len() > 0 {
			textPartJSON := `{"text":""}`
			textPartJSON, _ = sjson.Set(textPartJSON, "text", currentTextPart.String())
			consolidated = append(consolidated, textPartJSON)
			currentTextPart.Reset()
			hasText = false
		}
	}

	flushThought := func() {
		// Flush accumulated thinking content to the consolidated parts array
		if hasThought && currentThoughtPart.Len() > 0 {
			thoughtPartJSON := `{"thought":true,"text":""}`
			thoughtPartJSON, _ = sjson.Set(thoughtPartJSON, "text", currentThoughtPart.String())
			consolidated = append(consolidated, thoughtPartJSON)
			currentThoughtPart.Reset()
			hasThought = false
		}
	}

	for _, partJSON := range parts {
		part := gjson.Parse(partJSON)
		if !part.Exists() || !part.IsObject() {
			// Flush any pending parts and add this non-text part
			flushText()
			flushThought()
			consolidated = append(consolidated, partJSON)
			continue
		}

		thought := part.Get("thought")
		if thought.Exists() && thought.Type == gjson.True {
			// This is a thinking part - flush any pending text first
			flushText() // Flush any pending text first

			if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
				currentThoughtPart.WriteString(text.String())
				hasThought = true
			}
		} else if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
			// This is a regular text part - flush any pending thought first
			flushThought() // Flush any pending thought first

			currentTextPart.WriteString(text.String())
			hasText = true
		} else {
			// This is some other type of part (like function call) - flush both text and thought
			flushText()
			flushThought()
			consolidated = append(consolidated, partJSON)
		}
	}

	// Flush any remaining parts
	flushThought() // Flush thought first to maintain order
	flushText()

	return consolidated
}
