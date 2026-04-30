// Package claude provides response translation functionality for Codex to Claude Code API compatibility.
// This package handles the conversion of Codex API responses into Claude Code-compatible
// Server-Sent Events (SSE) format, implementing a sophisticated state machine that manages
// different response types including text content, thinking processes, and function calls.
// The translation ensures proper sequencing of SSE events and maintains state across
// multiple response chunks to provide a seamless streaming experience.
package claude

import (
	"bytes"
	"context"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertCodexResponseToClaudeParams holds parameters for response conversion.
type ConvertCodexResponseToClaudeParams struct {
	HasToolCall               bool
	BlockIndex                int
	HasReceivedArgumentsDelta bool
	HasTextDelta              bool
	TextBlockOpen             bool
	ThinkingBlockOpen         bool
	ThinkingStopPending       bool
	ThinkingSignature         string
	ThinkingSummarySeen       bool
}

// ConvertCodexResponseToClaude performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates Codex API responses
// into Claude Code-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of Claude Code-compatible JSON responses
func ConvertCodexResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, _ []byte, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertCodexResponseToClaudeParams{
			HasToolCall: false,
			BlockIndex:  0,
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	output := make([]byte, 0, 512)
	rootResult := gjson.ParseBytes(rawJSON)
	params := (*param).(*ConvertCodexResponseToClaudeParams)
	if params.ThinkingBlockOpen && params.ThinkingStopPending {
		switch rootResult.Get("type").String() {
		case "response.content_part.added", "response.completed", "response.incomplete":
			output = append(output, finalizeCodexThinkingBlock(params)...)
		}
	}

	typeResult := rootResult.Get("type")
	typeStr := typeResult.String()
	var template []byte

	if typeStr == "response.created" {
		template = []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"claude-opus-4-1-20250805","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0},"content":[],"stop_reason":null}}`)
		template, _ = sjson.SetBytes(template, "message.model", rootResult.Get("response.model").String())
		template, _ = sjson.SetBytes(template, "message.id", rootResult.Get("response.id").String())

		output = translatorcommon.AppendSSEEventBytes(output, "message_start", template, 2)
	} else if typeStr == "response.reasoning_summary_part.added" {
		if params.ThinkingBlockOpen && params.ThinkingStopPending {
			output = append(output, finalizeCodexThinkingBlock(params)...)
		}
		params.ThinkingSummarySeen = true
		output = append(output, startCodexThinkingBlock(params)...)
	} else if typeStr == "response.reasoning_summary_text.delta" {
		template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
		template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
		template, _ = sjson.SetBytes(template, "delta.thinking", rootResult.Get("delta").String())

		output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
	} else if typeStr == "response.reasoning_summary_part.done" {
		params.ThinkingStopPending = true
	} else if typeStr == "response.content_part.added" {
		template = []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
		params.TextBlockOpen = true

		output = translatorcommon.AppendSSEEventBytes(output, "content_block_start", template, 2)
	} else if typeStr == "response.output_text.delta" {
		params.HasTextDelta = true
		template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
		template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
		template, _ = sjson.SetBytes(template, "delta.text", rootResult.Get("delta").String())

		output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
	} else if typeStr == "response.content_part.done" {
		template = []byte(`{"type":"content_block_stop","index":0}`)
		template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
		params.TextBlockOpen = false
		params.BlockIndex++

		output = translatorcommon.AppendSSEEventBytes(output, "content_block_stop", template, 2)
	} else if typeStr == "response.completed" || typeStr == "response.incomplete" {
		template = []byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
		responseData := rootResult.Get("response")
		template, _ = sjson.SetBytes(template, "delta.stop_reason", mapCodexStopReasonToClaude(codexStopReason(responseData), params.HasToolCall))
		template = setClaudeStopSequence(template, "delta.stop_sequence", responseData)
		inputTokens, outputTokens, cachedTokens := extractResponsesUsage(responseData.Get("usage"))
		template, _ = sjson.SetBytes(template, "usage.input_tokens", inputTokens)
		template, _ = sjson.SetBytes(template, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			template, _ = sjson.SetBytes(template, "usage.cache_read_input_tokens", cachedTokens)
		}

		output = translatorcommon.AppendSSEEventBytes(output, "message_delta", template, 2)
		output = translatorcommon.AppendSSEEventBytes(output, "message_stop", []byte(`{"type":"message_stop"}`), 2)
	} else if typeStr == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "function_call" {
			output = append(output, finalizeCodexThinkingBlock(params)...)
			params.HasToolCall = true
			params.HasReceivedArgumentsDelta = false
			template = []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
			template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
			template, _ = sjson.SetBytes(template, "content_block.id", util.SanitizeClaudeToolID(itemResult.Get("call_id").String()))
			{
				name := itemResult.Get("name").String()
				rev := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)
				if orig, ok := rev[name]; ok {
					name = orig
				}
				template, _ = sjson.SetBytes(template, "content_block.name", name)
			}

			output = translatorcommon.AppendSSEEventBytes(output, "content_block_start", template, 2)

			template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
			template, _ = sjson.SetBytes(template, "index", params.BlockIndex)

			output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
		} else if itemType == "reasoning" {
			params.ThinkingSummarySeen = false
			params.ThinkingSignature = itemResult.Get("encrypted_content").String()
		}
	} else if typeStr == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "message" {
			if params.HasTextDelta {
				return [][]byte{output}
			}
			contentResult := itemResult.Get("content")
			if !contentResult.Exists() || !contentResult.IsArray() {
				return [][]byte{output}
			}
			var textBuilder strings.Builder
			contentResult.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() != "output_text" {
					return true
				}
				if txt := part.Get("text").String(); txt != "" {
					textBuilder.WriteString(txt)
				}
				return true
			})
			text := textBuilder.String()
			if text == "" {
				return [][]byte{output}
			}

			output = append(output, finalizeCodexThinkingBlock(params)...)
			if !params.TextBlockOpen {
				template = []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
				template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
				params.TextBlockOpen = true
				output = translatorcommon.AppendSSEEventBytes(output, "content_block_start", template, 2)
			}

			template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
			template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
			template, _ = sjson.SetBytes(template, "delta.text", text)
			output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)

			template = []byte(`{"type":"content_block_stop","index":0}`)
			template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
			params.TextBlockOpen = false
			params.BlockIndex++
			params.HasTextDelta = true
			output = translatorcommon.AppendSSEEventBytes(output, "content_block_stop", template, 2)
		} else if itemType == "function_call" {
			template = []byte(`{"type":"content_block_stop","index":0}`)
			template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
			params.BlockIndex++

			output = translatorcommon.AppendSSEEventBytes(output, "content_block_stop", template, 2)
		} else if itemType == "reasoning" {
			if signature := itemResult.Get("encrypted_content").String(); signature != "" {
				params.ThinkingSignature = signature
			}
			if params.ThinkingSummarySeen {
				output = append(output, finalizeCodexThinkingBlock(params)...)
			} else {
				output = append(output, finalizeCodexSignatureOnlyThinkingBlock(params)...)
			}
			params.ThinkingSignature = ""
			params.ThinkingSummarySeen = false
		}
	} else if typeStr == "response.function_call_arguments.delta" {
		params.HasReceivedArgumentsDelta = true
		template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
		template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
		template, _ = sjson.SetBytes(template, "delta.partial_json", rootResult.Get("delta").String())

		output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
	} else if typeStr == "response.function_call_arguments.done" {
		if !params.HasReceivedArgumentsDelta {
			if args := rootResult.Get("arguments").String(); args != "" {
				template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
				template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
				template, _ = sjson.SetBytes(template, "delta.partial_json", args)

				output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
			}
		}
	}

	return [][]byte{output}
}

