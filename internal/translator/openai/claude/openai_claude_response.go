// Package claude provides response translation functionality for OpenAI to Anthropic API.
// This package handles the conversion of OpenAI Chat Completions API responses into Anthropic API-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by Anthropic API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, and usage metadata appropriately.
package claude

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/claude/streamstate"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertOpenAIResponseToAnthropicParams holds parameters for response conversion
type ConvertOpenAIResponseToAnthropicParams struct {
	MessageID   string
	Model       string
	CreatedAt   int64
	ToolNameMap map[string]string
	SawToolCall bool
	// Content accumulator for streaming
	ContentAccumulator strings.Builder
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	// Track finish reason for later use
	FinishReason string
	// Track if message_delta has been sent
	MessageDeltaSent bool
	// Track if message_start has been sent
	MessageStarted bool
	// Track if message_stop has been sent
	MessageStopSent bool
	Lifecycle       *streamstate.Lifecycle
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
//   - [][]byte: A slice of byte chunks, each containing an Anthropic-compatible JSON response.
func ConvertOpenAIResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertOpenAIResponseToAnthropicParams{
			MessageID:            "",
			Model:                "",
			CreatedAt:            0,
			ToolNameMap:          nil,
			SawToolCall:          false,
			ContentAccumulator:   strings.Builder{},
			ToolCallsAccumulator: nil,
			FinishReason:         "",
			MessageDeltaSent:     false,
			Lifecycle:            streamstate.NewLifecycle(),
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	if (*param).(*ConvertOpenAIResponseToAnthropicParams).ToolNameMap == nil {
		(*param).(*ConvertOpenAIResponseToAnthropicParams).ToolNameMap = util.ToolNameMapFromClaudeRequest(originalRequestRawJSON)
	}
	if (*param).(*ConvertOpenAIResponseToAnthropicParams).Lifecycle == nil {
		(*param).(*ConvertOpenAIResponseToAnthropicParams).Lifecycle = streamstate.NewLifecycle()
	}

	// Check if this is the [DONE] marker
	if bytes.Equal(bytes.TrimSpace(rawJSON), []byte("[DONE]")) {
		return convertOpenAIDoneToAnthropic((*param).(*ConvertOpenAIResponseToAnthropicParams))
	}

	streamResult := gjson.GetBytes(originalRequestRawJSON, "stream")
	if !streamResult.Exists() || (streamResult.Exists() && streamResult.Type == gjson.False) {
		return convertOpenAINonStreamingToAnthropic(rawJSON)
	} else {
		return convertOpenAIStreamingChunkToAnthropic(rawJSON, (*param).(*ConvertOpenAIResponseToAnthropicParams))
	}
}

func effectiveOpenAIFinishReason(param *ConvertOpenAIResponseToAnthropicParams) string {
	if param == nil {
		return ""
	}
	return param.FinishReason
}

// convertOpenAIStreamingChunkToAnthropic converts OpenAI streaming chunk to Anthropic streaming events
func convertOpenAIStreamingChunkToAnthropic(rawJSON []byte, param *ConvertOpenAIResponseToAnthropicParams) [][]byte {
	root := gjson.ParseBytes(rawJSON)
	var results [][]byte

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

	// Emit message_start on the very first chunk, regardless of whether it has a role field.
	// Some providers (like Copilot) may send tool_calls in the first chunk without a role field.
	if delta := root.Get("choices.0.delta"); delta.Exists() {
		if !param.MessageStarted {
			// Send message_start event
			messageStartJSON := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
			messageStartJSON, _ = sjson.SetBytes(messageStartJSON, "message.id", param.MessageID)
			messageStartJSON, _ = sjson.SetBytes(messageStartJSON, "message.model", param.Model)
			results = append(results, translatorcommon.AppendSSEEventBytes(nil, "message_start", messageStartJSON, 2))
			param.MessageStarted = true

			// Don't send content_block_start for text here - wait for actual content
		}

		// Handle reasoning content delta
		if reasoning := delta.Get("reasoning_content"); reasoning.Exists() {
			for _, reasoningText := range collectOpenAIReasoningTexts(reasoning) {
				if reasoningText == "" {
					continue
				}
				results = append(results, param.Lifecycle.AppendThinking(reasoningText)...)
			}
		}

		// Handle content delta
		if content := delta.Get("content"); content.Exists() && content.String() != "" {
			results = append(results, param.Lifecycle.AppendText(content.String())...)

			// Accumulate content
			param.ContentAccumulator.WriteString(content.String())
		}

		// Handle tool calls
		if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			if param.ToolCallsAccumulator == nil {
				param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}

			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				param.SawToolCall = true
				index := int(toolCall.Get("index").Int())
				toolKey := strconv.Itoa(index)

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
						accumulator.Name = util.MapToolName(param.ToolNameMap, name.String())
						results = append(results, param.Lifecycle.EnsureToolUse(toolKey, accumulator.ID, accumulator.Name)...)
					}

					// Handle function arguments
					if args := function.Get("arguments"); args.Exists() {
						argsText := args.String()
						if argsText != "" {
							accumulator.Arguments.WriteString(argsText)
							results = append(results, param.Lifecycle.AppendToolInput(toolKey, argsText)...)
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
		if param.SawToolCall {
			param.FinishReason = "tool_calls"
		} else {
			param.FinishReason = reason
		}

		results = append(results, param.Lifecycle.CloseAll()...)

		// Don't send message_delta here - wait for usage info or [DONE]
	}

	// Handle usage information separately (this comes in a later chunk)
	// Only process if usage has actual values (not null)
	if param.FinishReason != "" {
		usage := root.Get("usage")
		var inputTokens, outputTokens, cachedTokens int64
		if usage.Exists() && usage.Type != gjson.Null {
			inputTokens, outputTokens, cachedTokens = extractOpenAIUsage(usage)
			// Send message_delta with usage
			messageDeltaJSON := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
			messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "delta.stop_reason", mapOpenAIFinishReasonToAnthropic(effectiveOpenAIFinishReason(param)))
			messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "usage.input_tokens", inputTokens)
			messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "usage.output_tokens", outputTokens)
			if cachedTokens > 0 {
				messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "usage.cache_read_input_tokens", cachedTokens)
			}
			results = append(results, translatorcommon.AppendSSEEventBytes(nil, "message_delta", messageDeltaJSON, 2))
			param.MessageDeltaSent = true

			emitMessageStopIfNeeded(param, &results)
		}
	}

	return results
}

