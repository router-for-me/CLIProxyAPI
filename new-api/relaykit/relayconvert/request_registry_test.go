package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	sharedgemini "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/gemini"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestConverterRegistryListsSupportedTextConverters(t *testing.T) {
	tests := []struct {
		converter      string
		from           types.RelayFormat
		to             types.RelayFormat
		quality        RequestConverterQuality
		stepConverters []string
		advancedCustom bool
	}{
		{converter: ConverterClaudeMessagesToOpenAIChat, from: types.RelayFormatClaude, to: types.RelayFormatOpenAI, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterGeminiContentToOpenAIChat, from: types.RelayFormatGemini, to: types.RelayFormatOpenAI, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterOpenAIChatToClaudeMessages, from: types.RelayFormatOpenAI, to: types.RelayFormatClaude, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterOpenAIChatToGeminiContent, from: types.RelayFormatOpenAI, to: types.RelayFormatGemini, quality: RequestConverterQualityFair, advancedCustom: true},
		{converter: ConverterOpenAIChatToOpenAIResponses, from: types.RelayFormatOpenAI, to: types.RelayFormatOpenAIResponses, quality: RequestConverterQualityGood, advancedCustom: true},
		{converter: ConverterOpenAIResponsesToOpenAIChat, from: types.RelayFormatOpenAIResponses, to: types.RelayFormatOpenAI, quality: RequestConverterQualityGood, advancedCustom: true},
		{
			converter: requestConverterClaudeToGemini,
			from:      types.RelayFormatClaude,
			to:        types.RelayFormatGemini,
			quality:   RequestConverterQualityDiscouraged,
			stepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
		},
		{
			converter: requestConverterClaudeToResponses,
			from:      types.RelayFormatClaude,
			to:        types.RelayFormatOpenAIResponses,
			quality:   RequestConverterQualityFair,
		},
		{
			converter: requestConverterGeminiToClaude,
			from:      types.RelayFormatGemini,
			to:        types.RelayFormatClaude,
			quality:   RequestConverterQualityDiscouraged,
			stepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToClaudeMessages,
			},
		},
		{
			converter: requestConverterGeminiToResponses,
			from:      types.RelayFormatGemini,
			to:        types.RelayFormatOpenAIResponses,
			quality:   RequestConverterQualityFair,
			stepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
		},
		{
			converter: requestConverterResponsesToClaude,
			from:      types.RelayFormatOpenAIResponses,
			to:        types.RelayFormatClaude,
			quality:   RequestConverterQualityFair,
		},
		{
			converter:      ConverterOpenAIResponsesToGemini,
			from:           types.RelayFormatOpenAIResponses,
			to:             types.RelayFormatGemini,
			quality:        RequestConverterQualityFair,
			advancedCustom: true,
		},
	}

	require.Len(t, requestConverters, len(tests))

	for _, tt := range tests {
		t.Run(tt.converter, func(t *testing.T) {
			spec, ok := LookupRequestConverter(tt.converter)

			require.True(t, ok)
			assert.Equal(t, tt.converter, spec.ID)
			assert.Equal(t, tt.from, spec.From)
			assert.Equal(t, tt.to, spec.To)
			assert.Equal(t, tt.quality, spec.Quality)
			assert.Equal(t, tt.stepConverters, spec.StepConverters)
			if len(tt.stepConverters) == 0 {
				assert.NotNil(t, spec.Convert)
			} else {
				assert.Nil(t, spec.Convert)
			}
			assert.Equal(t, tt.advancedCustom, dto.IsAdvancedCustomConverterAllowed(tt.converter))
		})
	}
}

