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

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// codexThinkingSummaryPartSeparator joins consecutive reasoning summary parts inside
// the single thinking block that represents one Codex reasoning item.
const codexThinkingSummaryPartSeparator = "\n\n"

// ConvertCodexResponseToClaudeParams holds parameters for response conversion.
type ConvertCodexResponseToClaudeParams struct {
	HasEmittedToolUse      bool
	BlockIndex             int
	HasTextDelta           bool
	TextBlockOpen          bool
	ThinkingBlockOpen      bool
	ThinkingSignature      string
	ThinkingSummarySeen    bool
	WebSearchToolUseIDs    map[string]struct{}
	WebSearchToolResultIDs map[string]struct{}
	LastWebSearchToolUseID string
	FunctionCalls          map[string]*codexFunctionCallStream
	FunctionCallQueue      []*codexFunctionCallStream
	ActiveFunctionCall     *codexFunctionCallStream
	LastFunctionCall       *codexFunctionCallStream
	DeferredStreamEvents   [][]byte
}

type codexFunctionCallStream struct {
	CallID                    string
	Name                      string
	BlockIndex                int
	Arguments                 string
	EmittedArgumentsLength    int
	HasReceivedArgumentsDelta bool
	EmitInitialEmptyDelta     bool
	Started                   bool
	Done                      bool
	Closed                    bool
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
			BlockIndex: 0,
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	streamEventRawJSON := bytes.Clone(rawJSON)
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	output := make([]byte, 0, 512)
	rootResult := gjson.ParseBytes(rawJSON)
	params := (*param).(*ConvertCodexResponseToClaudeParams)

	typeResult := rootResult.Get("type")
	typeStr := typeResult.String()
	if params.ActiveFunctionCall != nil && shouldDeferCodexStreamEvent(typeStr, rootResult) {
		params.DeferredStreamEvents = append(params.DeferredStreamEvents, streamEventRawJSON)
		return [][]byte{}
	}
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
		output = append(output, stopCodexTextBlock(params)...)
		// Codex splits a single reasoning item into several summary parts, but only
		// output_item.done carries that item's final encrypted_content. Keep one
		// thinking block open for the whole item and separate the parts with a blank
		// line, so the only signature ever emitted is the final one.
		if params.ThinkingBlockOpen {
			output = append(output, appendCodexThinkingDelta(params, codexThinkingSummaryPartSeparator)...)
		} else {
			output = append(output, startCodexThinkingBlock(params)...)
		}
		params.ThinkingSummarySeen = true
	case "response.reasoning_summary_text.delta":
		output = append(output, stopCodexTextBlock(params)...)
		output = append(output, startCodexThinkingBlock(params)...)
		output = append(output, appendCodexThinkingDelta(params, rootResult.Get("delta").String())...)
	case "response.reasoning_summary_part.done":
		// Intentionally does not close the thinking block: it stays open until
		// output_item.done delivers the reasoning item's final encrypted_content.
	case "response.content_part.added":
		output = append(output, finalizeCodexThinkingBlock(params)...)
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
		output = append(output, finalizeCodexThinkingBlock(params)...)
		output = append(output, stopCodexTextBlock(params)...)
		output = appendCodexFunctionCallsFromTerminal(output, params, originalRequestRawJSON, responseData)
		output = appendDeferredCodexStreamEvents(output, originalRequestRawJSON, param)
		output = append(output, finalizeCodexThinkingBlock(params)...)
		output = append(output, stopCodexTextBlock(params)...)
		template, _ = sjson.SetBytes(template, "delta.stop_reason", mapCodexStopReasonToClaude(codexStopReason(responseData), params.HasEmittedToolUse))
		template = setClaudeStopSequence(template, "delta.stop_sequence", responseData)
		inputTokens, outputTokens, cachedTokens, cacheWriteTokens := extractResponsesUsage(responseData.Get("usage"))
		template, _ = sjson.SetBytes(template, "usage.input_tokens", inputTokens)
		template, _ = sjson.SetBytes(template, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			template, _ = sjson.SetBytes(template, "usage.cache_read_input_tokens", cachedTokens)
		}
		if cacheWriteTokens > 0 {
			template, _ = sjson.SetBytes(template, "usage.cache_creation_input_tokens", cacheWriteTokens)
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

			call := recordCodexFunctionCall(params, rootResult, itemResult)
			updateCodexFunctionCallIdentity(params, call, rootResult, itemResult)
			if call.Name != "" {
				call.EmitInitialEmptyDelta = true
			}
			output = appendCodexFunctionCallQueue(output, params, originalRequestRawJSON)
		case "reasoning":
			output = append(output, stopCodexTextBlock(params)...)
			// A previous reasoning item that never reported output_item.done must not
			// leak its still-open block into this one.
			output = append(output, finalizeCodexThinkingBlock(params)...)
			params.ThinkingSummarySeen = false
			// Kept only as a fallback for streams whose output_item.done omits
			// encrypted_content; it is a pre-content snapshot, never the final value.
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
			output = append(output, finalizeCodexThinkingBlock(params)...)
			output = append(output, stopCodexTextBlock(params)...)
			call := codexFunctionCallForEvent(params, rootResult, itemResult)
			if call == nil {
				call = recordCodexFunctionCall(params, rootResult, itemResult)
			}
			updateCodexFunctionCallIdentity(params, call, rootResult, itemResult)
			updateCodexFunctionCallArguments(call, itemResult.Get("arguments").String(), false)
			call.Done = true
			output = appendCodexFunctionCallQueue(output, params, originalRequestRawJSON)
		case "reasoning":
			output = append(output, stopCodexTextBlock(params)...)
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
		call := codexFunctionCallForEvent(params, rootResult, gjson.Result{})
		if call == nil {
			call = recordCodexFunctionCall(params, rootResult, gjson.Result{})
		}
		updateCodexFunctionCallArguments(call, rootResult.Get("delta").String(), true)
		output = appendCodexFunctionCallBufferedArguments(output, params, call)
	case "response.function_call_arguments.done":
		call := codexFunctionCallForEvent(params, rootResult, gjson.Result{})
		if call == nil {
			call = recordCodexFunctionCall(params, rootResult, gjson.Result{})
		}
		updateCodexFunctionCallArguments(call, rootResult.Get("arguments").String(), false)
		output = appendCodexFunctionCallBufferedArguments(output, params, call)
	}

	if len(params.FunctionCallQueue) == 0 {
		output = appendDeferredCodexStreamEvents(output, originalRequestRawJSON, param)
	}
	return [][]byte{output}
}

