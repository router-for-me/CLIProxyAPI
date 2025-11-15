// Package claude provides response translation functionality for OpenAI to Anthropic API.
// This package handles the conversion of OpenAI Chat Completions API responses into Anthropic API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Anthropic API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertOpenAIResponseToAnthropicParams holds parameters for response conversion
type ConvertOpenAIResponseToAnthropicParams struct {
	MessageID string
	Model     string
	CreatedAt int64
	// Content accumulator for streaming
	ContentAccumulator strings.Builder
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	// Track if text content block has been started
	TextContentBlockStarted bool
	// Track finish reason for later use
	FinishReason string
	// Track if content blocks have been stopped
	ContentBlocksStopped bool
	// Track if message_delta has been sent
	MessageDeltaSent bool
	// Track if message start has been sent
	MessageStarted bool
}

// ToolCallAccumulator holds the state for accumulating tool call data
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// ConvertOpenAIResponseToClaude converts OpenAI streaming response format to Anthropic API format.
// This function processes OpenAI streaming chunks and transforms them into Anthropic-compatible JSON responses.
// It handles text content, tool calls, and usage metadata, outputting responses that match the Anthropic API format.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - []string: A slice of strings, each containing an Anthropic-compatible JSON response.
func ConvertOpenAIResponseToClaude(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &ConvertOpenAIResponseToAnthropicParams{
			MessageID:               "",
			Model:                   "",
			CreatedAt:               0,
			ContentAccumulator:      strings.Builder{},
			ToolCallsAccumulator:    nil,
			TextContentBlockStarted: false,
			FinishReason:            "",
			ContentBlocksStopped:    false,
			MessageDeltaSent:        false,
			MessageStarted:          false,
		}
	}

	// Check if this is a web search response (non-streaming case)
	// When handling streaming, the web search response should come as a single chunk
	if isWebSearchResponse(rawJSON) || (bytes.HasPrefix(rawJSON, dataTag) && isWebSearchResponse(bytes.TrimSpace(rawJSON[5:]))) {
		// For web search responses in streaming context, we need to generate SSE events
		// If this is a data-tag prefixed response, process as a streaming chunk
		if bytes.HasPrefix(rawJSON, dataTag) {
			webSearchRaw := bytes.TrimSpace(rawJSON[5:])
			// Also check the original request was streaming to determine format
			streamResult := gjson.GetBytes(originalRequestRawJSON, "stream")
			isStream := streamResult.Exists() && streamResult.Type != gjson.False
			if isStream {
				return convertWebSearchResponseToClaudeSSE(webSearchRaw, modelName, (*param).(*ConvertOpenAIResponseToAnthropicParams))
			} else {
				// Non-streaming context, return the complete Claude message
				converted := convertWebSearchResponseToClaude(webSearchRaw, modelName)
				return []string{converted}
			}
		} else {
			// This is unprefixed web search response - check if original request was streaming
			streamResult := gjson.GetBytes(originalRequestRawJSON, "stream")
			isStream := streamResult.Exists() && streamResult.Type != gjson.False
			if isStream {
				// Original request was streaming, convert to SSE events
				return convertWebSearchResponseToClaudeSSE(rawJSON, modelName, (*param).(*ConvertOpenAIResponseToAnthropicParams))
			} else {
				// Non-streaming context, return the complete Claude message
				converted := convertWebSearchResponseToClaude(rawJSON, modelName)
				return []string{converted}
			}
		}
	}

	// Non-web-search response handling continues normally
	if !bytes.HasPrefix(rawJSON, dataTag) {
		return convertOpenAINonStreamingToAnthropic(rawJSON)
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	// Check if this is the [DONE] marker
	rawStr := strings.TrimSpace(string(rawJSON))
	if rawStr == "[DONE]" {
		return convertOpenAIDoneToAnthropic((*param).(*ConvertOpenAIResponseToAnthropicParams))
	}

	return convertOpenAIStreamingChunkToAnthropic(rawJSON, (*param).(*ConvertOpenAIResponseToAnthropicParams))
}

