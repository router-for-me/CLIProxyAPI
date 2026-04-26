// Package claude provides response translation functionality for Claude Code API compatibility.
// This package handles the conversion of backend client responses into Claude Code-compatible
// Server-Sent Events (SSE) format, implementing a sophisticated state machine that manages
// different response types including text content, thinking processes, and function calls.
// The translation ensures proper sequencing of SSE events and maintains state across
// multiple response chunks to provide a seamless streaming experience.
package claude

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/claude/streamstate"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Params holds parameters for response conversion and maintains state across streaming chunks.
// This structure tracks the current state of the response translation process to ensure
// proper sequencing of SSE events and transitions between different content types.
type Params struct {
	HasFirstResponse bool // Indicates if the initial message_start event has been sent
	HasContent       bool // Tracks whether any content (text, thinking, or tool use) has been output

	// Reverse map: sanitized Gemini function name → original Claude tool name.
	ToolNameMap    map[string]string
	CurrentToolKey string
	ToolUseCount   int
	Lifecycle      *streamstate.Lifecycle
}

// toolUseIDCounter provides a process-wide unique counter for tool use identifiers.
var toolUseIDCounter uint64

// ConvertGeminiCLIResponseToClaude performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates backend client responses
// into Claude Code-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini CLI API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of bytes, each containing a Claude Code-compatible SSE payload.
func ConvertGeminiCLIResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &Params{
			HasFirstResponse: false,
			ToolNameMap:      util.SanitizedToolNameMap(originalRequestRawJSON),
			Lifecycle:        streamstate.NewLifecycle(),
		}
	}
	params := (*param).(*Params)
	if params.Lifecycle == nil {
		params.Lifecycle = streamstate.NewLifecycle()
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		// Only send message_stop if we have actually output content
		if params.HasContent {
			output := make([]byte, 0, 256)
			for _, chunk := range params.Lifecycle.CloseAll() {
				output = append(output, chunk...)
			}
			output = translatorcommon.AppendSSEEventString(output, "message_stop", `{"type":"message_stop"}`, 3)
			return [][]byte{output}
		}
		return [][]byte{}
	}

	// Track whether tools are being used in this response chunk
	usedTool := false
	output := make([]byte, 0, 1024)
	appendEvent := func(event, payload string) {
		output = translatorcommon.AppendSSEEventString(output, event, payload, 3)
	}
	appendChunks := func(chunks [][]byte) {
		for _, chunk := range chunks {
			output = append(output, chunk...)
		}
	}

	// Initialize the streaming session with a message_start event
	// This is only sent for the very first response chunk to establish the streaming session
	if !params.HasFirstResponse {
		// Create the initial message structure with default values according to Claude Code API specification
		// This follows the Claude Code API specification for streaming message initialization
		messageStartTemplate := []byte(`{"type":"message_start","message":{"id":"msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)

		// Override default values with actual response metadata if available from the Gemini CLI response
		if modelVersionResult := gjson.GetBytes(rawJSON, "response.modelVersion"); modelVersionResult.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.model", modelVersionResult.String())
		}
		if responseIDResult := gjson.GetBytes(rawJSON, "response.responseId"); responseIDResult.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.id", responseIDResult.String())
		}
		appendEvent("message_start", string(messageStartTemplate))

		params.HasFirstResponse = true
	}

	// Process the response parts array from the backend client
	// Each part can contain text content, thinking content, or function calls
	partsResult := gjson.GetBytes(rawJSON, "response.candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]

			// Extract the different types of content from each part
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")

			// Handle text content (both regular content and thinking)
			if partTextResult.Exists() {
				// Process thinking content (internal reasoning)
				if partResult.Get("thought").Bool() {
					appendChunks(params.Lifecycle.AppendThinking(partTextResult.String()))
					params.HasContent = true
				} else {
					appendChunks(params.Lifecycle.AppendText(partTextResult.String()))
					params.HasContent = true
				}
			} else if functionCallResult.Exists() {
				// Handle function/tool calls from the AI model
				// This processes tool usage requests and formats them for Claude Code API compatibility
				usedTool = true
				fcName := util.RestoreSanitizedToolName(params.ToolNameMap, functionCallResult.Get("name").String())
				if fcName == "" && params.CurrentToolKey != "" {
					if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
						appendChunks(params.Lifecycle.AppendToolInput(params.CurrentToolKey, fcArgsResult.Raw))
						params.HasContent = true
					}
					continue
				}

				if params.CurrentToolKey != "" {
					appendChunks(params.Lifecycle.CloseToolUse(params.CurrentToolKey))
				}

				params.ToolUseCount++
				params.CurrentToolKey = fmt.Sprintf("tool-%d", params.ToolUseCount)
				toolID := fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&toolUseIDCounter, 1))
				appendChunks(params.Lifecycle.EnsureToolUse(params.CurrentToolKey, toolID, fcName))

				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					appendChunks(params.Lifecycle.AppendToolInput(params.CurrentToolKey, fcArgsResult.Raw))
				}
				params.HasContent = true
			}
		}
	}

	usageResult := gjson.GetBytes(rawJSON, "response.usageMetadata")
	// Process usage metadata and finish reason when present in the response
	if usageResult.Exists() && bytes.Contains(rawJSON, []byte(`"finishReason"`)) {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			// Only send final events if we have actually output content
			if params.HasContent {
				appendChunks(params.Lifecycle.CloseAll())
				// Create the message delta template with appropriate stop reason
				template := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
				// Set tool_use stop reason if tools were used in this response
				if usedTool {
					template = []byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
				} else if finish := gjson.GetBytes(rawJSON, "response.candidates.0.finishReason"); finish.Exists() && finish.String() == "MAX_TOKENS" {
					template = []byte(`{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
				}

				// Include thinking tokens in output token count if present
				thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
				template, _ = sjson.SetBytes(template, "usage.output_tokens", candidatesTokenCountResult.Int()+thoughtsTokenCount)
				template, _ = sjson.SetBytes(template, "usage.input_tokens", usageResult.Get("promptTokenCount").Int())

				appendEvent("message_delta", string(template))
			}
		}
	}

	return [][]byte{output}
}

