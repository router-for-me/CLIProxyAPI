package gemini

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGeminiInstructionsAndInput(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model:        "gemini-test",
		Instructions: mustGeminiRawMessage(t, "system rules"),
		Input:        mustGeminiRawMessage(t, "hello"),
	})

	require.NotNil(t, got.SystemInstructions)
	require.Len(t, got.SystemInstructions.Parts, 1)
	assert.Equal(t, "system rules", got.SystemInstructions.Parts[0].Text)
	require.Len(t, got.Contents, 1)
	assert.Equal(t, "user", got.Contents[0].Role)
	require.Len(t, got.Contents[0].Parts, 1)
	assert.Equal(t, "hello", got.Contents[0].Parts[0].Text)
}

func TestConvertOpenAIResponsesRequestToGeminiFunctionToolAndChoice(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustGeminiRawMessage(t, "lookup weather"),
		Tools: mustGeminiRawMessage(t, []map[string]any{
			{
				"type":        "function",
				"name":        "lookup",
				"description": "Lookup data",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"q": map[string]any{"type": "string"},
					},
				},
			},
			{"type": "custom", "name": "freeform"},
		}),
		ToolChoice: mustGeminiRawMessage(t, map[string]any{
			"type": "function",
			"name": "lookup",
		}),
	})

	tools := got.GetTools()
	require.Len(t, tools, 1)
	assert.Equal(t, "lookup", gjson.GetBytes(got.Tools, "0.functionDeclarations.0.name").String())
	assert.Equal(t, "Lookup data", gjson.GetBytes(got.Tools, "0.functionDeclarations.0.description").String())
	require.NotNil(t, got.ToolConfig)
	require.NotNil(t, got.ToolConfig.FunctionCallingConfig)
	assert.Equal(t, dto.FunctionCallingConfigMode("ANY"), got.ToolConfig.FunctionCallingConfig.Mode)
	assert.Equal(t, []string{"lookup"}, got.ToolConfig.FunctionCallingConfig.AllowedFunctionNames)
}

func TestConvertOpenAIResponsesRequestToGeminiFunctionCallConversation(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustGeminiRawMessage(t, []map[string]any{
			{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "I will call."},
				},
			},
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": map[string]any{"q": "x"},
			},
			{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  map[string]any{"ok": true},
			},
		}),
		Tools: mustGeminiRawMessage(t, []map[string]any{
			{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
		}),
	})

	require.Len(t, got.Contents, 2)
	assert.Equal(t, "model", got.Contents[0].Role)
	require.Len(t, got.Contents[0].Parts, 2)
	require.NotNil(t, got.Contents[0].Parts[0].FunctionCall)
	assert.Equal(t, "lookup", got.Contents[0].Parts[0].FunctionCall.FunctionName)
	assert.Equal(t, map[string]any{"q": "x"}, got.Contents[0].Parts[0].FunctionCall.Arguments)
	assert.Equal(t, "I will call.", got.Contents[0].Parts[1].Text)

	assert.Equal(t, "user", got.Contents[1].Role)
	require.Len(t, got.Contents[1].Parts, 1)
	require.NotNil(t, got.Contents[1].Parts[0].FunctionResponse)
	assert.Equal(t, "lookup", got.Contents[1].Parts[0].FunctionResponse.Name)
	assert.Equal(t, map[string]any{"ok": true}, got.Contents[1].Parts[0].FunctionResponse.Response)
}

func TestConvertOpenAIResponsesRequestToGeminiSkipsCustomToolCalls(t *testing.T) {
	got := mustConvertResponsesToGemini(t, dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustGeminiRawMessage(t, []map[string]any{
			{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "before custom"},
				},
			},
			{
				"type":    "custom_tool_call",
				"call_id": "call_custom",
				"name":    "apply_patch",
				"input":   "patch body",
			},
			{
				"type":    "custom_tool_call_output",
				"call_id": "call_custom",
				"output":  "ok",
			},
			{
				"type":    "function_call_output",
				"call_id": "call_custom",
				"output":  "legacy custom output",
			},
			{
				"role":    "user",
				"content": "next turn",
			},
		}),
		Tools: mustGeminiRawMessage(t, []map[string]any{
			{"type": "custom", "name": "apply_patch"},
			{"type": "unknown", "name": "unknown"},
		}),
	})

	assert.Empty(t, got.GetTools())
	require.Len(t, got.Contents, 2)
	assert.Equal(t, "model", got.Contents[0].Role)
	require.Len(t, got.Contents[0].Parts, 1)
	assert.Equal(t, "before custom", got.Contents[0].Parts[0].Text)
	assert.Nil(t, got.Contents[0].Parts[0].FunctionCall)

	assert.Equal(t, "user", got.Contents[1].Role)
	require.Len(t, got.Contents[1].Parts, 1)
	assert.Equal(t, "next turn", got.Contents[1].Parts[0].Text)
	assert.Nil(t, got.Contents[1].Parts[0].FunctionResponse)
}

func mustConvertResponsesToGemini(t *testing.T, req dto.OpenAIResponsesRequest) *dto.GeminiChatRequest {
	t.Helper()
	info := &relaycommon.RelayInfo{
		OriginModelName: req.Model,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: req.Model,
		},
	}
	got, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, req)
	require.NoError(t, err)
	geminiReq, ok := got.(*dto.GeminiChatRequest)
	require.True(t, ok)
	return geminiReq
}

func mustGeminiRawMessage(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := common.Marshal(value)
	require.NoError(t, err)
	return raw
}