// convertOpenAIDoneToAnthropic handles the [DONE] marker and sends final events
func convertOpenAIDoneToAnthropic(param *ConvertOpenAIResponseToAnthropicParams) []string {
	var results []string

	// If we haven't sent message_delta yet (no usage info was received), send it now
	if param.FinishReason != "" && !param.MessageDeltaSent {
		messageDelta := map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason":   mapOpenAIFinishReasonToAnthropic(param.FinishReason),
				"stop_sequence": nil,
			},
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": param.ContentAccumulator.Len() / 4, // Rough approximation
			},
		}

		messageDeltaJSON, _ := json.Marshal(messageDelta)
		results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
		param.MessageDeltaSent = true
	}

	// Send message_stop
	results = append(results, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	return results
}

// convertOpenAIStreamingChunkToAnthropic converts OpenAI streaming chunk to Anthropic streaming events
func convertOpenAIStreamingChunkToAnthropic(rawJSON []byte, param *ConvertOpenAIResponseToAnthropicParams) []string {
	root := gjson.ParseBytes(rawJSON)
	var results []string

	// Initialize parameters if needed
	if param.MessageID == "" {
		param.MessageID = root.Get("id").String()
		if param.MessageID == "" {
			param.MessageID = generateMessageID()
		}
	}
	if param.Model == "" {
		param.Model = root.Get("model").String()
	}
	if param.CreatedAt == 0 {
		param.CreatedAt = root.Get("created").Int()
	}

	// Handle standard OpenAI streaming format
	// Check if this is the first chunk (has role)
	if delta := root.Get("choices.0.delta"); delta.Exists() {
		if role := delta.Get("role"); role.Exists() && role.String() == "assistant" && !param.MessageStarted {
			messageStart := map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":            param.MessageID,
					"type":          "message",
					"role":          "assistant",
					"model":         param.Model,
					"content":       []interface{}{},
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage": map[string]interface{}{
						"input_tokens":  0,
						"output_tokens": 0,
					},
				},
			}
			messageStartJSON, _ := json.Marshal(messageStart)
			results = append(results, "event: message_start\ndata: "+string(messageStartJSON)+"\n\n")
			param.MessageStarted = true

			// Don't send content_block_start for text here - wait for actual content
		}

		if content := delta.Get("content"); content.Exists() && content.String() != "" {
			// Send content_block_start for text if not already sent
			if !param.TextContentBlockStarted {
				contentBlockStart := map[string]interface{}{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				}
				contentBlockStartJSON, _ := json.Marshal(contentBlockStart)
				results = append(results, "event: content_block_start\ndata: "+string(contentBlockStartJSON)+"\n\n")
				param.TextContentBlockStarted = true
			}

			contentDelta := map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": content.String(),
				},
			}
			contentDeltaJSON, _ := json.Marshal(contentDelta)
			results = append(results, "event: content_block_delta\ndata: "+string(contentDeltaJSON)+"\n\n")

			// Accumulate content
			param.ContentAccumulator.WriteString(content.String())
		}

		// Handle tool calls
		if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			if param.ToolCallsAccumulator == nil {
				param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}

			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				index := int(toolCall.Get("index").Int())

				// Initialize accumulator if needed
				if _, exists := param.ToolCallsAccumulator[index]; !exists {
					param.ToolCallsAccumulator[index] = &ToolCallAccumulator{}
				}

				accumulator := param.ToolCallsAccumulator[index]

				// Handle tool call ID
				if id := toolCall.Get("id"); id.Exists() {
					accumulator.ID = id.String()
				}

				// Handle function name
				if function := toolCall.Get("function"); function.Exists() {
					if name := function.Get("name"); name.Exists() {
						accumulator.Name = name.String()

						if param.TextContentBlockStarted {
							param.TextContentBlockStarted = false
							contentBlockStop := map[string]interface{}{
								"type":  "content_block_stop",
								"index": index,
							}
							contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
							results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
						}

						// Send content_block_start for tool_use
						contentBlockStart := map[string]interface{}{
							"type":  "content_block_start",
							"index": index + 1, // Offset by 1 since text is at index 0
							"content_block": map[string]interface{}{
								"type":  "tool_use",
								"id":    accumulator.ID,
								"name":  accumulator.Name,
								"input": map[string]interface{}{},
							},
						}
						contentBlockStartJSON, _ := json.Marshal(contentBlockStart)
						results = append(results, "event: content_block_start\ndata: "+string(contentBlockStartJSON)+"\n\n")
					}

					// Handle function arguments
					if args := function.Get("arguments"); args.Exists() {
						argsText := args.String()
						if argsText != "" {
							accumulator.Arguments.WriteString(argsText)
						}
					}
				}

				return true
			})
		}
	}

	// Handle finish_reason (send message_delta and stop events)
	if finishReason := root.Get("choices.0.finish_reason"); finishReason.Exists() && finishReason.String() != "" {
		reason := finishReason.String()
		param.FinishReason = reason

		// Send content_block_stop for text if text content block was started
		if param.TextContentBlockStarted && !param.ContentBlocksStopped {
			contentBlockStop := map[string]interface{}{
				"type":  "content_block_stop",
				"index": 0,
			}
			contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
			results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")

			// Add ping event after content is stopped to follow Claude protocol sequence
			pingEvent := `event: ping
data: {"type": "ping"}

`
			results = append(results, pingEvent)

			param.ContentBlocksStopped = true
		}

		// Send content_block_stop for any tool calls
		if param.ToolCallsAccumulator != nil && !param.ContentBlocksStopped {
			for index := range param.ToolCallsAccumulator {
				accumulator := param.ToolCallsAccumulator[index]

				// Send complete input_json_delta with all accumulated arguments
				if accumulator.Arguments.Len() > 0 {
					inputDelta := map[string]interface{}{
						"type":  "content_block_delta",
						"index": index + 1,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": util.FixJSON(accumulator.Arguments.String()),
						},
					}
					inputDeltaJSON, _ := json.Marshal(inputDelta)
					results = append(results, "event: content_block_delta\ndata: "+string(inputDeltaJSON)+"\n\n")
				}

				contentBlockStop := map[string]interface{}{
					"type":  "content_block_stop",
					"index": index + 1,
				}
				contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
				results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
			}
			param.ContentBlocksStopped = true
		}

		// Handle usage information if available in this chunk
		if usage := root.Get("usage"); usage.Exists() && usage.Type != gjson.Null && !param.MessageDeltaSent {
			promptTokens := usage.Get("prompt_tokens")
			completionTokens := usage.Get("completion_tokens")

			if promptTokens.Exists() || completionTokens.Exists() {
				// Send message_delta with usage
				messageDelta := map[string]interface{}{
					"type": "message_delta",
					"delta": map[string]interface{}{
						"stop_reason":   mapOpenAIFinishReasonToAnthropic(param.FinishReason),
						"stop_sequence": nil,
					},
					"usage": map[string]interface{}{
						"input_tokens":  promptTokens.Int(),
						"output_tokens": completionTokens.Int(),
					},
				}

				messageDeltaJSON, _ := json.Marshal(messageDelta)
				results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
				param.MessageDeltaSent = true
			}
		}
	} else if usage := root.Get("usage"); usage.Exists() && usage.Type != gjson.Null {
		// Handle case where usage is sent in a separate chunk without finish_reason
		// This can happen in some streaming implementations
		if !param.MessageDeltaSent {
			promptTokens := usage.Get("prompt_tokens")
			completionTokens := usage.Get("completion_tokens")

			if promptTokens.Exists() || completionTokens.Exists() {
				stopReason := "end_turn"
				if param.FinishReason != "" {
					stopReason = mapOpenAIFinishReasonToAnthropic(param.FinishReason)
				}

				messageDelta := map[string]interface{}{
					"type": "message_delta",
					"delta": map[string]interface{}{
						"stop_reason":   stopReason,
						"stop_sequence": nil,
					},
					"usage": map[string]interface{}{
						"input_tokens":  promptTokens.Int(),
						"output_tokens": completionTokens.Int(),
					},
				}

				messageDeltaJSON, _ := json.Marshal(messageDelta)
				results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
				param.MessageDeltaSent = true
			}
		}
	}

	return results
}