// convertOpenAIDoneToAnthropic handles the [DONE] marker and sends final events
func convertOpenAIDoneToAnthropic(param *ConvertOpenAIResponseToAnthropicParams) [][]byte {
	var results [][]byte

	results = append(results, param.Lifecycle.CloseAll()...)

	// If we haven't sent message_delta yet (no usage info was received), send it now
	if param.FinishReason != "" && !param.MessageDeltaSent {
		messageDeltaJSON := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
		messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "delta.stop_reason", mapOpenAIFinishReasonToAnthropic(effectiveOpenAIFinishReason(param)))
		results = append(results, translatorcommon.AppendSSEEventBytes(nil, "message_delta", messageDeltaJSON, 2))
		param.MessageDeltaSent = true
	}

	emitMessageStopIfNeeded(param, &results)

	return results
}

// convertOpenAINonStreamingToAnthropic converts OpenAI non-streaming response to Anthropic format
func convertOpenAINonStreamingToAnthropic(rawJSON []byte) [][]byte {
	root := gjson.ParseBytes(rawJSON)

	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", root.Get("id").String())
	out, _ = sjson.SetBytes(out, "model", root.Get("model").String())

	// Process message content and tool calls
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() && len(choices.Array()) > 0 {
		choice := choices.Array()[0] // Take first choice

		reasoningNode := choice.Get("message.reasoning_content")
		for _, reasoningText := range collectOpenAIReasoningTexts(reasoningNode) {
			if reasoningText == "" {
				continue
			}
			block := []byte(`{"type":"thinking","thinking":""}`)
			block, _ = sjson.SetBytes(block, "thinking", reasoningText)
			out, _ = sjson.SetRawBytes(out, "content.-1", block)
		}

		// Handle text content
		if content := choice.Get("message.content"); content.Exists() && content.String() != "" {
			block := []byte(`{"type":"text","text":""}`)
			block, _ = sjson.SetBytes(block, "text", content.String())
			out, _ = sjson.SetRawBytes(out, "content.-1", block)
		}

		// Handle tool calls
		if toolCalls := choice.Get("message.tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				toolUseBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "id", util.SanitizeClaudeToolID(toolCall.Get("id").String()))
				toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "name", toolCall.Get("function.name").String())

				argsStr := util.FixJSON(toolCall.Get("function.arguments").String())
				if argsStr != "" && gjson.Valid(argsStr) {
					argsJSON := gjson.Parse(argsStr)
					if argsJSON.IsObject() {
						toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(argsJSON.Raw))
					} else {
						toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
					}
				} else {
					toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
				}

				out, _ = sjson.SetRawBytes(out, "content.-1", toolUseBlock)
				return true
			})
		}

		// Set stop reason
		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			out, _ = sjson.SetBytes(out, "stop_reason", mapOpenAIFinishReasonToAnthropic(finishReason.String()))
		}
	}

	// Set usage information
	if usage := root.Get("usage"); usage.Exists() {
		inputTokens, outputTokens, cachedTokens := extractOpenAIUsage(usage)
		out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
		out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.cache_read_input_tokens", cachedTokens)
		}
	}

	return [][]byte{out}
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
		if text := node.String(); text != "" {
			texts = append(texts, text)
		}
	case gjson.JSON:
		if text := node.Get("text"); text.Exists() {
			if textStr := text.String(); textStr != "" {
				texts = append(texts, textStr)
			}
		} else if raw := node.Raw; raw != "" && !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
			texts = append(texts, raw)
		}
	}

	return texts
}

