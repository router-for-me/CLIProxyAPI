// Package bedrock provides response translation between AWS Bedrock Converse API
// and OpenAI Chat Completions format.
package bedrock

import (
	"bytes"
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// non-streaming

// ConvertBedrockResponseToOpenAINonStream converts a Bedrock Converse non-streaming
// response body to the OpenAI Chat Completions (non-stream) format.
func ConvertBedrockResponseToOpenAINonStream(_ context.Context, modelName string, _, _, rawJSON []byte, _ *any) []byte {
	root := gjson.ParseBytes(rawJSON)

	out := []byte(`{"id":"bedrock-msg","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	// model
	if model := root.Get("model"); model.Exists() {
		modelVal := strings.TrimSpace(model.String())
		if modelVal != "" {
			out, _ = sjson.SetBytes(out, "model", modelVal)
		}
	}

	// Build content and tool_calls from output.message.content blocks
	var textParts []string
	var reasoningParts []string
	var toolCalls []interface{}

	if contentBlocks := root.Get("output.message.content"); contentBlocks.Exists() && contentBlocks.IsArray() {
		contentBlocks.ForEach(func(_, block gjson.Result) bool {
			switch {
			case block.Get("text").Exists():
				textParts = append(textParts, block.Get("text").String())
			case block.Get("reasoningContent.reasoningText.text").Exists():
				reasoningParts = append(reasoningParts, block.Get("reasoningContent.reasoningText.text").String())
			case block.Get("reasoningContent.reasoning").Exists():
				reasoningParts = append(reasoningParts, block.Get("reasoningContent.reasoning").String())
			case block.Get("reasoningContent.text").Exists():
				reasoningParts = append(reasoningParts, block.Get("reasoningContent.text").String())
			case block.Get("reasoning.text").Exists():
				reasoningParts = append(reasoningParts, block.Get("reasoning.text").String())
			case block.Get("reasoning").Type == gjson.String:
				reasoningParts = append(reasoningParts, block.Get("reasoning").String())
			case block.Get("type").String() == "thinking":
				reasoningParts = append(reasoningParts, block.Get("thinking").String())
			case block.Get("toolUse").Exists():
				tu := block.Get("toolUse")
				tcJSON := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				tcJSON, _ = sjson.SetBytes(tcJSON, "id", tu.Get("toolUseId").String())
				tcJSON, _ = sjson.SetBytes(tcJSON, "function.name", tu.Get("name").String())
				if input := tu.Get("input"); input.Exists() {
					tcJSON, _ = sjson.SetBytes(tcJSON, "function.arguments", input.Raw)
				}
				toolCalls = append(toolCalls, gjson.ParseBytes(tcJSON).Value())
			}
			return true
		})
	}

	if len(textParts) > 0 {
		out, _ = sjson.SetBytes(out, "choices.0.message.content", strings.Join(textParts, ""))
	}
	if len(reasoningParts) > 0 {
		out, _ = sjson.SetBytes(out, "choices.0.message.reasoning_content", strings.Join(reasoningParts, ""))
	}
	if len(toolCalls) > 0 {
		out, _ = sjson.SetBytes(out, "choices.0.message.tool_calls", toolCalls)
		out, _ = sjson.SetBytes(out, "choices.0.finish_reason", "tool_calls")
	}

	// stop reason
	if stopReason := root.Get("stopReason"); stopReason.Exists() {
		out, _ = sjson.SetBytes(out, "choices.0.finish_reason", mapBedrockStopReasonToOpenAI(stopReason.String()))
	}

	// usage
	if usage := root.Get("usage"); usage.Exists() {
		inputTokens := usage.Get("inputTokens").Int()
		outputTokens := usage.Get("outputTokens").Int()
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens", inputTokens)
		out, _ = sjson.SetBytes(out, "usage.completion_tokens", outputTokens)
		out, _ = sjson.SetBytes(out, "usage.total_tokens", inputTokens+outputTokens)
	}

	return out
}

// streaming

// BedrockStreamState holds accumulation state across streaming chunks.
type BedrockStreamState struct {
	MessageID      string
	Model          string
	FinishReason   string
	TextBlockIdx   int
	ToolBlockIdx   map[int]string // Bedrock contentBlockIndex -> toolUseId
	ToolNameMap    map[int]string
	MessageStarted bool
}

// ConvertBedrockResponseToOpenAI translates a single Bedrock Converse streaming
// SSE chunk into one or more OpenAI-format SSE lines.
func ConvertBedrockResponseToOpenAI(_ context.Context, modelName string, _, _, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &BedrockStreamState{
			TextBlockIdx: -1,
			ToolBlockIdx: make(map[int]string),
			ToolNameMap:  make(map[int]string),
		}
	}
	state := (*param).(*BedrockStreamState)
	if state.Model == "" {
		state.Model = strings.TrimSpace(modelName)
	}

	// Bedrock SSE lines arrive as raw JSON (no "data:" prefix since the executor
	// reads event payloads directly). However the executor may forward them with
	// or without the "data:" prefix depending on mode.  Normalise here.
	line := bytes.TrimSpace(rawJSON)
	line = bytes.TrimPrefix(line, []byte("data:"))
	line = bytes.TrimSpace(line)

	if len(line) == 0 {
		return nil
	}

	root := gjson.ParseBytes(line)
	eventType := root.Get("type").String()

	var results [][]byte

	switch eventType {
	case "messageStart":
		state.MessageID = root.Get("p").String()
		if state.MessageID == "" {
			state.MessageID = "bedrock-stream"
		}
		role := root.Get("role").String()
		if role == "" {
			role = "assistant"
		}
		chunkJSON := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"role":"","content":""},"finish_reason":null}]}`)
		chunkJSON, _ = sjson.SetBytes(chunkJSON, "id", state.MessageID)
		chunkJSON, _ = sjson.SetBytes(chunkJSON, "model", state.Model)
		chunkJSON, _ = sjson.SetBytes(chunkJSON, "choices.0.delta.role", role)
		results = append(results, appendSSEDataLine(chunkJSON))
		state.MessageStarted = true

	case "contentBlockStart":
		blockIdx := int(root.Get("contentBlockIndex").Int())
		if toolUse := root.Get("contentBlock.toolUse"); toolUse.Exists() {
			toolID := toolUse.Get("toolUseId").String()
			toolName := toolUse.Get("name").String()
			state.ToolBlockIdx[blockIdx] = toolID
			state.ToolNameMap[blockIdx] = toolName

			// Emit tool_calls delta start
			chunkJSON := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"tool_calls":[]},"finish_reason":null}]}`)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "id", state.MessageID)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "model", state.Model)
			tcStart := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
			tcStart, _ = sjson.SetBytes(tcStart, "index", blockIdx)
			tcStart, _ = sjson.SetBytes(tcStart, "id", toolID)
			tcStart, _ = sjson.SetBytes(tcStart, "function.name", toolName)
			chunkJSON, _ = sjson.SetRawBytes(chunkJSON, "choices.0.delta.tool_calls.-1", tcStart)
			results = append(results, appendSSEDataLine(chunkJSON))
		}

	case "contentBlockDelta":
		blockIdx := int(root.Get("contentBlockIndex").Int())
		delta := root.Get("delta")

		if text := delta.Get("text"); text.Exists() {
			chunkJSON := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "id", state.MessageID)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "model", state.Model)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "choices.0.delta.content", text.String())
			results = append(results, appendSSEDataLine(chunkJSON))
		} else if reasoningDelta, ok := extractBedrockReasoningDelta(delta); ok {
			chunkJSON := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"reasoning_content":""},"finish_reason":null}]}`)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "id", state.MessageID)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "model", state.Model)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "choices.0.delta.reasoning_content", reasoningDelta)
			results = append(results, appendSSEDataLine(chunkJSON))
		} else if toolJSON := delta.Get("toolUse.input"); toolJSON.Exists() {
			// Streaming tool input JSON fragment
			chunkJSON := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"tool_calls":[]},"finish_reason":null}]}`)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "id", state.MessageID)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "model", state.Model)
			tcDelta := []byte(`{"index":0,"function":{"arguments":""}}`)
			tcDelta, _ = sjson.SetBytes(tcDelta, "index", blockIdx)
			tcDelta, _ = sjson.SetBytes(tcDelta, "function.arguments", toolJSON.String())
			chunkJSON, _ = sjson.SetRawBytes(chunkJSON, "choices.0.delta.tool_calls.-1", tcDelta)
			results = append(results, appendSSEDataLine(chunkJSON))
		}

	case "contentBlockStop":
		// no special action needed for OpenAI format

	case "messageStop":
		stopReason := root.Get("stopReason").String()
		state.FinishReason = mapBedrockStopReasonToOpenAI(stopReason)

		// Send final "finish_reason" chunk
		chunkJSON := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":""}]}`)
		chunkJSON, _ = sjson.SetBytes(chunkJSON, "id", state.MessageID)
		chunkJSON, _ = sjson.SetBytes(chunkJSON, "model", state.Model)
		chunkJSON, _ = sjson.SetBytes(chunkJSON, "choices.0.finish_reason", state.FinishReason)
		results = append(results, appendSSEDataLine(chunkJSON))
		results = append(results, appendSSEDone())

	case "metadata":
		// usage info; emit as a usage chunk
		usage := root.Get("usage")
		if usage.Exists() {
			inputTokens := usage.Get("inputTokens").Int()
			outputTokens := usage.Get("outputTokens").Int()
			chunkJSON := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "id", state.MessageID)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "model", state.Model)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "usage.prompt_tokens", inputTokens)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "usage.completion_tokens", outputTokens)
			chunkJSON, _ = sjson.SetBytes(chunkJSON, "usage.total_tokens", inputTokens+outputTokens)
			results = append(results, appendSSEDataLine(chunkJSON))
		}
	}

	return results
}

func appendSSEDataLine(jsonBytes []byte) []byte {
	out := make([]byte, 0, len(jsonBytes)+8)
	out = append(out, "data: "...)
	out = append(out, jsonBytes...)
	out = append(out, '\n', '\n')
	return out
}

func appendSSEDone() []byte {
	return []byte("data: [DONE]\n\n")
}

func mapBedrockStopReasonToOpenAI(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

func extractBedrockReasoningDelta(delta gjson.Result) (string, bool) {
	paths := []string{
		"reasoningContent.reasoningText.text",
		"reasoningContent.reasoning",
		"reasoningContent.text",
		"reasoning.text",
	}
	for _, path := range paths {
		if value := delta.Get(path); value.Exists() {
			return value.String(), true
		}
	}
	if value := delta.Get("reasoning"); value.Exists() && value.Type == gjson.String {
		return value.String(), true
	}
	if delta.Get("type").String() == "thinking_delta" {
		if value := delta.Get("thinking"); value.Exists() {
			return value.String(), true
		}
	}
	return "", false
}