// convertOpenAINonStreamingToAnthropic converts OpenAI non-streaming response to Anthropic format
func convertOpenAINonStreamingToAnthropic(rawJSON []byte) []string {
	// Check if this is a web search response
	if isWebSearchResponse(rawJSON) {
		// This shouldn't normally happen since web search responses are handled at the higher level,
		// but just in case, create a proper Claude response
		webSearchClaude := convertWebSearchResponseToClaude(rawJSON, "web-search-model")
		return []string{webSearchClaude}
	}

	root := gjson.ParseBytes(rawJSON)
	// Build Anthropic response
	response := map[string]interface{}{
		"id":            root.Get("id").String(),
		"type":          "message",
		"role":          "assistant",
		"model":         root.Get("model").String(),
		"content":       []interface{}{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}

	// Process message content and tool calls
	var contentBlocks []interface{}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choice := choices.Array()[0] // Take first choice

		// Handle text content
		if content := choice.Get("message.content"); content.Exists() && content.String() != "" {
			textBlock := map[string]interface{}{
				"type": "text",
				"text": content.String(),
			}
			contentBlocks = append(contentBlocks, textBlock)
		}

		// Handle tool calls
		if toolCalls := choice.Get("message.tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				toolUseBlock := map[string]interface{}{
					"type": "tool_use",
					"id":   toolCall.Get("id").String(),
					"name": toolCall.Get("function.name").String(),
				}

				// Parse arguments
				argsStr := toolCall.Get("function.arguments").String()
				argsStr = util.FixJSON(argsStr)
				if argsStr != "" {
					var args interface{}
					if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
						toolUseBlock["input"] = args
					} else {
						toolUseBlock["input"] = map[string]interface{}{}
					}
				} else {
					toolUseBlock["input"] = map[string]interface{}{}
				}

				contentBlocks = append(contentBlocks, toolUseBlock)
				return true
			})
		}

		// Set stop reason
		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			response["stop_reason"] = mapOpenAIFinishReasonToAnthropic(finishReason.String())
		}
	}

	response["content"] = contentBlocks

	// Set usage information
	if usage := root.Get("usage"); usage.Exists() {
		response["usage"] = map[string]interface{}{
			"input_tokens":  usage.Get("prompt_tokens").Int(),
			"output_tokens": usage.Get("completion_tokens").Int(),
		}
	}

	responseJSON, _ := json.Marshal(response)
	return []string{string(responseJSON)}
}