func TestConvertRequestToTargetRecordsConversionChain(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatOpenAI},
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, types.RelayFormatOpenAI, result.From)
	assert.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), result.To)
	assert.Equal(t, ConverterOpenAIChatToOpenAIResponses, result.Converter)
	assert.Equal(t, RequestConverterQualityGood, result.Quality)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIChatToOpenAIResponses,
			From:      types.RelayFormatOpenAI,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestClaudeToResponsesUsesDirectPath(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatClaude},
	}
	req := &dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, types.RelayFormat(types.RelayFormatClaude), result.From)
	assert.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), result.To)
	assert.Equal(t, requestConverterClaudeToResponses, result.Converter)
	assert.Equal(t, RequestConverterQualityFair, result.Quality)
	assert.Equal(t, []RequestStep{
		{
			Converter: requestConverterClaudeToResponses,
			From:      types.RelayFormatClaude,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestClaudeToChatResolvesToolResultNames(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "call_1", Content: "before call"},
			}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_use", Id: "call_1", Name: "first", Input: map[string]any{}},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "call_1", Content: "after call"},
				{Type: "tool_result", ToolUseId: "missing", Content: "unknown"},
				{Type: "tool_result", ToolUseId: "call_1", Name: "explicit", Content: "named"},
			}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_use", Id: "call_1", Name: "later", Input: map[string]any{}},
			}},
		},
	}

	result, err := ConvertRequestByID(nil, nil, ConverterClaudeMessagesToOpenAIChat, req)
	require.NoError(t, err)
	chatReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Len(t, chatReq.Messages, 6)
	for _, tt := range []struct {
		index int
		id    string
		name  string
	}{
		{0, "call_1", "first"},
		{2, "call_1", "first"},
		{3, "missing", ""},
		{4, "call_1", "explicit"},
	} {
		message := chatReq.Messages[tt.index]
		assert.Equal(t, "tool", message.Role)
		assert.Equal(t, tt.id, message.ToolCallId)
		require.NotNil(t, message.Name)
		assert.Equal(t, tt.name, *message.Name)
	}
	assert.Equal(t, "assistant", chatReq.Messages[1].Role)
	assert.Equal(t, "assistant", chatReq.Messages[5].Role)
}

func TestConvertRequestClaudeToResponsesPreservesMixedBlockOrder(t *testing.T) {
	info := &convmeta.Values{ConversionChain: []types.RelayFormat{types.RelayFormatClaude}}
	stream := true
	strict := true
	maxTokens := uint(4096)
	req := &dto.ClaudeRequest{
		Model:     "gpt-test",
		System:    []dto.ClaudeMediaMessage{{Type: "text", Text: kitutil.GetPointer("system ")}, {Type: "text", Text: kitutil.GetPointer("rules")}},
		MaxTokens: &maxTokens,
		Stream:    &stream,
		Tools: []dto.Tool{{
			Name:        "lookup",
			Description: "Look up a value",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
			Strict:      &strict,
		}},
		ToolChoice: dto.ClaudeToolChoice{Type: "tool", Name: "lookup", DisableParallelToolUse: true},
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: []dto.ClaudeMediaMessage{{Type: "text", Text: kitutil.GetPointer("question")}}},
			{Role: "assistant", Content: []dto.ClaudeMediaMessage{
				{Type: "text", Text: kitutil.GetPointer("before")},
				{Type: "tool_use", Id: "call_1", Name: "lookup", Input: map[string]any{"q": "x"}},
				{Type: "text", Text: kitutil.GetPointer("after")},
			}},
			{Role: "user", Content: []dto.ClaudeMediaMessage{
				{Type: "tool_result", ToolUseId: "call_1", Content: "result"},
				{Type: "text", Text: kitutil.GetPointer("continue")},
			}},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)
	require.NoError(t, err)
	responsesReq := result.Value.(*dto.OpenAIResponsesRequest)
	assert.Equal(t, "gpt-test", responsesReq.Model)
	assert.Equal(t, maxTokens, *responsesReq.MaxOutputTokens)
	assert.True(t, *responsesReq.Stream)
	assert.JSONEq(t, `"system rules"`, string(responsesReq.Instructions))
	assert.JSONEq(t, `[{"type":"function","name":"lookup","description":"Look up a value","parameters":{"type":"object","properties":{"q":{"type":"string"}}},"strict":true}]`, string(responsesReq.Tools))
	assert.JSONEq(t, `{"type":"function","name":"lookup"}`, string(responsesReq.ToolChoice))
	assert.JSONEq(t, `false`, string(responsesReq.ParallelToolCalls))

	var input []map[string]any
	require.NoError(t, kitutil.Unmarshal(responsesReq.Input, &input))
	require.Len(t, input, 6)
	assert.Equal(t, "user", input[0]["role"])
	assert.Equal(t, "question", inputContentText(t, input[0]))
	assert.Equal(t, "assistant", input[1]["role"])
	assert.Equal(t, "before", inputContentText(t, input[1]))
	assert.Equal(t, "function_call", input[2]["type"])
	assert.Equal(t, "call_1", input[2]["call_id"])
	assert.Equal(t, "lookup", input[2]["name"])
	assert.JSONEq(t, `{"q":"x"}`, input[2]["arguments"].(string))
	assert.Equal(t, "assistant", input[3]["role"])
	assert.Equal(t, "after", inputContentText(t, input[3]))
	assert.Equal(t, "function_call_output", input[4]["type"])
	assert.Equal(t, "result", input[4]["output"])
	assert.Equal(t, "user", input[5]["role"])
	assert.Equal(t, "continue", inputContentText(t, input[5]))
}

