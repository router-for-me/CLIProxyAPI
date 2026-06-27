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

	translatorcommon "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/common"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertCodexResponseToClaudeParams holds parameters for response conversion.
type ConvertCodexResponseToClaudeParams struct {
	HasToolCall                bool
	BlockIndex                 int
	HasReceivedArgumentsDelta  bool
	HasTextDelta               bool
	TextBlockOpen              bool
	ThinkingBlockOpen          bool
	ThinkingStopPending        bool
	ThinkingSignature          string
	ThinkingSummarySeen        bool
	WebSearchToolUseIDs        map[string]struct{}
	WebSearchToolResultIDs     map[string]struct{}
	LastWebSearchToolUseID     string
	PendingFunctionCalls       map[string]*pendingCodexFunctionCall
	LastPendingFunctionCallKey string
}

type pendingCodexFunctionCall struct {
	CallID                    string
	Arguments                 string
	BlockIndex                int
	HasReceivedArgumentsDelta bool
	StartEmitted              bool
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

	switch typeStr {
	case "error":
		output = append(output, codexStreamErrorToClaudeError(rootResult)...)
	case "response.created":
		template = []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"claude-opus-4-1-20250805","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0},"content":[],"stop_reason":null}}`)
		template, _ = sjson.SetBytes(template, "message.model", rootResult.Get("response.model").String())
		template, _ = sjson.SetBytes(template, "message.id", rootResult.Get("response.id").String())

		output = translatorcommon.AppendSSEEventBytes(output, "message_start", template, 2)
	case "response.reasoning_summary_part.added":
		if params.ThinkingBlockOpen && params.ThinkingStopPending {
			output = append(output, finalizeCodexThinkingBlock(params)...)
		}
		params.ThinkingSummarySeen = true
		output = append(output, startCodexThinkingBlock(params)...)
	case "response.reasoning_summary_text.delta":
		template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
		template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
		template, _ = sjson.SetBytes(template, "delta.thinking", rootResult.Get("delta").String())

		output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
	case "response.reasoning_summary_part.done":
		params.ThinkingStopPending = true
	case "response.content_part.added":
		if rootResult.Get("part.type").String() == "output_text" {
			output = append(output, startCodexTextBlock(params)...)
		}
	case "response.output_text.delta":
		params.HasTextDelta = true
		output = append(output, finalizeCodexThinkingBlock(params)...)
		output = append(output, startCodexTextBlock(params)...)
		template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
		template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
		template, _ = sjson.SetBytes(template, "delta.text", rootResult.Get("delta").String())

		output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
	case "response.content_part.done":
		if rootResult.Get("part.type").String() == "output_text" {
			output = append(output, stopCodexTextBlock(params)...)
		}
	case "response.web_search_call.searching", "response.web_search_call.completed", "response.web_search_call.in_progress":
		// Wait for populated web_search_call items on output_item.done.
	case "response.completed", "response.incomplete":
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
	case "response.output_item.added":
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		switch itemType {
		case "function_call":
			output = append(output, finalizeCodexThinkingBlock(params)...)
			output = append(output, stopCodexTextBlock(params)...)
			params.HasToolCall = true
			params.HasReceivedArgumentsDelta = false

			callID := itemResult.Get("call_id").String()
			name := itemResult.Get("name").String()
			key := codexFunctionCallKey(rootResult, itemResult)
			if name == "" {
				if params.PendingFunctionCalls == nil {
					params.PendingFunctionCalls = map[string]*pendingCodexFunctionCall{}
				}
				params.PendingFunctionCalls[key] = &pendingCodexFunctionCall{
					CallID:     callID,
					BlockIndex: params.BlockIndex,
				}
				params.LastPendingFunctionCallKey = key
				params.BlockIndex++
				break
			}

			delete(params.PendingFunctionCalls, key)
			output = appendCodexFunctionCallStart(output, originalRequestRawJSON, callID, name, params.BlockIndex)
			output = appendCodexFunctionCallArgumentDelta(output, "", params.BlockIndex)
		case "reasoning":
			params.ThinkingSummarySeen = false
			params.ThinkingSignature = itemResult.Get("encrypted_content").String()
		case "web_search_call":
			// Defer server_tool_use until output_item.done carries action/query.
		}
	case "response.output_item.done":
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		switch itemType {
		case "message":
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
			output = append(output, startCodexTextBlock(params)...)

			template = []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
			template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
			template, _ = sjson.SetBytes(template, "delta.text", text)
			output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)

			output = append(output, stopCodexTextBlock(params)...)
			params.HasTextDelta = true
		case "function_call":
			key := codexFunctionCallKey(rootResult, itemResult)
			if pending, pendingKey := pendingCodexFunctionCallForKey(params, key); pending != nil && !pending.StartEmitted {
				name := itemResult.Get("name").String()
				if name == "" {
					return [][]byte{output}
				}
				callID := pending.CallID
				if callID == "" {
					callID = itemResult.Get("call_id").String()
				}
				output = appendCodexFunctionCallStart(output, originalRequestRawJSON, callID, name, pending.BlockIndex)
				pending.StartEmitted = true

				args := pending.Arguments
				if args == "" {
					args = itemResult.Get("arguments").String()
				}
				if args != "" {
					output = appendCodexFunctionCallArgumentDelta(output, args, pending.BlockIndex)
				}
				output = appendCodexFunctionCallStop(output, pending.BlockIndex)

				delete(params.PendingFunctionCalls, pendingKey)
				if params.LastPendingFunctionCallKey == pendingKey {
					params.LastPendingFunctionCallKey = ""
				}
			} else {
				output = appendCodexFunctionCallStop(output, params.BlockIndex)
				params.BlockIndex++
			}
		case "reasoning":
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
		case "web_search_call":
			output = appendCodexWebSearchToolResult(output, params, rootResult, itemResult)
		}
	case "response.function_call_arguments.delta":
		delta := rootResult.Get("delta").String()
		key := codexArgumentsFunctionCallKey(params, rootResult)
		if pending, _ := pendingCodexFunctionCallForKey(params, key); pending != nil && !pending.StartEmitted {
			pending.HasReceivedArgumentsDelta = true
			pending.Arguments += delta
			break
		}

		params.HasReceivedArgumentsDelta = true
		output = appendCodexFunctionCallArgumentDelta(output, delta, params.BlockIndex)
	case "response.function_call_arguments.done":
		key := codexArgumentsFunctionCallKey(params, rootResult)
		if pending, _ := pendingCodexFunctionCallForKey(params, key); pending != nil && !pending.StartEmitted {
			if !pending.HasReceivedArgumentsDelta {
				pending.Arguments = rootResult.Get("arguments").String()
			}
			break
		}

		if !params.HasReceivedArgumentsDelta {
			if args := rootResult.Get("arguments").String(); args != "" {
				output = appendCodexFunctionCallArgumentDelta(output, args, params.BlockIndex)
			}
		}
	}

	return [][]byte{output}
}