// ConvertCodexResponseToClaudeNonStream converts a non-streaming Codex response to a non-streaming Claude Code response.
// This function processes the complete Codex response and transforms it into a single Claude Code-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the Claude Code API format.
func ConvertCodexResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, _ []byte, rawJSON []byte, _ *any) []byte {
	revNames := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)

	rootResult := gjson.ParseBytes(rawJSON)
	typeStr := rootResult.Get("type").String()
	if typeStr != "response.completed" && typeStr != "response.incomplete" {
		return []byte{}
	}

	responseData := rootResult.Get("response")
	if !responseData.Exists() {
		return []byte{}
	}

	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", responseData.Get("id").String())
	out, _ = sjson.SetBytes(out, "model", responseData.Get("model").String())
	inputTokens, outputTokens, cachedTokens := extractResponsesUsage(responseData.Get("usage"))
	out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
	if cachedTokens > 0 {
		out, _ = sjson.SetBytes(out, "usage.cache_read_input_tokens", cachedTokens)
	}

	hasToolCall := false

	if output := responseData.Get("output"); output.Exists() && output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "reasoning":
				thinkingBuilder := strings.Builder{}
				signature := item.Get("encrypted_content").String()
				if summary := item.Get("summary"); summary.Exists() {
					if summary.IsArray() {
						summary.ForEach(func(_, part gjson.Result) bool {
							if txt := part.Get("text"); txt.Exists() {
								thinkingBuilder.WriteString(txt.String())
							} else {
								thinkingBuilder.WriteString(part.String())
							}
							return true
						})
					} else {
						thinkingBuilder.WriteString(summary.String())
					}
				}
				if thinkingBuilder.Len() == 0 {
					if content := item.Get("content"); content.Exists() {
						if content.IsArray() {
							content.ForEach(func(_, part gjson.Result) bool {
								if txt := part.Get("text"); txt.Exists() {
									thinkingBuilder.WriteString(txt.String())
								} else {
									thinkingBuilder.WriteString(part.String())
								}
								return true
							})
						} else {
							thinkingBuilder.WriteString(content.String())
						}
					}
				}
				if thinkingBuilder.Len() > 0 || signature != "" {
					block := []byte(`{"type":"thinking","thinking":""}`)
					block, _ = sjson.SetBytes(block, "thinking", thinkingBuilder.String())
					if signature != "" {
						block, _ = sjson.SetBytes(block, "signature", signature)
					}
					out, _ = sjson.SetRawBytes(out, "content.-1", block)
				}
			case "message":
				if content := item.Get("content"); content.Exists() {
					if content.IsArray() {
						content.ForEach(func(_, part gjson.Result) bool {
							if part.Get("type").String() == "output_text" {
								text := part.Get("text").String()
								if text != "" {
									block := []byte(`{"type":"text","text":""}`)
									block, _ = sjson.SetBytes(block, "text", text)
									out, _ = sjson.SetRawBytes(out, "content.-1", block)
								}
							}
							return true
						})
					} else {
						text := content.String()
						if text != "" {
							block := []byte(`{"type":"text","text":""}`)
							block, _ = sjson.SetBytes(block, "text", text)
							out, _ = sjson.SetRawBytes(out, "content.-1", block)
						}
					}
				}
			case "function_call":
				hasToolCall = true
				name := item.Get("name").String()
				if original, ok := revNames[name]; ok {
					name = original
				}

				toolBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolBlock, _ = sjson.SetBytes(toolBlock, "id", util.SanitizeClaudeToolID(item.Get("call_id").String()))
				toolBlock, _ = sjson.SetBytes(toolBlock, "name", name)
				inputRaw := "{}"
				if argsStr := item.Get("arguments").String(); argsStr != "" && gjson.Valid(argsStr) {
					argsJSON := gjson.Parse(argsStr)
					if argsJSON.IsObject() {
						inputRaw = argsJSON.Raw
					}
				}
				toolBlock, _ = sjson.SetRawBytes(toolBlock, "input", []byte(inputRaw))
				out, _ = sjson.SetRawBytes(out, "content.-1", toolBlock)
			}
			return true
		})
	}

	out, _ = sjson.SetBytes(out, "stop_reason", mapCodexStopReasonToClaude(codexStopReason(responseData), hasToolCall))
	out = setClaudeStopSequence(out, "stop_sequence", responseData)

	return out
}