// mapOpenAIFinishReasonToAnthropic maps OpenAI finish reasons to Anthropic equivalents
func mapOpenAIFinishReasonToAnthropic(openAIReason string) string {
	switch openAIReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn" // Anthropic doesn't have direct equivalent
	case "function_call": // Legacy OpenAI
		return "tool_use"
	default:
		return "end_turn"
	}
}

// ConvertOpenAIResponseToClaudeNonStream converts a non-streaming OpenAI response to a non-streaming Anthropic response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the OpenAI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - string: An Anthropic-compatible JSON response.
func ConvertOpenAIResponseToClaudeNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	_ = originalRequestRawJSON
	_ = requestRawJSON

	// Check if this is a web search response
	if isWebSearchResponse(rawJSON) {
		return convertWebSearchResponseToClaude(rawJSON, modelName)
	}

	root := gjson.ParseBytes(rawJSON)

	// Build Anthropic response
	response := map[string]interface{}{
		"id":            root.Get("id").String(),
		"type":          "message",
		"role":          "assistant",
		"model":         root.Get("model").String(),
		"content":       []interface{}{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}

	// Process message content and tool calls
	var contentBlocks []interface{}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() && len(choices.Array()) > 0 {
		choice := choices.Array()[0]

		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			response["stop_reason"] = mapOpenAIFinishReasonToAnthropic(finishReason.String())
		}

		if message := choice.Get("message"); message.Exists() {
			if contentResult := message.Get("content"); contentResult.Exists() {
				if contentResult.IsArray() {
					var textBuilder strings.Builder
					var thinkingBuilder strings.Builder

					flushText := func() {
						if textBuilder.Len() == 0 {
							return
						}
						contentBlocks = append(contentBlocks, map[string]interface{}{
							"type": "text",
							"text": textBuilder.String(),
						})
						textBuilder.Reset()
					}

					flushThinking := func() {
						if thinkingBuilder.Len() == 0 {
							return
						}
						contentBlocks = append(contentBlocks, map[string]interface{}{
							"type":     "thinking",
							"thinking": thinkingBuilder.String(),
						})
						thinkingBuilder.Reset()
					}

					for _, item := range contentResult.Array() {
						typeStr := item.Get("type").String()
						switch typeStr {
						case "text":
							flushThinking()
							textBuilder.WriteString(item.Get("text").String())
						case "tool_calls":
							flushThinking()
							flushText()
							toolCalls := item.Get("tool_calls")
							if toolCalls.IsArray() {
								toolCalls.ForEach(func(_, tc gjson.Result) bool {
									toolUse := map[string]interface{}{
										"type": "tool_use",
										"id":   tc.Get("id").String(),
										"name": tc.Get("function.name").String(),
									}

									argsStr := util.FixJSON(tc.Get("function.arguments").String())
									if argsStr != "" {
										var parsed interface{}
										if err := json.Unmarshal([]byte(argsStr), &parsed); err == nil {
											toolUse["input"] = parsed
										} else {
											toolUse["input"] = map[string]interface{}{}
										}
									} else {
										toolUse["input"] = map[string]interface{}{}
									}

									contentBlocks = append(contentBlocks, toolUse)
									return true
								})
							}
						case "reasoning":
							flushText()
							if thinking := item.Get("text"); thinking.Exists() {
								thinkingBuilder.WriteString(thinking.String())
							}
						default:
							flushThinking()
							flushText()
						}
					}

					flushThinking()
					flushText()
				} else if contentResult.Type == gjson.String {
					textContent := contentResult.String()
					if textContent != "" {
						contentBlocks = append(contentBlocks, map[string]interface{}{
							"type": "text",
							"text": textContent,
						})
					}
				}
			}

			if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
					toolUseBlock := map[string]interface{}{
						"type": "tool_use",
						"id":   toolCall.Get("id").String(),
						"name": toolCall.Get("function.name").String(),
					}

					argsStr := toolCall.Get("function.arguments").String()
					argsStr = util.FixJSON(argsStr)
					if argsStr != "" {
						var args interface{}
						if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
							toolUseBlock["input"] = args
						} else {
							toolUseBlock["input"] = map[string]interface{}{}
						}
					} else {
						toolUseBlock["input"] = map[string]interface{}{}
					}

					contentBlocks = append(contentBlocks, toolUseBlock)
					return true
				})
			}
		}
	}

	response["content"] = contentBlocks

	if respUsage := root.Get("usage"); respUsage.Exists() {
		usageJSON := `{}`
		usageJSON, _ = sjson.Set(usageJSON, "input_tokens", respUsage.Get("prompt_tokens").Int())
		usageJSON, _ = sjson.Set(usageJSON, "output_tokens", respUsage.Get("completion_tokens").Int())
		parsedUsage := gjson.Parse(usageJSON).Value().(map[string]interface{})
		response["usage"] = parsedUsage
	}

	var hasToolCall bool = false
	if response["stop_reason"] == nil {
		if toolBlocks := response["content"].([]interface{}); len(toolBlocks) > 0 {
			for _, block := range toolBlocks {
				if m, ok := block.(map[string]interface{}); ok && m["type"] == "tool_use" {
					hasToolCall = true
					break
				}
			}
		}
		if hasToolCall {
			response["stop_reason"] = "tool_use"
		} else {
			response["stop_reason"] = "end_turn"
		}
	}

	if !hasToolCall {
		if toolBlocks := response["content"].([]interface{}); len(toolBlocks) > 0 {
			for _, block := range toolBlocks {
				if m, ok := block.(map[string]interface{}); ok && m["type"] == "tool_use" {
					hasToolCall = true
					break
				}
			}
		}
		if hasToolCall {
			response["stop_reason"] = "tool_use"
		}
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		return ""
	}

	return string(responseJSON)
}

