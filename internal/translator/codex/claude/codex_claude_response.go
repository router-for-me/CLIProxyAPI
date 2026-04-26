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
	"encoding/json"
	"fmt"
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

func codexClaudePayload(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, dataTag) {
		payload := bytes.TrimSpace(trimmed[len(dataTag):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}
		return payload
	}

	var dataLines [][]byte
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		data := bytes.TrimSpace(line[len(dataTag):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		dataLines = append(dataLines, data)
	}
	if len(dataLines) == 0 {
		if json.Valid(trimmed) {
			return trimmed
		}
		return nil
	}
	if len(dataLines) == 1 {
		return dataLines[0]
	}
	return bytes.Join(dataLines, []byte("\n"))
}

// ConvertCodexResponseToClaudeParams holds parameters for response conversion.
type ConvertCodexResponseToClaudeParams struct {
	HasToolCall               bool
	HasReceivedArgumentsDelta bool
	CurrentToolKey            string
	Lifecycle                 *streamstate.Lifecycle
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
			Lifecycle:   streamstate.NewLifecycle(),
		}
	}

	payload := codexClaudePayload(rawJSON)
	if len(payload) == 0 {
		return [][]byte{}
	}
	rawJSON = payload

	output := make([]byte, 0, 512)
	appendChunks := func(chunks [][]byte) {
		for _, chunk := range chunks {
			output = append(output, chunk...)
		}
	}
	rootResult := gjson.ParseBytes(rawJSON)
	typeResult := rootResult.Get("type")
	typeStr := typeResult.String()
	params := (*param).(*ConvertCodexResponseToClaudeParams)
	if params.Lifecycle == nil {
		params.Lifecycle = streamstate.NewLifecycle()
	}
	var template []byte
	if typeStr == "response.created" {
		template = []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"claude-opus-4-1-20250805","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0},"content":[],"stop_reason":null}}`)
		template, _ = sjson.SetBytes(template, "message.model", rootResult.Get("response.model").String())
		template, _ = sjson.SetBytes(template, "message.id", rootResult.Get("response.id").String())

		output = translatorcommon.AppendSSEEventBytes(output, "message_start", template, 2)
	} else if typeStr == "response.reasoning_summary_part.added" {
	} else if typeStr == "response.reasoning_summary_text.delta" {
		appendChunks(params.Lifecycle.AppendThinking(rootResult.Get("delta").String()))
	} else if typeStr == "response.reasoning_summary_part.done" {
		appendChunks(params.Lifecycle.CloseThinking())
	} else if typeStr == "response.content_part.added" {
	} else if typeStr == "response.output_text.delta" {
		appendChunks(params.Lifecycle.AppendText(rootResult.Get("delta").String()))
	} else if typeStr == "response.content_part.done" {
		appendChunks(params.Lifecycle.CloseText())
	} else if typeStr == "response.completed" {
		template = []byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
		p := params.HasToolCall
		stopReason := rootResult.Get("response.stop_reason").String()
		if p {
			template, _ = sjson.SetBytes(template, "delta.stop_reason", "tool_use")
		} else if stopReason == "max_tokens" || stopReason == "stop" {
			template, _ = sjson.SetBytes(template, "delta.stop_reason", stopReason)
		} else {
			template, _ = sjson.SetBytes(template, "delta.stop_reason", "end_turn")
		}
		inputTokens, outputTokens, cachedTokens := extractResponsesUsage(rootResult.Get("response.usage"))
		template, _ = sjson.SetBytes(template, "usage.input_tokens", inputTokens)
		template, _ = sjson.SetBytes(template, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			template, _ = sjson.SetBytes(template, "usage.cache_read_input_tokens", cachedTokens)
		}

		appendChunks(params.Lifecycle.CloseAll())
		output = translatorcommon.AppendSSEEventBytes(output, "message_delta", template, 2)
		output = translatorcommon.AppendSSEEventBytes(output, "message_stop", []byte(`{"type":"message_stop"}`), 2)
	} else if typeStr == "response.output_item.added" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "function_call" {
			params.HasToolCall = true
			params.HasReceivedArgumentsDelta = false
			params.CurrentToolKey = itemResult.Get("id").String()
			if params.CurrentToolKey == "" {
				params.CurrentToolKey = itemResult.Get("call_id").String()
			}
			if params.CurrentToolKey == "" {
				params.CurrentToolKey = fmt.Sprintf("tool-%d", len(rawJSON))
			}
			{
				// Restore original tool name if shortened
				name := itemResult.Get("name").String()
				rev := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)
				if orig, ok := rev[name]; ok {
					name = orig
				}
				appendChunks(params.Lifecycle.EnsureToolUse(params.CurrentToolKey, itemResult.Get("call_id").String(), name))
			}
		}
	} else if typeStr == "response.output_item.done" {
		itemResult := rootResult.Get("item")
		itemType := itemResult.Get("type").String()
		if itemType == "function_call" {
			toolKey := itemResult.Get("id").String()
			if toolKey == "" {
				toolKey = itemResult.Get("call_id").String()
			}
			if toolKey == "" {
				toolKey = params.CurrentToolKey
			}
			appendChunks(params.Lifecycle.CloseToolUse(toolKey))
			if toolKey == params.CurrentToolKey {
				params.CurrentToolKey = ""
			}
		}
	} else if typeStr == "response.function_call_arguments.delta" {
		params.HasReceivedArgumentsDelta = true
		appendChunks(params.Lifecycle.AppendToolInput(params.CurrentToolKey, rootResult.Get("delta").String()))
	} else if typeStr == "response.function_call_arguments.done" {
		// Some models (e.g. gpt-5.3-codex-spark) send function call arguments
		// in a single "done" event without preceding "delta" events.
		// Emit the full arguments as a single input_json_delta so the
		// downstream Claude client receives the complete tool input.
		// When delta events were already received, skip to avoid duplicating arguments.
		if !params.HasReceivedArgumentsDelta {
			if args := rootResult.Get("arguments").String(); args != "" {
				appendChunks(params.Lifecycle.AppendToolInput(params.CurrentToolKey, args))
			}
		}
	}

	return [][]byte{output}
}

// ConvertCodexResponseToClaudeNonStream converts a non-streaming Codex response to a non-streaming Claude Code response.
// This function processes the complete Codex response and transforms it into a single Claude Code-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the Claude Code API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Codex API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: A Claude Code-compatible JSON response containing all message content and metadata
func ConvertCodexResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, _ []byte, rawJSON []byte, _ *any) []byte {
	revNames := buildReverseMapFromClaudeOriginalShortToOriginal(originalRequestRawJSON)

	rootResult := gjson.ParseBytes(rawJSON)
	if rootResult.Get("type").String() != "response.completed" {
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
				if thinkingBuilder.Len() > 0 {
					block := []byte(`{"type":"thinking","thinking":""}`)
					block, _ = sjson.SetBytes(block, "thinking", thinkingBuilder.String())
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

	if stopReason := responseData.Get("stop_reason"); stopReason.Exists() && stopReason.String() != "" {
		out, _ = sjson.SetBytes(out, "stop_reason", stopReason.String())
	} else if hasToolCall {
		out, _ = sjson.SetBytes(out, "stop_reason", "tool_use")
	} else {
		out, _ = sjson.SetBytes(out, "stop_reason", "end_turn")
	}

	if stopSequence := responseData.Get("stop_sequence"); stopSequence.Exists() && stopSequence.String() != "" {
		out, _ = sjson.SetRawBytes(out, "stop_sequence", []byte(stopSequence.Raw))
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

func ClaudeTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}