func TestConvertRequestClaudeToResponsesDropsIncompatibleContextManagement(t *testing.T) {
	req := &dto.ClaudeRequest{
		Model: "gpt-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
		ContextManagement: mustRawMessage(t, map[string]any{
			"edits": []map[string]any{{"type": "clear_tool_uses_20250919"}},
		}),
	}

	result, err := ConvertRequest(nil, nil, types.RelayFormatOpenAIResponses, req)

	require.NoError(t, err)
	responsesReq, ok := result.Value.(*dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.Empty(t, responsesReq.ContextManagement)
}

func TestConvertRequestClaudeAdaptiveThinkingPreservesEffort(t *testing.T) {
	tests := []struct {
		name         string
		outputConfig []byte
		wantEffort   string
	}{
		{name: "adaptive default", wantEffort: "high"},
		{name: "explicit low", outputConfig: mustRawMessage(t, map[string]any{"effort": "low"}), wantEffort: "low"},
		{name: "explicit xhigh", outputConfig: mustRawMessage(t, map[string]any{"effort": "xhigh"}), wantEffort: "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &convmeta.Values{
				OriginModelName: "gpt-5.6-sol",
				ConversionChain: []types.RelayFormat{types.RelayFormatClaude},
			}
			req := &dto.ClaudeRequest{
				Model:        "gpt-5.6-sol",
				OutputConfig: tt.outputConfig,
				Thinking:     &dto.Thinking{Type: "adaptive", Display: "summarized"},
				Messages: []dto.ClaudeMessage{
					{Role: "user", Content: "hello"},
				},
			}

			result, err := ConvertRequest(nil, info, types.RelayFormatOpenAIResponses, req)

			require.NoError(t, err)
			responsesReq, ok := result.Value.(*dto.OpenAIResponsesRequest)
			require.True(t, ok)
			require.NotNil(t, responsesReq.Reasoning)
			assert.Equal(t, tt.wantEffort, responsesReq.Reasoning.Effort)
			assert.Equal(t, "detailed", responsesReq.Reasoning.Summary)
			assert.Equal(t, tt.wantEffort, info.GetReasoningEffort())
		})
	}
}