func ClaudeTokenCount(ctx context.Context, count int64) string {
	return fmt.Sprintf(`{"input_tokens":%d}`, count)
}

// isWebSearchResponse checks if the raw JSON response is a web search response
// by looking for distinctive fields like "data" array which is typical in web search responses
func isWebSearchResponse(rawJSON []byte) bool {
	root := gjson.ParseBytes(rawJSON)
	// Check if response has the "data" array field which is characteristic of web search results
	return root.Get("data").Exists() && root.Get("data").IsArray()
}

// convertWebSearchResponseToClaude converts a web search response (like from /chat/retrieve)
// to Claude-compatible message format
func convertWebSearchResponseToClaude(rawJSON []byte, originalModelName string) string {
	root := gjson.ParseBytes(rawJSON)

	// Create a Claude message response
	response := map[string]interface{}{
		"id":            generateMessageID(),
		"type":          "message",
		"role":          "assistant",
		"model":         originalModelName, // Use the original model name
		"content":       []interface{}{},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}

	// Build content with search results
	contentBlocks := make([]interface{}, 0)

	// Get search results from "data" array
	dataResults := root.Get("data")
	if dataResults.Exists() && dataResults.IsArray() {
		results := dataResults.Array()
		if len(results) > 0 {
			// Create a summary content block with search results
			var searchSummary strings.Builder
			for i, result := range results {
				if i > 0 {
					searchSummary.WriteString("\n\n") // Separate results clearly
				}

				title := result.Get("title").String()
				url := result.Get("url").String()
				abstract := result.Get("abstractInfo").String()

				// Add the result to summary
				if title != "" {
					searchSummary.WriteString("## ")
					searchSummary.WriteString(title)
					searchSummary.WriteString("\n")
				}
				if url != "" {
					searchSummary.WriteString("URL: ")
					searchSummary.WriteString(url)
					searchSummary.WriteString("\n")
				}
				if abstract != "" {
					// Limit the abstract length to avoid very long responses
					cleanAbstract := strings.ReplaceAll(abstract, "<[^>]*>", "") // Remove HTML tags
					if len(cleanAbstract) > 500 {
						cleanAbstract = cleanAbstract[:500] + "..."
					}
					searchSummary.WriteString(cleanAbstract)
				}
			}

			// Create a text content block with the search results
			contentBlock := map[string]interface{}{
				"type": "text",
				"text": searchSummary.String(),
			}
			contentBlocks = append(contentBlocks, contentBlock)
		}
	}

	response["content"] = contentBlocks

	// Marshal to JSON
	responseJSON, err := json.Marshal(response)
	if err != nil {
		// Fallback: return empty Claude response
		fallback := fmt.Sprintf(`{
			"id": "%s",
			"type": "message",
			"role": "assistant",
			"model": "web-search-model",
			"content": [{"type": "text", "text": "Web search completed."}],
			"stop_reason": "end_turn",
			"stop_sequence": null,
			"usage": {"input_tokens": 0, "output_tokens": 0}
		}`, generateMessageID())
		return fallback
	}

	return string(responseJSON)
}

