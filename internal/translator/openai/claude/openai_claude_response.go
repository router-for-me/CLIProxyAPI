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
	// Track if thinking content block has been started
	ThinkingContentBlockStarted bool
	// Track finish reason for later use
	FinishReason string
	// Track if content blocks have been stopped
	ContentBlocksStopped bool
	// Track if message_delta has been sent
	MessageDeltaSent bool
	// Track if message_start has been sent
	MessageStarted bool
	// Track if message_stop has been sent
	MessageStopSent bool
	// Tool call content block index mapping
	ToolCallBlockIndexes map[int]int
	// Index assigned to text content block
	TextContentBlockIndex int
	// Index assigned to thinking content block
	ThinkingContentBlockIndex int
	// Next available content block index
	NextContentBlockIndex int
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
func ConvertOpenAIResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &ConvertOpenAIResponseToAnthropicParams{
			MessageID:                   "",
			Model:                       "",
			CreatedAt:                   0,
			ContentAccumulator:          strings.Builder{},
			ToolCallsAccumulator:        nil,
			TextContentBlockStarted:     false,
			ThinkingContentBlockStarted: false,
			FinishReason:                "",
			ContentBlocksStopped:        false,
			MessageDeltaSent:            false,
			ToolCallBlockIndexes:        make(map[int]int),
			TextContentBlockIndex:       -1,
			ThinkingContentBlockIndex:   -1,
			NextContentBlockIndex:       0,
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return []string{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	// Check if this is the [DONE] marker
	rawStr := strings.TrimSpace(string(rawJSON))
	if rawStr == "[DONE]" {
		return convertOpenAIDoneToAnthropic((*param).(*ConvertOpenAIResponseToAnthropicParams))
	}

	streamResult := gjson.GetBytes(originalRequestRawJSON, "stream")
	if !streamResult.Exists() || (streamResult.Exists() && streamResult.Type == gjson.False) {
		return convertOpenAINonStreamingToAnthropic(rawJSON)
	} else {
		return convertOpenAIStreamingChunkToAnthropic(rawJSON, (*param).(*ConvertOpenAIResponseToAnthropicParams))
	}
}

// convertOpenAIStreamingChunkToAnthropic converts OpenAI streaming chunk to Anthropic streaming events
func convertOpenAIStreamingChunkToAnthropic(rawJSON []byte, param *ConvertOpenAIResponseToAnthropicParams) []string {
	root := gjson.ParseBytes(rawJSON)
	var results []string

	// Initialize parameters if needed
	if param.MessageID == "" {
		param.MessageID = root.Get("id").String()
	}
	if param.Model == "" {
		param.Model = root.Get("model").String()
	}
	if param.CreatedAt == 0 {
		param.CreatedAt = root.Get("created").Int()
	}

	// Check if this is the first chunk (has role)
	if delta := root.Get("choices.0.delta"); delta.Exists() {
		if role := delta.Get("role"); role.Exists() && role.String() == "assistant" && !param.MessageStarted {
			// Send message_start event
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

		// Handle reasoning content delta
		if reasoning := delta.Get("reasoning_content"); reasoning.Exists() {
			for _, reasoningText := range collectOpenAIReasoningTexts(reasoning) {
				if reasoningText == "" {
					continue
				}
				stopTextContentBlock(param, &results)
				if !param.ThinkingContentBlockStarted {
					if param.ThinkingContentBlockIndex == -1 {
						param.ThinkingContentBlockIndex = param.NextContentBlockIndex
						param.NextContentBlockIndex++
					}
					contentBlockStart := map[string]interface{}{
						"type":  "content_block_start",
						"index": param.ThinkingContentBlockIndex,
						"content_block": map[string]interface{}{
							"type":     "thinking",
							"thinking": "",
						},
					}
					contentBlockStartJSON, _ := json.Marshal(contentBlockStart)
					results = append(results, "event: content_block_start\ndata: "+string(contentBlockStartJSON)+"\n\n")
					param.ThinkingContentBlockStarted = true
				}

				thinkingDelta := map[string]interface{}{
					"type":  "content_block_delta",
					"index": param.ThinkingContentBlockIndex,
					"delta": map[string]interface{}{
						"type":     "thinking_delta",
						"thinking": reasoningText,
					},
				}
				thinkingDeltaJSON, _ := json.Marshal(thinkingDelta)
				results = append(results, "event: content_block_delta\ndata: "+string(thinkingDeltaJSON)+"\n\n")
			}
		}

		// Handle content delta
		if content := delta.Get("content"); content.Exists() && content.String() != "" {
			// Send content_block_start for text if not already sent
			if !param.TextContentBlockStarted {
				stopThinkingContentBlock(param, &results)
				if param.TextContentBlockIndex == -1 {
					param.TextContentBlockIndex = param.NextContentBlockIndex
					param.NextContentBlockIndex++
				}
				contentBlockStart := map[string]interface{}{
					"type":  "content_block_start",
					"index": param.TextContentBlockIndex,
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
				"index": param.TextContentBlockIndex,
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
				blockIndex := param.toolContentBlockIndex(index)

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

						stopThinkingContentBlock(param, &results)

						stopTextContentBlock(param, &results)

						// Send content_block_start for tool_use
						contentBlockStart := map[string]interface{}{
							"type":  "content_block_start",
							"index": blockIndex,
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

	// Handle finish_reason (but don't send message_delta/message_stop yet)
	if finishReason := root.Get("choices.0.finish_reason"); finishReason.Exists() && finishReason.String() != "" {
		reason := finishReason.String()
		param.FinishReason = reason

		// Send content_block_stop for thinking content if needed
		if param.ThinkingContentBlockStarted {
			contentBlockStop := map[string]interface{}{
				"type":  "content_block_stop",
				"index": param.ThinkingContentBlockIndex,
			}
			contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
			results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
			param.ThinkingContentBlockStarted = false
			param.ThinkingContentBlockIndex = -1
		}

		// Send content_block_stop for text if text content block was started
		stopTextContentBlock(param, &results)

		// Send content_block_stop for any tool calls
		if !param.ContentBlocksStopped {
			for index := range param.ToolCallsAccumulator {
				accumulator := param.ToolCallsAccumulator[index]
				blockIndex := param.toolContentBlockIndex(index)

				// Send complete input_json_delta with all accumulated arguments
				if accumulator.Arguments.Len() > 0 {
					inputDelta := map[string]interface{}{
						"type":  "content_block_delta",
						"index": blockIndex,
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
					"index": blockIndex,
				}
				contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
				results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
				delete(param.ToolCallBlockIndexes, index)
			}
			param.ContentBlocksStopped = true
		}

		// Don't send message_delta here - wait for usage info or [DONE]
	}

	// Handle usage information separately (this comes in a later chunk)
	// Only process if usage has actual values (not null)
	if param.FinishReason != "" {
		usage := root.Get("usage")
		var inputTokens, outputTokens int64
		if usage.Exists() && usage.Type != gjson.Null {
			// Check if usage has actual token counts
			promptTokens := usage.Get("prompt_tokens")
			completionTokens := usage.Get("completion_tokens")

			if promptTokens.Exists() && completionTokens.Exists() {
				inputTokens = promptTokens.Int()
				outputTokens = completionTokens.Int()
			}
		}
		// Send message_delta with usage
		messageDelta := map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason":   mapOpenAIFinishReasonToAnthropic(param.FinishReason),
				"stop_sequence": nil,
			},
			"usage": map[string]interface{}{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
			},
		}

		messageDeltaJSON, _ := json.Marshal(messageDelta)
		results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
		param.MessageDeltaSent = true

		emitMessageStopIfNeeded(param, &results)

	}

	return results
}

// convertOpenAIDoneToAnthropic handles the [DONE] marker and sends final events
func convertOpenAIDoneToAnthropic(param *ConvertOpenAIResponseToAnthropicParams) []string {
	var results []string

	// Ensure all content blocks are stopped before final events
	if param.ThinkingContentBlockStarted {
		contentBlockStop := map[string]interface{}{
			"type":  "content_block_stop",
			"index": param.ThinkingContentBlockIndex,
		}
		contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
		results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
		param.ThinkingContentBlockStarted = false
		param.ThinkingContentBlockIndex = -1
	}

	stopTextContentBlock(param, &results)

	if !param.ContentBlocksStopped {
		for index := range param.ToolCallsAccumulator {
			accumulator := param.ToolCallsAccumulator[index]
			blockIndex := param.toolContentBlockIndex(index)

			if accumulator.Arguments.Len() > 0 {
				inputDelta := map[string]interface{}{
					"type":  "content_block_delta",
					"index": blockIndex,
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
				"index": blockIndex,
			}
			contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
			results = append(results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
			delete(param.ToolCallBlockIndexes, index)
		}
		param.ContentBlocksStopped = true
	}

	// If we haven't sent message_delta yet (no usage info was received), send it now
	if param.FinishReason != "" && !param.MessageDeltaSent {
		messageDelta := map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason":   mapOpenAIFinishReasonToAnthropic(param.FinishReason),
				"stop_sequence": nil,
			},
		}

		messageDeltaJSON, _ := json.Marshal(messageDelta)
		results = append(results, "event: message_delta\ndata: "+string(messageDeltaJSON)+"\n\n")
		param.MessageDeltaSent = true
	}

	emitMessageStopIfNeeded(param, &results)

	return results
}

// convertOpenAINonStreamingToAnthropic converts OpenAI non-streaming response to Anthropic format
func convertOpenAINonStreamingToAnthropic(rawJSON []byte) []string {
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
		reasoningNode := choice.Get("message.reasoning_content")
		allReasoning := collectOpenAIReasoningTexts(reasoningNode)

		for _, reasoningText := range allReasoning {
			if reasoningText == "" {
				continue
			}
			contentBlocks = append(contentBlocks, map[string]interface{}{
				"type":     "thinking",
				"thinking": reasoningText,
			})
		}

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
			"reasoning_tokens": func() int64 {
				if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
					return v.Int()
				}
				return 0
			}(),
		}
	} else {
		response["usage"] = map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
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

func (p *ConvertOpenAIResponseToAnthropicParams) toolContentBlockIndex(openAIToolIndex int) int {
	if idx, ok := p.ToolCallBlockIndexes[openAIToolIndex]; ok {
		return idx
	}
	idx := p.NextContentBlockIndex
	p.NextContentBlockIndex++
	p.ToolCallBlockIndexes[openAIToolIndex] = idx
	return idx
}

func collectOpenAIReasoningTexts(node gjson.Result) []string {
	var texts []string
	if !node.Exists() {
		return texts
	}

	if node.IsArray() {
		node.ForEach(func(_, value gjson.Result) bool {
			texts = append(texts, collectOpenAIReasoningTexts(value)...)
			return true
		})
		return texts
	}

	switch node.Type {
	case gjson.String:
		if text := strings.TrimSpace(node.String()); text != "" {
			texts = append(texts, text)
		}
	case gjson.JSON:
		if text := node.Get("text"); text.Exists() {
			if trimmed := strings.TrimSpace(text.String()); trimmed != "" {
				texts = append(texts, trimmed)
			}
		} else if raw := strings.TrimSpace(node.Raw); raw != "" && !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
			texts = append(texts, raw)
		}
	}

	return texts
}

func stopThinkingContentBlock(param *ConvertOpenAIResponseToAnthropicParams, results *[]string) {
	if !param.ThinkingContentBlockStarted {
		return
	}
	contentBlockStop := map[string]interface{}{
		"type":  "content_block_stop",
		"index": param.ThinkingContentBlockIndex,
	}
	contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
	*results = append(*results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
	param.ThinkingContentBlockStarted = false
	param.ThinkingContentBlockIndex = -1
}

func emitMessageStopIfNeeded(param *ConvertOpenAIResponseToAnthropicParams, results *[]string) {
	if param.MessageStopSent {
		return
	}
	*results = append(*results, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	param.MessageStopSent = true
}

func stopTextContentBlock(param *ConvertOpenAIResponseToAnthropicParams, results *[]string) {
	if !param.TextContentBlockStarted {
		return
	}
	contentBlockStop := map[string]interface{}{
		"type":  "content_block_stop",
		"index": param.TextContentBlockIndex,
	}
	contentBlockStopJSON, _ := json.Marshal(contentBlockStop)
	*results = append(*results, "event: content_block_stop\ndata: "+string(contentBlockStopJSON)+"\n\n")
	param.TextContentBlockStarted = false
	param.TextContentBlockIndex = -1
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
func ConvertOpenAIResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	_ = originalRequestRawJSON
	_ = requestRawJSON

	root := gjson.ParseBytes(rawJSON)

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

	contentBlocks := make([]interface{}, 0)
	hasToolCall := false

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
									hasToolCall = true
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

			if reasoning := message.Get("reasoning_content"); reasoning.Exists() {
				for _, reasoningText := range collectOpenAIReasoningTexts(reasoning) {
					if reasoningText == "" {
						continue
					}
					contentBlocks = append(contentBlocks, map[string]interface{}{
						"type":     "thinking",
						"thinking": reasoningText,
					})
				}
			}

			if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
					hasToolCall = true
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
	} else {
		response["usage"] = `{"input_tokens":0,"output_tokens":0}`
	}

	if response["stop_reason"] == nil {
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