func TestGeminiThinkingLevelCaseInsensitiveAcrossPaths(t *testing.T) {
	newRequest := func(level string) *dto.GeminiChatRequest {
		return &dto.GeminiChatRequest{
			Contents: []dto.GeminiChatContent{{Role: "user", Parts: []dto.GeminiPart{{Text: "hello"}}}},
			GenerationConfig: dto.GeminiChatGenerationConfig{
				ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: level},
			},
		}
	}

	t.Run("native passthrough records canonical effort without rewriting wire value", func(t *testing.T) {
		info := &convmeta.Values{OriginModelName: "gemini-3.7-flash", UpstreamModelName: "gemini-3.7-flash"}
		req := newRequest(" MEDIUM ")
		require.NoError(t, ApplyGeminiThinkingConfigChecked(req, info))
		assert.Equal(t, "medium", info.GetReasoningEffort())
		assert.Equal(t, " MEDIUM ", req.GenerationConfig.ThinkingConfig.ThinkingLevel)
	})

	t.Run("native passthrough keeps unknown level as sent", func(t *testing.T) {
		info := &convmeta.Values{OriginModelName: "gemini-3.7-flash", UpstreamModelName: "gemini-3.7-flash"}
		req := newRequest("ULTRA")
		require.NoError(t, ApplyGeminiThinkingConfigChecked(req, info))
		assert.Equal(t, "ULTRA", info.GetReasoningEffort())
	})

	t.Run("suffix state canonicalizes uppercase level against normalized effort", func(t *testing.T) {
		info := &convmeta.Values{
			OriginModelName:     "gemini-3.7-flash-thinking-medium",
			UpstreamModelName:   "gemini-3.7-flash",
			ChannelMetaAttached: true,
			ReasoningConversion: &dto.ReasoningConversionState{Mode: "enabled", Effort: "medium"},
		}
		req := newRequest("MEDIUM")
		require.NoError(t, ApplyGeminiThinkingConfigChecked(req, info))
		assert.Equal(t, "medium", info.GetReasoningEffort())
		assert.Equal(t, "medium", req.GenerationConfig.ThinkingConfig.ThinkingLevel)
	})

	t.Run("gemini to openai conversion accepts uppercase level", func(t *testing.T) {
		info := &convmeta.Values{
			OriginModelName:   "gemini-3.7-flash",
			UpstreamModelName: "gemini-3.7-flash",
			ConversionChain:   []types.RelayFormat{types.RelayFormatGemini},
		}
		result, err := ConvertRequest(nil, info, types.RelayFormatOpenAI, newRequest("MEDIUM"))
		require.NoError(t, err)
		openaiReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		assert.Equal(t, "medium", openaiReq.ReasoningEffort)
		assert.Equal(t, "medium", info.GetReasoningEffort())
	})

	t.Run("gemini to openai conversion adjusts unsupported level with a diagnostic", func(t *testing.T) {
		info := &convmeta.Values{
			OriginModelName:   "gemini-3-pro-preview",
			UpstreamModelName: "gemini-3-pro-preview",
			ConversionChain:   []types.RelayFormat{types.RelayFormatGemini},
		}
		result, err := ConvertRequest(nil, info, types.RelayFormatOpenAI, newRequest("MINIMAL"))
		require.NoError(t, err)
		openaiReq, ok := result.Value.(*dto.GeneralOpenAIRequest)
		require.True(t, ok)
		assert.Equal(t, "low", openaiReq.ReasoningEffort)
		assert.Equal(t, "low", info.GetReasoningEffort())
		codes := make([]string, 0, len(result.Diagnostics))
		for _, diagnostic := range result.Diagnostics {
			codes = append(codes, diagnostic.Code)
		}
		assert.Contains(t, codes, "gemini_level_adjusted")
	})
}