func codexStreamErrorToClaudeError(rootResult gjson.Result) []byte {
	errorResult := rootResult.Get("error")
	errType := strings.TrimSpace(errorResult.Get("type").String())
	if errType == "" {
		errType = strings.TrimSpace(rootResult.Get("error_type").String())
	}
	if errType == "" {
		errType = "api_error"
	}

	code := strings.TrimSpace(errorResult.Get("code").String())
	message := strings.TrimSpace(errorResult.Get("message").String())
	if message == "" {
		message = strings.TrimSpace(rootResult.Get("message").String())
	}
	if message == "" {
		message = code
	}
	if message == "" {
		message = errType
	}

	if code == "cyber_policy" || errType == "invalid_request" {
		errType = "invalid_request_error"
	}

	out := []byte(`{"type":"error","error":{"type":"api_error","message":""}}`)
	out, _ = sjson.SetBytes(out, "error.type", errType)
	out, _ = sjson.SetBytes(out, "error.message", message)
	return translatorcommon.AppendSSEEventBytes(nil, "error", out, 2)
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
	webSearchSeen := make(map[string]struct{})

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
			case "web_search_call":
				out = appendCodexWebSearchNonStreamContent(out, item, webSearchSeen)
			case "function_call":
				hasToolCall = true
				name := item.Get("name").String()
				if original, ok := revNames[name]; ok {
					name = original
				}

				toolBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolBlock, _ = sjson.SetBytes(toolBlock, "id", shortenCodexCallIDIfNeeded(util.SanitizeClaudeToolID(item.Get("call_id").String())))
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

func codexFunctionCallKey(rootResult, itemResult gjson.Result) string {
	if outputIndex := rootResult.Get("output_index"); outputIndex.Exists() {
		return "output:" + outputIndex.Raw
	}
	if callID := itemResult.Get("call_id").String(); callID != "" {
		return "call:" + callID
	}
	return "last"
}

func codexArgumentsFunctionCallKey(params *ConvertCodexResponseToClaudeParams, rootResult gjson.Result) string {
	if outputIndex := rootResult.Get("output_index"); outputIndex.Exists() {
		return "output:" + outputIndex.Raw
	}
	return params.LastPendingFunctionCallKey
}

func pendingCodexFunctionCallForKey(params *ConvertCodexResponseToClaudeParams, key string) (*pendingCodexFunctionCall, string) {
	if params == nil || params.PendingFunctionCalls == nil {
		return nil, ""
	}
	if key != "" {
		if pending, ok := params.PendingFunctionCalls[key]; ok {
			return pending, key
		}
	}
	if params.LastPendingFunctionCallKey != "" {
		if pending, ok := params.PendingFunctionCalls[params.LastPendingFunctionCallKey]; ok {
			return pending, params.LastPendingFunctionCallKey
		}
	}
	return nil, ""
}

func appendCodexFunctionCallStart(output []byte, originalRequestRawJSON []byte, callID, name string, blockIndex int) []byte {
	template := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
	template, _ = sjson.SetBytes(template, "index", blockIndex)
	template, _ = sjson.SetBytes(template, "content_block.id", shortenCodexCallIDIfNeeded(util.SanitizeClaudeToolID(callID)))
	template, _ = sjson.SetBytes(template, "content_block.name", resolveCodexClaudeToolUseName(originalRequestRawJSON, name))
	return translatorcommon.AppendSSEEventBytes(output, "content_block_start", template, 2)
}

func appendCodexFunctionCallArgumentDelta(output []byte, partialJSON string, blockIndex int) []byte {
	template := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
	template, _ = sjson.SetBytes(template, "index", blockIndex)
	template, _ = sjson.SetBytes(template, "delta.partial_json", partialJSON)
	return translatorcommon.AppendSSEEventBytes(output, "content_block_delta", template, 2)
}

func appendCodexFunctionCallStop(output []byte, blockIndex int) []byte {
	template := []byte(`{"type":"content_block_stop","index":0}`)
	template, _ = sjson.SetBytes(template, "index", blockIndex)
	return translatorcommon.AppendSSEEventBytes(output, "content_block_stop", template, 2)
}

func resolveCodexClaudeToolUseName(originalRequestRawJSON []byte, name string) string {
	rev := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)
	if orig, ok := rev[name]; ok {
		return orig
	}
	return name
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

func startCodexTextBlock(params *ConvertCodexResponseToClaudeParams) []byte {
	if params.TextBlockOpen {
		return nil
	}

	template := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
	params.TextBlockOpen = true

	return translatorcommon.AppendSSEEventBytes(nil, "content_block_start", template, 2)
}

func stopCodexTextBlock(params *ConvertCodexResponseToClaudeParams) []byte {
	if !params.TextBlockOpen {
		return nil
	}

	template := []byte(`{"type":"content_block_stop","index":0}`)
	template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
	params.TextBlockOpen = false
	params.BlockIndex++

	return translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", template, 2)
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