func emitMessageStopIfNeeded(param *ConvertOpenAIResponseToAnthropicParams, results *[][]byte) {
	if param.MessageStopSent {
		return
	}
	*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "message_stop", []byte(`{"type":"message_stop"}`), 2))
	param.MessageStopSent = true
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
//   - []byte: An Anthropic-compatible JSON response.
func ConvertOpenAIResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = requestRawJSON

	root := gjson.ParseBytes(rawJSON)
	toolNameMap := util.ToolNameMapFromClaudeRequest(originalRequestRawJSON)
	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", root.Get("id").String())
	out, _ = sjson.SetBytes(out, "model", root.Get("model").String())

	hasToolCall := false
	stopReasonSet := false

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() && len(choices.Array()) > 0 {
		choice := choices.Array()[0]

		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			out, _ = sjson.SetBytes(out, "stop_reason", finishReason.String())
			stopReasonSet = true
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
						block := []byte(`{"type":"text","text":""}`)
						block, _ = sjson.SetBytes(block, "text", textBuilder.String())
						out, _ = sjson.SetRawBytes(out, "content.-1", block)
						textBuilder.Reset()
					}

					flushThinking := func() {
						if thinkingBuilder.Len() == 0 {
							return
						}
						block := []byte(`{"type":"thinking","thinking":""}`)
						block, _ = sjson.SetBytes(block, "thinking", thinkingBuilder.String())
						out, _ = sjson.SetRawBytes(out, "content.-1", block)
						thinkingBuilder.Reset()
					}

					for _, item := range contentResult.Array() {
						switch item.Get("type").String() {
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
									toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
									toolUse, _ = sjson.SetBytes(toolUse, "id", util.SanitizeClaudeToolID(tc.Get("id").String()))
									toolUse, _ = sjson.SetBytes(toolUse, "name", util.MapToolName(toolNameMap, tc.Get("function.name").String()))

									argsStr := util.FixJSON(tc.Get("function.arguments").String())
									if argsStr != "" && gjson.Valid(argsStr) {
										argsJSON := gjson.Parse(argsStr)
										if argsJSON.IsObject() {
											toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(argsJSON.Raw))
										} else {
											toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(`{}`))
										}
									} else {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(`{}`))
									}

									out, _ = sjson.SetRawBytes(out, "content.-1", toolUse)
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
						block := []byte(`{"type":"text","text":""}`)
						block, _ = sjson.SetBytes(block, "text", textContent)
						out, _ = sjson.SetRawBytes(out, "content.-1", block)
					}
				}
			}

			if reasoning := message.Get("reasoning_content"); reasoning.Exists() {
				for _, reasoningText := range collectOpenAIReasoningTexts(reasoning) {
					if reasoningText == "" {
						continue
					}
					block := []byte(`{"type":"thinking","thinking":""}`)
					block, _ = sjson.SetBytes(block, "thinking", reasoningText)
					out, _ = sjson.SetRawBytes(out, "content.-1", block)
				}
			}

			if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
					hasToolCall = true
					toolUseBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
					toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "id", util.SanitizeClaudeToolID(toolCall.Get("id").String()))
					toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "name", util.MapToolName(toolNameMap, toolCall.Get("function.name").String()))

					argsStr := util.FixJSON(toolCall.Get("function.arguments").String())
					if argsStr != "" && gjson.Valid(argsStr) {
						argsJSON := gjson.Parse(argsStr)
						if argsJSON.IsObject() {
							toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(argsJSON.Raw))
						} else {
							toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
						}
					} else {
						toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
					}

					out, _ = sjson.SetRawBytes(out, "content.-1", toolUseBlock)
					return true
				})
			}
		}
	}

	if respUsage := root.Get("usage"); respUsage.Exists() {
		inputTokens, outputTokens, cachedTokens := extractOpenAIUsage(respUsage)
		out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
		out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.cache_read_input_tokens", cachedTokens)
		}
	}

	if stopReasonSet {
		out, _ = sjson.SetBytes(out, "stop_reason", mapOpenAIFinishReasonToAnthropic(gjson.GetBytes(out, "stop_reason").String()))
	} else {
		if hasToolCall {
			out, _ = sjson.SetBytes(out, "stop_reason", "tool_use")
		} else {
			out, _ = sjson.SetBytes(out, "stop_reason", "end_turn")
		}
	}

	return out
}

func ClaudeTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}

func extractOpenAIUsage(usage gjson.Result) (int64, int64, int64) {
	if !usage.Exists() || usage.Type == gjson.Null {
		return 0, 0, 0
	}

	inputTokens := usage.Get("prompt_tokens").Int()
	outputTokens := usage.Get("completion_tokens").Int()
	cachedTokens := usage.Get("prompt_tokens_details.cached_tokens").Int()

	if cachedTokens > 0 {
		if inputTokens >= cachedTokens {
			inputTokens -= cachedTokens
		} else {
			inputTokens = 0
		}
	}

	return inputTokens, outputTokens, cachedTokens
}