func TestConvertRequestViaExecutesExplicitPath(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatOpenAI},
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequestVia(nil, info, req, types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIChatToOpenAIResponses,
			From:      types.RelayFormatOpenAI,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestResponsesToGeminiAppliesResponsesPreprocess(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAIResponses},
		ChannelMetaAttached: true,
		UpstreamModelName:   "gemini-test",
	}
	req := &dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"role":    "user",
				"content": "next turn",
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
		}),
		Tools: mustRawMessage(t, []map[string]any{
			{"type": "custom", "name": "apply_patch"},
		}),
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatGemini, req)

	require.NoError(t, err)
	geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
	require.True(t, ok)
	assert.Empty(t, geminiReq.GetTools())
	require.Len(t, geminiReq.Contents, 1)
	assert.Equal(t, "user", geminiReq.Contents[0].Role)
	require.Len(t, geminiReq.Contents[0].Parts, 1)
	assert.Equal(t, "next turn", geminiReq.Contents[0].Parts[0].Text)
	assert.Equal(t, ConverterOpenAIResponsesToGemini, result.Converter)
	assert.Equal(t, RequestConverterQualityFair, result.Quality)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIResponsesToGemini,
			From:      types.RelayFormatOpenAIResponses,
			To:        types.RelayFormatGemini,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAIResponses, types.RelayFormatGemini}, info.ConversionChain)
}

func TestConvertRequestResponsesToGeminiUsesDirectConverter(t *testing.T) {
	info := &convmeta.Values{
		Options:             &convmeta.Options{Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: true}},
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAIResponses},
		ChannelMetaAttached: true,
		UpstreamModelName:   "gemini-test",
	}
	maxOutputTokens := uint(256)
	req := &dto.OpenAIResponsesRequest{
		Model:           "gemini-test",
		Instructions:    mustRawMessage(t, "system rules"),
		MaxOutputTokens: &maxOutputTokens,
		Input: mustRawMessage(t, []map[string]any{
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
		Tools: mustRawMessage(t, []map[string]any{
			{
				"type":        "function",
				"name":        "lookup",
				"description": "Lookup data",
				"parameters": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"propertyNames":        map[string]any{"pattern": "^[a-z]+$"},
					"properties": map[string]any{
						"q": map[string]any{
							"type":             "string",
							"exclusiveMinimum": 0,
						},
						"filters": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": true,
								"properties": map[string]any{
									"name": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
		}),
		Text: mustRawMessage(t, map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   "answer",
				"schema": map[string]any{"type": "object"},
			},
		}),
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatGemini, req)

	require.NoError(t, err)
	geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
	require.True(t, ok)
	assert.Equal(t, ConverterOpenAIResponsesToGemini, result.Converter)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIResponsesToGemini,
			From:      types.RelayFormatOpenAIResponses,
			To:        types.RelayFormatGemini,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAIResponses, types.RelayFormatGemini}, info.ConversionChain)

	require.NotNil(t, geminiReq.SystemInstructions)
	require.Len(t, geminiReq.SystemInstructions.Parts, 1)
	assert.Equal(t, "system rules", geminiReq.SystemInstructions.Parts[0].Text)
	assert.Equal(t, "application/json", geminiReq.GenerationConfig.ResponseMimeType)
	assert.Equal(t, maxOutputTokens, *geminiReq.GenerationConfig.MaxOutputTokens)

	tools := geminiReq.GetTools()
	require.Len(t, tools, 1)
	functions, err := kitutil.Any2Type[[]dto.FunctionRequest](tools[0].FunctionDeclarations)
	require.NoError(t, err)
	require.Len(t, functions, 1)
	assert.Equal(t, "lookup", functions[0].Name)
	params, ok := functions[0].Parameters.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "OBJECT", params["type"])
	assert.NotContains(t, params, "additionalProperties")
	assert.NotContains(t, params, "propertyNames")
	properties, ok := params["properties"].(map[string]any)
	require.True(t, ok)
	queryParam, ok := properties["q"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "STRING", queryParam["type"])
	assert.NotContains(t, queryParam, "exclusiveMinimum")
	filterParam, ok := properties["filters"].(map[string]any)
	require.True(t, ok)
	filterItems, ok := filterParam["items"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, filterItems, "additionalProperties")

	require.Len(t, geminiReq.Contents, 2)
	assert.Equal(t, "model", geminiReq.Contents[0].Role)
	require.Len(t, geminiReq.Contents[0].Parts, 2)
	functionCall := geminiReq.Contents[0].Parts[0].FunctionCall
	require.NotNil(t, functionCall)
	assert.Equal(t, "lookup", functionCall.FunctionName)
	assert.Equal(t, map[string]any{"q": "x"}, functionCall.Arguments)
	var thoughtSignature string
	require.NoError(t, kitutil.Unmarshal(geminiReq.Contents[0].Parts[0].ThoughtSignature, &thoughtSignature))
	assert.Equal(t, sharedgemini.ThoughtSignatureBypassValue, thoughtSignature)
	assert.Equal(t, "I will call.", geminiReq.Contents[0].Parts[1].Text)

	assert.Equal(t, "user", geminiReq.Contents[1].Role)
	require.Len(t, geminiReq.Contents[1].Parts, 1)
	functionResponse := geminiReq.Contents[1].Parts[0].FunctionResponse
	require.NotNil(t, functionResponse)
	assert.Equal(t, "lookup", functionResponse.Name)
	assert.Equal(t, true, functionResponse.Response["ok"])
	assert.Empty(t, geminiReq.Contents[1].Parts[0].ThoughtSignature)
}