// ConvertGeminiCLIResponseToClaudeNonStream converts a non-streaming Gemini CLI response to a non-streaming Claude response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the Gemini CLI API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - []byte: A Claude-compatible JSON response.
func ConvertGeminiCLIResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	toolNameMap := util.SanitizedToolNameMap(originalRequestRawJSON)
	_ = requestRawJSON

	root := gjson.ParseBytes(rawJSON)

	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", root.Get("response.responseId").String())
	out, _ = sjson.SetBytes(out, "model", root.Get("response.modelVersion").String())

	inputTokens := root.Get("response.usageMetadata.promptTokenCount").Int()
	outputTokens := root.Get("response.usageMetadata.candidatesTokenCount").Int() + root.Get("response.usageMetadata.thoughtsTokenCount").Int()
	out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)

	parts := root.Get("response.candidates.0.content.parts")
	textBuilder := strings.Builder{}
	thinkingBuilder := strings.Builder{}
	toolIDCounter := 0
	hasToolCall := false

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

	if parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() && text.String() != "" {
				if part.Get("thought").Bool() {
					flushText()
					thinkingBuilder.WriteString(text.String())
					continue
				}
				flushThinking()
				textBuilder.WriteString(text.String())
				continue
			}

			if functionCall := part.Get("functionCall"); functionCall.Exists() {
				flushThinking()
				flushText()
				hasToolCall = true

				name := util.RestoreSanitizedToolName(toolNameMap, functionCall.Get("name").String())
				toolIDCounter++
				toolBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolBlock, _ = sjson.SetBytes(toolBlock, "id", fmt.Sprintf("tool_%d", toolIDCounter))
				toolBlock, _ = sjson.SetBytes(toolBlock, "name", name)
				inputRaw := "{}"
				if args := functionCall.Get("args"); args.Exists() && gjson.Valid(args.Raw) && args.IsObject() {
					inputRaw = args.Raw
				}
				toolBlock, _ = sjson.SetRawBytes(toolBlock, "input", []byte(inputRaw))
				out, _ = sjson.SetRawBytes(out, "content.-1", toolBlock)
				continue
			}
		}
	}

	flushThinking()
	flushText()

	stopReason := "end_turn"
	if hasToolCall {
		stopReason = "tool_use"
	} else {
		if finish := root.Get("response.candidates.0.finishReason"); finish.Exists() {
			switch finish.String() {
			case "MAX_TOKENS":
				stopReason = "max_tokens"
			case "STOP", "FINISH_REASON_UNSPECIFIED", "UNKNOWN":
				stopReason = "end_turn"
			default:
				stopReason = "end_turn"
			}
		}
	}
	out, _ = sjson.SetBytes(out, "stop_reason", stopReason)

	if inputTokens == int64(0) && outputTokens == int64(0) && !root.Get("response.usageMetadata").Exists() {
		out, _ = sjson.DeleteBytes(out, "usage")
	}

	return out
}

func ClaudeTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}