// convertWebSearchResponseToClaudeSSE converts a web search response to Claude SSE events
func convertWebSearchResponseToClaudeSSE(rawJSON []byte, originalModelName string, param *ConvertOpenAIResponseToAnthropicParams) []string {
	root := gjson.ParseBytes(rawJSON)

	var results []string

	// Initialize message ID and other fields if not already done
	if param.MessageID == "" {
		param.MessageID = generateMessageID()
	}
	if param.Model == "" {
		param.Model = originalModelName
	}

	// Only send message_start if we haven't sent it yet
	if !param.MessageStarted {
		// Create message_start event
		messageStart := map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":            param.MessageID,
				"type":          "message",
				"role":          "assistant",
				"model":         param.Model,
				"content":       []interface{}{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]interface{}{
					"input_tokens":  0,
					"output_tokens": 0,
				},
			},
		}
		messageStartJSON, _ := json.Marshal(messageStart)
		results = append(results, "event: message_start\ndata: "+string(messageStartJSON)+"\n\n")
		param.MessageStarted = true
	}

	// Get search results from "data" array
	contentText := ""
	dataResults := root.Get("data")
	if dataResults.Exists() && dataResults.IsArray() {
		resultsArray := dataResults.Array()
		if len(resultsArray) > 0 {
			// Create a summary content text with search results
			var searchSummary strings.Builder
			for i, result := range resultsArray {
				if i > 0 {
					searchSummary.WriteString("\n\n") // Separate results clearly
				}

				title := result.Get("title").String()
				url := result.Get("url").String()
				abstract := result.Get("abstractInfo").String()

				// Add the result to summary
				if title != "" {
					searchSummary.WriteString("## ")
					searchSummary.WriteString(title)
					searchSummary.WriteString("\n")
				}
				if url != "" {
					searchSummary.WriteString("URL: ")
					searchSummary.WriteString(url)
					searchSummary.WriteString("\n")
				}
				if abstract != "" {
					// Limit the abstract length to avoid very long responses
					cleanAbstract := strings.ReplaceAll(abstract, "<[^>]*>", "") // Remove HTML tags
					if len(cleanAbstract) > 500 {
						cleanAbstract = cleanAbstract[:500] + "..."
					}
					searchSummary.WriteString(cleanAbstract)
				}
			}
			contentText = searchSummary.String()
		}
	}

	// Send content_block_start
	if contentText != "" {
		contentBlockStart := map[string]interface{}{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		}
		contentBlockStartJSON, _ := json.Marshal(contentBlockStart)
		results = append(results, "event: content_block_start\ndata: "+string(contentBlockStartJSON)+"\n\n")

		// Send content_block_delta with the content in chunks (to simulate streaming)
		if len(contentText) > 0 {
			// Break content into reasonable-sized chunks to simulate streaming (avoiding tiny chunks)
			charsPerChunk := 100
			for i := 0; i < len(contentText); i += charsPerChunk {
				end := i + charsPerChunk
				if end > len(contentText) {
					end = len(contentText)
				}
				chunk := contentText[i:end]

				contentDelta := map[string]interface{}{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": chunk,
					},
				}
				contentDeltaJSON, _ := json.Marshal(contentDelta)
				results = append(results, "event: content_block_delta\ndata: "+string(contentDeltaJSON)+"\n\n")

				// Accumulate content
				param.ContentAccumulator.WriteString(chunk)
			}
		}

		// Send content_block_stop
		contentBlockStop := map[string]interface{}{
			"type":  "content_block_stop",
			"index": 0,
		}
		contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
		results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")

		param.ContentBlocksStopped = true
	}

	// Send message_delta with stop reason and usage
	messageDelta := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": len(contentText) / 4, // Rough approximation of tokens (4 chars per token)
		},
	}
	messageDeltaJSON, _ := json.Marshal(messageDelta)
	results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
	param.MessageDeltaSent = true

	// Send message_stop event
	messageStop := map[string]interface{}{
		"type": "message_stop",
	}
	messageStopJSON, _ := json.Marshal(messageStop)
	results = append(results, "event: message_stop\ndata: "+string(messageStopJSON)+"\n\n")

	return results
}

// generateMessageID creates a unique message ID for Claude responses
func generateMessageID() string {
	return "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}