func TestConvertRequestResponsesToGeminiSkipsThoughtSignatureWhenDisabled(t *testing.T) {
	info := &convmeta.Values{
		Options:             &convmeta.Options{Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: false}},
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAIResponses},
		ChannelMetaAttached: true,
		UpstreamModelName:   "gemini-test",
	}
	req := &dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": map[string]any{"q": "x"},
			},
		}),
		Tools: mustRawMessage(t, []map[string]any{
			{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
		}),
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatGemini, req)

	require.NoError(t, err)
	geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
	require.True(t, ok)
	require.Len(t, geminiReq.Contents, 1)
	require.Len(t, geminiReq.Contents[0].Parts, 1)
	require.NotNil(t, geminiReq.Contents[0].Parts[0].FunctionCall)
	assert.Empty(t, geminiReq.Contents[0].Parts[0].ThoughtSignature)
}

func TestConvertRequestOpenAIChatToGeminiAddsThoughtSignatureForAdvancedCustom(t *testing.T) {
	assistantMessage := dto.Message{Role: "assistant", Content: ""}
	assistantMessage.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   "call_1",
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "lookup",
				Arguments: `{"q":"x"}`,
			},
		},
	})
	info := &convmeta.Values{
		Options:             &convmeta.Options{Gemini: convmeta.GeminiOptions{FunctionCallThoughtSignatureEnabled: true}},
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAI},
		ChannelMetaAttached: true,
		ChannelType:         58, // advanced-custom in the host
		UpstreamModelName:   "gemini-test",
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gemini-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hi"},
			assistantMessage,
			{Role: "tool", ToolCallId: "call_1", Content: `{"ok":true}`},
		},
		Tools: []dto.ToolCallRequest{
			{
				Type: "function",
				Function: dto.FunctionRequest{
					Name:       "lookup",
					Parameters: map[string]any{"type": "object"},
				},
			},
		},
	}

	result, err := ConvertRequest(nil, info, types.RelayFormatGemini, req)

	require.NoError(t, err)
	geminiReq, ok := result.Value.(*dto.GeminiChatRequest)
	require.True(t, ok)
	require.Len(t, geminiReq.Contents, 3)
	assert.Equal(t, "model", geminiReq.Contents[1].Role)
	require.Len(t, geminiReq.Contents[1].Parts, 1)
	require.NotNil(t, geminiReq.Contents[1].Parts[0].FunctionCall)
	var thoughtSignature string
	require.NoError(t, kitutil.Unmarshal(geminiReq.Contents[1].Parts[0].ThoughtSignature, &thoughtSignature))
	assert.Equal(t, sharedgemini.ThoughtSignatureBypassValue, thoughtSignature)
}