func codexStopReason(responseData gjson.Result) string {
	if stopReason := responseData.Get("stop_reason"); stopReason.Exists() && stopReason.String() != "" {
		if stopReason.String() == "stop" && codexStopSequence(responseData).String() != "" {
			return "stop_sequence"
		}
		return stopReason.String()
	}
	if reason := responseData.Get("incomplete_details.reason"); reason.Exists() && reason.String() != "" {
		return reason.String()
	}
	if codexStopSequence(responseData).String() != "" {
		return "stop_sequence"
	}
	return ""
}

func mapCodexStopReasonToClaude(stopReason string, hasToolCall bool) string {
	if hasToolCall {
		return "tool_use"
	}

	switch stopReason {
	case "", "stop", "completed":
		return "end_turn"
	case "max_tokens", "max_output_tokens":
		return "max_tokens"
	case "tool_use", "tool_calls", "function_call":
		return "tool_use"
	case "end_turn", "stop_sequence", "pause_turn", "refusal", "model_context_window_exceeded":
		return stopReason
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

func codexStopSequence(responseData gjson.Result) gjson.Result {
	return responseData.Get("stop_sequence")
}

func setClaudeStopSequence(out []byte, path string, responseData gjson.Result) []byte {
	if stopSequence := codexStopSequence(responseData); stopSequence.Exists() && stopSequence.String() != "" {
		out, _ = sjson.SetRawBytes(out, path, []byte(stopSequence.Raw))
	}
	return out
}

func extractResponsesUsage(usage gjson.Result) (int64, int64, int64) {
	if !usage.Exists() || usage.Type == gjson.Null {
		return 0, 0, 0
	}

	inputTokens := usage.Get("input_tokens").Int()
	outputTokens := usage.Get("output_tokens").Int()
	cachedTokens := usage.Get("input_tokens_details.cached_tokens").Int()

	if cachedTokens > 0 {
		if inputTokens >= cachedTokens {
			inputTokens -= cachedTokens
		} else {
			inputTokens = 0
		}
	}

	return inputTokens, outputTokens, cachedTokens
}

// buildReverseMapFromClaudeOriginalShortToOriginal builds a map[short]original from original Claude request tools.
func buildReverseMapFromClaudeOriginalShortToOriginal(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	rev := map[string]string{}
	if !tools.IsArray() {
		return rev
	}
	var names []string
	arr := tools.Array()
	for i := 0; i < len(arr); i++ {
		n := arr[i].Get("name").String()
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		m := buildShortNameMap(names)
		for orig, short := range m {
			rev[short] = orig
		}
	}
	return rev
}

func ClaudeTokenCount(_ context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}

func startCodexThinkingBlock(params *ConvertCodexResponseToClaudeParams) []byte {
	if params.ThinkingBlockOpen {
		return nil
	}

	template := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
	params.ThinkingBlockOpen = true
	params.ThinkingStopPending = false

	return translatorcommon.AppendSSEEventBytes(nil, "content_block_start", template, 2)
}

func finalizeCodexSignatureOnlyThinkingBlock(params *ConvertCodexResponseToClaudeParams) []byte {
	if params.ThinkingSignature == "" {
		return nil
	}

	output := startCodexThinkingBlock(params)
	output = append(output, finalizeCodexThinkingBlock(params)...)
	return output
}

func finalizeCodexThinkingBlock(params *ConvertCodexResponseToClaudeParams) []byte {
	if !params.ThinkingBlockOpen {
		return nil
	}

	output := make([]byte, 0, 256)
	if params.ThinkingSignature != "" {
		signatureDelta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`)
		signatureDelta, _ = sjson.SetBytes(signatureDelta, "index", params.BlockIndex)
		signatureDelta, _ = sjson.SetBytes(signatureDelta, "delta.signature", params.ThinkingSignature)
		output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", signatureDelta, 2)
	}

	contentBlockStop := []byte(`{"type":"content_block_stop","index":0}`)
	contentBlockStop, _ = sjson.SetBytes(contentBlockStop, "index", params.BlockIndex)
	output = translatorcommon.AppendSSEEventBytes(output, "content_block_stop", contentBlockStop, 2)

	params.BlockIndex++
	params.ThinkingBlockOpen = false
	params.ThinkingStopPending = false

	return output
}