func shouldDeferCodexStreamEvent(typeStr string, rootResult gjson.Result) bool {
	switch typeStr {
	case "error", "response.completed", "response.incomplete", "response.function_call_arguments.delta", "response.function_call_arguments.done":
		return false
	case "response.output_item.added", "response.output_item.done":
		return rootResult.Get("item.type").String() != "function_call"
	default:
		return true
	}
}

func appendDeferredCodexStreamEvents(output []byte, originalRequestRawJSON []byte, param *any) []byte {
	if param == nil || *param == nil {
		return output
	}
	params := (*param).(*ConvertCodexResponseToClaudeParams)
	if len(params.DeferredStreamEvents) == 0 {
		return output
	}

	events := params.DeferredStreamEvents
	params.DeferredStreamEvents = nil
	for _, event := range events {
		translated := ConvertCodexResponseToClaude(context.Background(), "", originalRequestRawJSON, nil, event, param)
		for _, chunk := range translated {
			output = append(output, chunk...)
		}
	}
	return output
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
	inputTokens, outputTokens, cachedTokens, cacheWriteTokens := extractResponsesUsage(responseData.Get("usage"))
	out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
	if cachedTokens > 0 {
		out, _ = sjson.SetBytes(out, "usage.cache_read_input_tokens", cachedTokens)
	}
	if cacheWriteTokens > 0 {
		out, _ = sjson.SetBytes(out, "usage.cache_creation_input_tokens", cacheWriteTokens)
	}

	hasToolCall := false
	webSearchSeen := make(map[string]struct{})
	var contentBlocks [][]byte

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
					contentBlocks = append(contentBlocks, block)
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
									contentBlocks = append(contentBlocks, block)
								}
							}
							return true
						})
					} else {
						text := content.String()
						if text != "" {
							block := []byte(`{"type":"text","text":""}`)
							block, _ = sjson.SetBytes(block, "text", text)
							contentBlocks = append(contentBlocks, block)
						}
					}
				}
			case "web_search_call":
				contentBlocks = appendCodexWebSearchNonStreamBlocks(contentBlocks, item, webSearchSeen)
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
				contentBlocks = append(contentBlocks, toolBlock)
			}
			return true
		})
	}

	if len(contentBlocks) > 0 {
		out = translatorcommon.SetRawArrayItems(out, "content", contentBlocks)
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
		return "end_turn"
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

func codexFunctionCallID(itemResult gjson.Result) string {
	return itemResult.Get("call_id").String()
}

func codexFunctionCallKeys(rootResult, itemResult gjson.Result) []string {
	keys := make([]string, 0, 5)
	if outputIndex := rootResult.Get("output_index"); outputIndex.Exists() {
		keys = appendUniqueCodexFunctionCallKey(keys, "output:"+outputIndex.Raw)
	}
	if callID := codexFunctionCallID(itemResult); callID != "" {
		keys = appendUniqueCodexFunctionCallKey(keys, "call:"+callID)
	}
	if callID := rootResult.Get("call_id").String(); callID != "" {
		keys = appendUniqueCodexFunctionCallKey(keys, "call:"+callID)
	}
	if itemID := itemResult.Get("id").String(); itemID != "" {
		keys = appendUniqueCodexFunctionCallKey(keys, "item:"+itemID)
	}
	if itemID := rootResult.Get("item_id").String(); itemID != "" {
		keys = appendUniqueCodexFunctionCallKey(keys, "item:"+itemID)
	}
	return keys
}

func appendUniqueCodexFunctionCallKey(keys []string, key string) []string {
	if key == "" {
		return keys
	}
	for _, existing := range keys {
		if existing == key {
			return keys
		}
	}
	return append(keys, key)
}

func codexFunctionCallForKeys(params *ConvertCodexResponseToClaudeParams, keys []string) *codexFunctionCallStream {
	if params == nil || params.FunctionCalls == nil {
		return nil
	}
	for _, key := range keys {
		if call := params.FunctionCalls[key]; call != nil {
			return call
		}
	}
	return nil
}

func codexFunctionCallForEvent(params *ConvertCodexResponseToClaudeParams, rootResult, itemResult gjson.Result) *codexFunctionCallStream {
	keys := codexFunctionCallKeys(rootResult, itemResult)
	if len(keys) > 0 {
		return codexFunctionCallForKeys(params, keys)
	}
	if params == nil {
		return nil
	}
	return params.LastFunctionCall
}

func recordCodexFunctionCall(params *ConvertCodexResponseToClaudeParams, rootResult, itemResult gjson.Result) *codexFunctionCallStream {
	keys := codexFunctionCallKeys(rootResult, itemResult)
	call := codexFunctionCallForKeys(params, keys)
	if call == nil {
		call = &codexFunctionCallStream{BlockIndex: -1}
		params.FunctionCallQueue = append(params.FunctionCallQueue, call)
	}
	addCodexFunctionCallAliases(params, call, keys)
	params.LastFunctionCall = call
	return call
}

func addCodexFunctionCallAliases(params *ConvertCodexResponseToClaudeParams, call *codexFunctionCallStream, keys []string) {
	if params == nil || call == nil {
		return
	}
	if params.FunctionCalls == nil {
		params.FunctionCalls = map[string]*codexFunctionCallStream{}
	}
	for _, key := range keys {
		params.FunctionCalls[key] = call
	}
}

func updateCodexFunctionCallIdentity(params *ConvertCodexResponseToClaudeParams, call *codexFunctionCallStream, rootResult, itemResult gjson.Result) {
	if call == nil {
		return
	}
	if callID := codexFunctionCallID(itemResult); callID != "" {
		call.CallID = callID
	}
	if name := itemResult.Get("name").String(); name != "" {
		call.Name = name
	}
	addCodexFunctionCallAliases(params, call, codexFunctionCallKeys(rootResult, itemResult))
}

func updateCodexFunctionCallArguments(call *codexFunctionCallStream, arguments string, delta bool) {
	if call == nil || arguments == "" {
		return
	}
	if delta {
		call.Arguments += arguments
		call.HasReceivedArgumentsDelta = true
		return
	}
	if !call.HasReceivedArgumentsDelta {
		call.Arguments = arguments
		return
	}
	if strings.HasPrefix(arguments, call.Arguments) {
		call.Arguments = arguments
	}
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

func appendCodexFunctionCallBufferedArguments(output []byte, params *ConvertCodexResponseToClaudeParams, call *codexFunctionCallStream) []byte {
	if params == nil || call == nil || params.ActiveFunctionCall != call || !call.Started || call.Closed {
		return output
	}
	if call.EmittedArgumentsLength >= len(call.Arguments) {
		return output
	}

	output = appendCodexFunctionCallArgumentDelta(output, call.Arguments[call.EmittedArgumentsLength:], call.BlockIndex)
	call.EmittedArgumentsLength = len(call.Arguments)
	return output
}

func appendCodexFunctionCallQueue(output []byte, params *ConvertCodexResponseToClaudeParams, originalRequestRawJSON []byte) []byte {
	if params == nil {
		return output
	}

	for {
		if active := params.ActiveFunctionCall; active != nil {
			output = appendCodexFunctionCallBufferedArguments(output, params, active)
			if !active.Done {
				return output
			}
			output = appendCodexFunctionCallStop(output, active.BlockIndex)
			if params.BlockIndex <= active.BlockIndex {
				params.BlockIndex = active.BlockIndex + 1
			}
			active.Closed = true
			params.ActiveFunctionCall = nil
			removeCodexFunctionCallFromQueue(params, active)
		}

		for len(params.FunctionCallQueue) > 0 && params.FunctionCallQueue[0].Closed {
			params.FunctionCallQueue = params.FunctionCallQueue[1:]
		}
		if len(params.FunctionCallQueue) == 0 {
			return output
		}

		call := params.FunctionCallQueue[0]
		if call.Name == "" {
			return output
		}

		call.BlockIndex = params.BlockIndex
		output = appendCodexFunctionCallStart(output, originalRequestRawJSON, call.CallID, call.Name, call.BlockIndex)
		if call.EmitInitialEmptyDelta {
			output = appendCodexFunctionCallArgumentDelta(output, "", call.BlockIndex)
		}
		call.Started = true
		params.ActiveFunctionCall = call
		params.HasEmittedToolUse = true
		output = appendCodexFunctionCallBufferedArguments(output, params, call)
	}
}

func removeCodexFunctionCallFromQueue(params *ConvertCodexResponseToClaudeParams, call *codexFunctionCallStream) {
	if params == nil || call == nil {
		return
	}
	for index, queued := range params.FunctionCallQueue {
		if queued != call {
			continue
		}
		params.FunctionCallQueue = append(params.FunctionCallQueue[:index], params.FunctionCallQueue[index+1:]...)
		return
	}
}

func appendCodexFunctionCallsFromTerminal(output []byte, params *ConvertCodexResponseToClaudeParams, originalRequestRawJSON []byte, responseData gjson.Result) []byte {
	if params == nil {
		return output
	}

	responseData.Get("output").ForEach(func(index, item gjson.Result) bool {
		if item.Get("type").String() != "function_call" {
			return true
		}

		keys := codexFunctionCallKeys(gjson.Result{}, item)
		if itemOutputIndex := item.Get("output_index"); itemOutputIndex.Exists() {
			keys = appendUniqueCodexFunctionCallKey(keys, "output:"+itemOutputIndex.Raw)
		}
		if index.Exists() {
			keys = appendUniqueCodexFunctionCallKey(keys, "output:"+index.String())
		}
		call := codexFunctionCallForKeys(params, keys)
		if call == nil {
			call = &codexFunctionCallStream{BlockIndex: -1}
			params.FunctionCallQueue = append(params.FunctionCallQueue, call)
		}
		addCodexFunctionCallAliases(params, call, keys)
		updateCodexFunctionCallIdentity(params, call, gjson.Result{}, item)
		updateCodexFunctionCallArguments(call, item.Get("arguments").String(), false)
		call.Done = true
		return true
	})

	queuedCalls := params.FunctionCallQueue[:0]
	for _, call := range params.FunctionCallQueue {
		if call.Closed {
			continue
		}
		if call.Name == "" {
			call.Closed = true
			continue
		}
		call.Done = true
		queuedCalls = append(queuedCalls, call)
	}
	params.FunctionCallQueue = queuedCalls
	output = appendCodexFunctionCallQueue(output, params, originalRequestRawJSON)

	clearCodexFunctionCalls(params)
	return output
}

func clearCodexFunctionCalls(params *ConvertCodexResponseToClaudeParams) {
	if params == nil {
		return
	}
	clear(params.FunctionCalls)
	params.FunctionCallQueue = nil
	params.ActiveFunctionCall = nil
	params.LastFunctionCall = nil
}

func resolveCodexClaudeToolUseName(originalRequestRawJSON []byte, name string) string {
	rev := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)
	if orig, ok := rev[name]; ok {
		return orig
	}
	return name
}

func extractResponsesUsage(usage gjson.Result) (int64, int64, int64, int64) {
	if !usage.Exists() || usage.Type == gjson.Null {
		return 0, 0, 0, 0
	}

	inputTokens := usage.Get("input_tokens").Int()
	outputTokens := usage.Get("output_tokens").Int()
	cachedTokens := usage.Get("input_tokens_details.cached_tokens").Int()
	cacheWriteTokens := usage.Get("input_tokens_details.cache_write_tokens").Int()
	if cacheWriteTokens == 0 {
		cacheWriteTokens = usage.Get("input_tokens_details.cache_creation_tokens").Int()
	}

	if cachedTokens > 0 {
		if inputTokens >= cachedTokens {
			inputTokens -= cachedTokens
		} else {
			inputTokens = 0
		}
	}

	return inputTokens, outputTokens, cachedTokens, cacheWriteTokens
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

	return translatorcommon.AppendSSEEventBytes(nil, "content_block_start", template, 2)
}

// appendCodexThinkingDelta emits a thinking_delta for the currently open thinking block.
func appendCodexThinkingDelta(params *ConvertCodexResponseToClaudeParams, text string) []byte {
	if text == "" {
		return nil
	}

	template := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
	template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
	template, _ = sjson.SetBytes(template, "delta.thinking", text)

	return translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", template, 2)
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

	return output
}