func TestConvertRequestViaResponsesToGeminiStillUsesDirectSteps(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain:     []types.RelayFormat{types.RelayFormatOpenAIResponses},
		ChannelMetaAttached: true,
		UpstreamModelName:   "gemini-test",
	}
	req := &dto.OpenAIResponsesRequest{
		Model: "gemini-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"role":    "user",
				"content": "hello",
			},
		}),
	}

	result, err := ConvertRequestVia(nil, info, req, types.RelayFormatOpenAI, types.RelayFormatGemini)

	require.NoError(t, err)
	require.IsType(t, &dto.GeminiChatRequest{}, result.Value)
	assert.Equal(t, ConverterOpenAIResponsesToOpenAIChat+","+ConverterOpenAIChatToGeminiContent, result.Converter)
	assert.Equal(t, []RequestStep{
		{
			Converter: ConverterOpenAIResponsesToOpenAIChat,
			From:      types.RelayFormatOpenAIResponses,
			To:        types.RelayFormatOpenAI,
		},
		{
			Converter: ConverterOpenAIChatToGeminiContent,
			From:      types.RelayFormatOpenAI,
			To:        types.RelayFormatGemini,
		},
	}, result.Steps)
}

func TestConvertRequestByIDDeduplicatesConversionChain(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses},
	}
	req := &dto.GeneralOpenAIRequest{
		Model: "gpt-test",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequestByID(nil, info, ConverterOpenAIChatToOpenAIResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	require.Len(t, result.Steps, 1)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestByIDExecutesDirectClaudeToResponsesConverter(t *testing.T) {
	info := &convmeta.Values{
		ConversionChain: []types.RelayFormat{types.RelayFormatClaude},
	}
	req := &dto.ClaudeRequest{
		Model: "claude-test",
		Messages: []dto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}

	result, err := ConvertRequestByID(nil, info, requestConverterClaudeToResponses, req)

	require.NoError(t, err)
	require.IsType(t, &dto.OpenAIResponsesRequest{}, result.Value)
	assert.Equal(t, requestConverterClaudeToResponses, result.Converter)
	assert.Equal(t, RequestConverterQualityFair, result.Quality)
	assert.Equal(t, []RequestStep{
		{
			Converter: requestConverterClaudeToResponses,
			From:      types.RelayFormatClaude,
			To:        types.RelayFormatOpenAIResponses,
		},
	}, result.Steps)
	assert.Equal(t, []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses}, info.ConversionChain)
}

func TestConvertRequestRejectsUnsupportedConverterAndNilRequest(t *testing.T) {
	_, err := ConvertRequestByID(nil, &convmeta.Values{}, "missing_converter", &dto.GeneralOpenAIRequest{Model: "gpt-test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")

	_, err = ConvertRequest(nil, &convmeta.Values{}, types.RelayFormatOpenAIResponses, (*dto.GeneralOpenAIRequest)(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request is nil")
}

func TestConvertRequestByIDRejectsWrongSourceFormat(t *testing.T) {
	_, err := ConvertRequestByID(
		nil,
		&convmeta.Values{},
		ConverterOpenAIChatToOpenAIResponses,
		&dto.ClaudeRequest{Model: "claude-test"},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expects openai request")
}

func TestConvertRequestRejectsUnregisteredExplicitPath(t *testing.T) {
	_, err := ConvertRequest(
		nil,
		&convmeta.Values{},
		types.RelayFormatEmbedding,
		&dto.ClaudeRequest{Model: "claude-test"},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "from claude to embedding is not registered")
}

func mustRawMessage(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := kitutil.Marshal(value)
	require.NoError(t, err)
	return raw
}

func inputContentText(t *testing.T, item map[string]any) string {
	t.Helper()
	content, ok := item["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	part, ok := content[0].(map[string]any)
	require.True(t, ok)
	text, ok := part["text"].(string)
	require.True(t, ok)
	return text
}
