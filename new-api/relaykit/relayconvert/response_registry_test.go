package relayconvert

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupBuiltinResponseConverters(t *testing.T) {
	tests := []struct {
		lookupID       string
		id             string
		from           types.RelayFormat
		to             types.RelayFormat
		quality        ResponseConverterQuality
		stepConverters []string
	}{
		{lookupID: ResponseConverterOAIChatToOAIResponses, id: ConverterOpenAIChatToOpenAIResponses, from: types.RelayFormatOpenAI, to: types.RelayFormatOpenAIResponses, quality: ResponseConverterQualityGood},
		{lookupID: ResponseConverterOAIResponsesToOAIChat, id: ConverterOpenAIResponsesToOpenAIChat, from: types.RelayFormatOpenAIResponses, to: types.RelayFormatOpenAI, quality: ResponseConverterQualityGood},
		{lookupID: ResponseConverterOAIChatToClaudeMessages, id: ConverterOpenAIChatToClaudeMessages, from: types.RelayFormatOpenAI, to: types.RelayFormatClaude, quality: ResponseConverterQualityFair},
		{lookupID: ResponseConverterOAIChatToGeminiChat, id: ConverterOpenAIChatToGeminiContent, from: types.RelayFormatOpenAI, to: types.RelayFormatGemini, quality: ResponseConverterQualityFair},
		{lookupID: ResponseConverterClaudeMessagesToOAIChat, id: ConverterClaudeMessagesToOpenAIChat, from: types.RelayFormatClaude, to: types.RelayFormatOpenAI, quality: ResponseConverterQualityFair},
		{lookupID: ResponseConverterGeminiChatToOAIChat, id: ConverterGeminiContentToOpenAIChat, from: types.RelayFormatGemini, to: types.RelayFormatOpenAI, quality: ResponseConverterQualityFair},
		{
			lookupID: responseConverterClaudeToGemini,
			id:       requestConverterClaudeToGemini,
			from:     types.RelayFormatClaude,
			to:       types.RelayFormatGemini,
			quality:  ResponseConverterQualityDiscouraged,
			stepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
		},
		{
			lookupID: responseConverterClaudeToResponses,
			id:       requestConverterClaudeToResponses,
			from:     types.RelayFormatClaude,
			to:       types.RelayFormatOpenAIResponses,
			quality:  ResponseConverterQualityFair,
			stepConverters: []string{
				ConverterClaudeMessagesToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
		},
		{
			lookupID: responseConverterGeminiToClaude,
			id:       requestConverterGeminiToClaude,
			from:     types.RelayFormatGemini,
			to:       types.RelayFormatClaude,
			quality:  ResponseConverterQualityDiscouraged,
			stepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToClaudeMessages,
			},
		},
		{
			lookupID: responseConverterGeminiToResponses,
			id:       requestConverterGeminiToResponses,
			from:     types.RelayFormatGemini,
			to:       types.RelayFormatOpenAIResponses,
			quality:  ResponseConverterQualityFair,
			stepConverters: []string{
				ConverterGeminiContentToOpenAIChat,
				ConverterOpenAIChatToOpenAIResponses,
			},
		},
		{
			lookupID: responseConverterResponsesToClaude,
			id:       requestConverterResponsesToClaude,
			from:     types.RelayFormatOpenAIResponses,
			to:       types.RelayFormatClaude,
			quality:  ResponseConverterQualityFair,
		},
		{
			lookupID: responseConverterResponsesToGemini,
			id:       ConverterOpenAIResponsesToGemini,
			from:     types.RelayFormatOpenAIResponses,
			to:       types.RelayFormatGemini,
			quality:  ResponseConverterQualityFair,
			stepConverters: []string{
				ConverterOpenAIResponsesToOpenAIChat,
				ConverterOpenAIChatToGeminiContent,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.lookupID, func(t *testing.T) {
			spec, ok := LookupResponseConverter(tt.lookupID)
			require.True(t, ok)
			assert.Equal(t, tt.id, spec.ID)
			assert.Equal(t, tt.from, spec.From)
			assert.Equal(t, tt.to, spec.To)
			assert.Equal(t, tt.quality, spec.Quality)
			assert.Equal(t, tt.stepConverters, spec.StepConverters)
			if len(tt.stepConverters) == 0 {
				assert.NotNil(t, spec.Convert)
			} else {
				assert.Nil(t, spec.Convert)
			}
		})
	}

	_, ok := LookupResponseConverter("missing")
	assert.False(t, ok)
}

func TestConvertResponseRejectsNilAndUnsupportedRoute(t *testing.T) {
	_, err := ConvertResponse(nil, nil, types.RelayFormatOpenAI, (*dto.OpenAITextResponse)(nil))
	require.Error(t, err)

	_, err = ConvertResponse(nil, nil, types.RelayFormatEmbedding, &dto.OpenAITextResponse{})
	require.Error(t, err)
}

func TestConvertResponseDirectConverters(t *testing.T) {
	chat := textRegistryChatResponse()
	info := &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}

	toResponses, err := ConvertResponse(nil, info, types.RelayFormatOpenAIResponses, chat)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIChatToOpenAIResponses, toResponses.Converter)
	assert.Equal(t, ResponseConverterQualityGood, toResponses.Quality)
	assert.Equal(t, types.RelayFormatOpenAI, toResponses.From)
	assert.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), toResponses.To)
	assert.Equal(t, []ResponseStep{{Converter: ConverterOpenAIChatToOpenAIResponses, From: types.RelayFormatOpenAI, To: types.RelayFormatOpenAIResponses}}, toResponses.Steps)
	require.IsType(t, &dto.OpenAIResponsesResponse{}, toResponses.Value)
	assert.Equal(t, 9, toResponses.Usage.TotalTokens)
	require.NotNil(t, toResponses.Usage.BillingUsage)
	require.NotNil(t, toResponses.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIChat, toResponses.Usage.BillingUsage.Source)
	assert.Equal(t, 4, toResponses.Usage.BillingUsage.OpenAIUsage.PromptTokens)

	responses := &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		CreatedAt: 123,
		Model:     "gpt-test",
		Status:    []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "hello"},
				},
			},
		},
		Usage: &dto.Usage{InputTokens: 4, OutputTokens: 6, TotalTokens: 10},
	}
	toChat, err := ConvertResponse(nil, info, types.RelayFormatOpenAI, responses)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIResponsesToOpenAIChat, toChat.Converter)
	assert.Equal(t, ResponseConverterQualityGood, toChat.Quality)
	require.IsType(t, &dto.OpenAITextResponse{}, toChat.Value)
	assert.Equal(t, 10, toChat.Usage.TotalTokens)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIResponses, toChat.Usage.BillingUsage.Source)
	assert.Equal(t, 4, toChat.Usage.BillingUsage.OpenAIUsage.InputTokens)

	toClaude, err := ConvertResponse(nil, info, types.RelayFormatClaude, chat)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIChatToClaudeMessages, toClaude.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toClaude.Quality)
	require.IsType(t, &dto.ClaudeResponse{}, toClaude.Value)
	assert.Equal(t, 9, toClaude.Usage.TotalTokens)
	require.NotNil(t, toClaude.Usage.BillingUsage)
	require.NotNil(t, toClaude.Usage.BillingUsage.OpenAIUsage)
	claudeValue := toClaude.Value.(*dto.ClaudeResponse)
	require.NotNil(t, claudeValue.Usage)
	require.NotNil(t, claudeValue.Usage.BillingUsage)
	require.NotNil(t, claudeValue.Usage.BillingUsage.OpenAIUsage)

	toGemini, err := ConvertResponse(nil, info, types.RelayFormatGemini, chat)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIChatToGeminiContent, toGemini.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toGemini.Quality)
	require.IsType(t, &dto.GeminiChatResponse{}, toGemini.Value)
	assert.Equal(t, 9, toGemini.Usage.TotalTokens)
	require.NotNil(t, toGemini.Usage.BillingUsage)
	require.NotNil(t, toGemini.Usage.BillingUsage.OpenAIUsage)
	geminiValue := toGemini.Value.(*dto.GeminiChatResponse)
	require.NotNil(t, geminiValue.UsageMetadata.BillingUsage)
	require.NotNil(t, geminiValue.UsageMetadata.BillingUsage.OpenAIUsage)
}

func TestConvertResponseDirectAndMultiHopConverters(t *testing.T) {
	responses := textRegistryResponsesResponse()

	toClaude, err := ConvertResponse(nil, &convmeta.Values{}, types.RelayFormatClaude, responses)
	require.NoError(t, err)
	assert.Equal(t, requestConverterResponsesToClaude, toClaude.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toClaude.Quality)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToClaudeMessages, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatClaude},
	}, toClaude.Steps)
	require.IsType(t, &dto.ClaudeResponse{}, toClaude.Value)
	claudeValue := toClaude.Value.(*dto.ClaudeResponse)
	require.Len(t, claudeValue.Content, 2)
	assert.Equal(t, "text", claudeValue.Content[0].Type)
	assert.Equal(t, "tool_use", claudeValue.Content[1].Type)
	assert.Equal(t, "lookup", claudeValue.Content[1].Name)
	assert.Equal(t, map[string]any{"q": "x"}, claudeValue.Content[1].Input)
	assert.Equal(t, 11, toClaude.Usage.TotalTokens)

	toGemini, err := ConvertResponse(nil, &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}, types.RelayFormatGemini, responses)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIResponsesToGemini, toGemini.Converter)
	assert.Equal(t, ResponseConverterQualityFair, toGemini.Quality)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToOpenAIChat, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatOpenAI},
		{Converter: ConverterOpenAIChatToGeminiContent, From: types.RelayFormatOpenAI, To: types.RelayFormatGemini},
	}, toGemini.Steps)
	require.IsType(t, &dto.GeminiChatResponse{}, toGemini.Value)
	geminiValue := toGemini.Value.(*dto.GeminiChatResponse)
	require.Len(t, geminiValue.Candidates, 1)
	require.Len(t, geminiValue.Candidates[0].Content.Parts, 2)
	assert.Equal(t, "hello", geminiValue.Candidates[0].Content.Parts[0].Text)
	require.NotNil(t, geminiValue.Candidates[0].Content.Parts[1].FunctionCall)
	assert.Equal(t, "lookup", geminiValue.Candidates[0].Content.Parts[1].FunctionCall.FunctionName)
	assert.Equal(t, map[string]any{"q": "x"}, geminiValue.Candidates[0].Content.Parts[1].FunctionCall.Arguments)
	assert.Equal(t, 11, toGemini.Usage.TotalTokens)
}

func TestConvertResponsePreservesInterleavedResponsesBlocksForClaude(t *testing.T) {
	responses := &dto.OpenAIResponsesResponse{
		ID:     "resp_1",
		Model:  "gpt-test",
		Status: []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{Type: "reasoning", Summary: []dto.ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "**Planning file inspection**"}}},
			{Type: "message", Role: "assistant", Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "I’ll inspect the starter repository."}}},
			{Type: "reasoning", Summary: []dto.ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "**Clarifying environment task requirements**"}}},
			{Type: "message", Role: "assistant", Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "What would you like me to build?"}}},
		},
	}

	result, err := ConvertResponse(nil, nil, types.RelayFormatClaude, responses)
	require.NoError(t, err)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToClaudeMessages, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatClaude},
	}, result.Steps)
	claudeResponse := result.Value.(*dto.ClaudeResponse)
	require.Len(t, claudeResponse.Content, 4)
	assert.Equal(t, []string{"thinking", "text", "thinking", "text"}, []string{
		claudeResponse.Content[0].Type,
		claudeResponse.Content[1].Type,
		claudeResponse.Content[2].Type,
		claudeResponse.Content[3].Type,
	})
	require.NotNil(t, claudeResponse.Content[0].Thinking)
	require.NotNil(t, claudeResponse.Content[2].Thinking)
	assert.Equal(t, "**Planning file inspection**", *claudeResponse.Content[0].Thinking)
	assert.Equal(t, "I’ll inspect the starter repository.", claudeResponse.Content[1].GetText())
	assert.Equal(t, "**Clarifying environment task requirements**", *claudeResponse.Content[2].Thinking)
	assert.Equal(t, "What would you like me to build?", claudeResponse.Content[3].GetText())
}

func TestConvertResponseByIDExecutesMultiHopAndChecksSource(t *testing.T) {
	responses := textRegistryResponsesResponse()

	result, err := ConvertResponseByID(nil, nil, responseConverterResponsesToGemini, responses)
	require.NoError(t, err)
	assert.Equal(t, ConverterOpenAIResponsesToGemini, result.Converter)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToOpenAIChat, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatOpenAI},
		{Converter: ConverterOpenAIChatToGeminiContent, From: types.RelayFormatOpenAI, To: types.RelayFormatGemini},
	}, result.Steps)

	_, err = ConvertResponseByID(nil, nil, responseConverterResponsesToGemini, textRegistryChatResponse())
	require.Error(t, err)
}

func TestConvertResponseProviderToOAIChatUsage(t *testing.T) {
	claude := &dto.ClaudeResponse{
		Id:         "msg_1",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-test",
		StopReason: "end_turn",
		Content: []dto.ClaudeMediaMessage{
			{Type: "tool_use", Id: "toolu_1", Name: "lookup", Input: map[string]any{"q": "x"}},
		},
		Usage: &dto.ClaudeUsage{
			InputTokens:              10,
			CacheReadInputTokens:     3,
			CacheCreationInputTokens: 4,
			OutputTokens:             5,
			CacheCreation: &dto.ClaudeCacheCreationUsage{
				Ephemeral5mInputTokens: 1,
				Ephemeral1hInputTokens: 3,
			},
		},
	}
	toChat, err := ConvertResponse(nil, nil, types.RelayFormatOpenAI, claude)
	require.NoError(t, err)
	assert.Equal(t, ConverterClaudeMessagesToOpenAIChat, toChat.Converter)
	require.IsType(t, &dto.OpenAITextResponse{}, toChat.Value)
	assert.Equal(t, 17, toChat.Usage.PromptTokens)
	assert.Equal(t, 5, toChat.Usage.CompletionTokens)
	assert.Equal(t, 22, toChat.Usage.TotalTokens)
	assert.Equal(t, 3, toChat.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 4, toChat.Usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 4, toChat.Usage.PromptTokensDetails.CacheWriteTokens)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.ClaudeUsage)
	assert.Equal(t, dto.BillingUsageSourceClaudeMessages, toChat.Usage.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticAnthropic, toChat.Usage.BillingUsage.Semantic)
	assert.Equal(t, 10, toChat.Usage.BillingUsage.ClaudeUsage.InputTokens)
	assert.Equal(t, 3, toChat.Usage.BillingUsage.ClaudeUsage.CacheReadInputTokens)
	assert.Equal(t, 4, toChat.Usage.BillingUsage.ClaudeUsage.CacheCreationInputTokens)
	assert.Equal(t, 5, toChat.Usage.BillingUsage.ClaudeUsage.OutputTokens)
	chatValue := toChat.Value.(*dto.OpenAITextResponse)
	require.Len(t, chatValue.Choices, 1)
	require.Len(t, chatValue.Choices[0].Message.ParseToolCalls(), 1)
	assert.JSONEq(t, `{"q":"x"}`, chatValue.Choices[0].Message.ParseToolCalls()[0].Function.Arguments)

	gemini := &dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{
			{
				Content: dto.GeminiChatContent{
					Parts: []dto.GeminiPart{
						{Text: "hello"},
						{FunctionCall: &dto.FunctionCall{FunctionName: "lookup", Arguments: map[string]any{"q": "x"}}},
					},
				},
			},
		},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:        7,
			ToolUsePromptTokenCount: 2,
			CandidatesTokenCount:    5,
			ThoughtsTokenCount:      3,
			TotalTokenCount:         17,
			CachedContentTokenCount: 4,
			PromptTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 5},
				{Modality: "IMAGE", TokenCount: 1},
			},
			ToolUsePromptTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "AUDIO", TokenCount: 3},
			},
			CandidatesTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: 4},
				{Modality: "IMAGE", TokenCount: 1},
			},
		},
	}
	toChat, err = ConvertResponse(nil, &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}, types.RelayFormatOpenAI, gemini)
	require.NoError(t, err)
	assert.Equal(t, ConverterGeminiContentToOpenAIChat, toChat.Converter)
	require.IsType(t, &dto.OpenAITextResponse{}, toChat.Value)
	assert.Equal(t, 9, toChat.Usage.PromptTokens)
	assert.Equal(t, 8, toChat.Usage.CompletionTokens)
	assert.Equal(t, 17, toChat.Usage.TotalTokens)
	assert.Equal(t, 3, toChat.Usage.CompletionTokenDetails.ReasoningTokens)
	assert.Equal(t, 4, toChat.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 5, toChat.Usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 3, toChat.Usage.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 1, toChat.Usage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 4, toChat.Usage.CompletionTokenDetails.TextTokens)
	assert.Equal(t, 1, toChat.Usage.CompletionTokenDetails.ImageTokens)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, dto.BillingUsageSourceGeminiChat, toChat.Usage.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticGemini, toChat.Usage.BillingUsage.Semantic)
	assert.Equal(t, 7, toChat.Usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	assert.Equal(t, 2, toChat.Usage.BillingUsage.GeminiUsageMetadata.ToolUsePromptTokenCount)
	assert.Equal(t, 17, toChat.Usage.BillingUsage.GeminiUsageMetadata.TotalTokenCount)
}

func TestConvertResponsePreservesBillingUsageAcrossChatResponsesBridge(t *testing.T) {
	chat := textRegistryChatResponse()
	chat.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
		InputTokens:              10,
		CacheReadInputTokens:     3,
		CacheCreationInputTokens: 4,
		OutputTokens:             5,
	})

	toResponses, err := ConvertResponse(nil, nil, types.RelayFormatOpenAIResponses, chat)
	require.NoError(t, err)
	require.NotNil(t, toResponses.Usage.BillingUsage)
	require.NotNil(t, toResponses.Usage.BillingUsage.ClaudeUsage)
	assert.Equal(t, 10, toResponses.Usage.BillingUsage.ClaudeUsage.InputTokens)

	responsesValue := toResponses.Value.(*dto.OpenAIResponsesResponse)
	toChat, err := ConvertResponse(nil, nil, types.RelayFormatOpenAI, responsesValue)
	require.NoError(t, err)
	require.NotNil(t, toChat.Usage.BillingUsage)
	require.NotNil(t, toChat.Usage.BillingUsage.ClaudeUsage)
	assert.Equal(t, 4, toChat.Usage.BillingUsage.ClaudeUsage.CacheCreationInputTokens)
}

func TestConvertResponseUsesBillingUsageWhenRestoringNativeTargets(t *testing.T) {
	chat := textRegistryChatResponse()
	chat.Usage.BillingUsage = dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
		InputTokens:              10,
		CacheReadInputTokens:     3,
		CacheCreationInputTokens: 4,
		OutputTokens:             5,
	})

	toClaude, err := ConvertResponse(nil, nil, types.RelayFormatClaude, chat)
	require.NoError(t, err)
	claudeValue := toClaude.Value.(*dto.ClaudeResponse)
	require.NotNil(t, claudeValue.Usage)
	assert.Equal(t, 10, claudeValue.Usage.InputTokens)
	assert.Equal(t, 3, claudeValue.Usage.CacheReadInputTokens)
	assert.Equal(t, 4, claudeValue.Usage.CacheCreationInputTokens)
	assert.Equal(t, 5, claudeValue.Usage.OutputTokens)

	chat.Usage.BillingUsage = dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
		PromptTokenCount:        7,
		ToolUsePromptTokenCount: 2,
		CandidatesTokenCount:    5,
		ThoughtsTokenCount:      3,
		TotalTokenCount:         17,
	})

	toGemini, err := ConvertResponse(nil, nil, types.RelayFormatGemini, chat)
	require.NoError(t, err)
	geminiValue := toGemini.Value.(*dto.GeminiChatResponse)
	assert.Equal(t, 7, geminiValue.UsageMetadata.PromptTokenCount)
	assert.Equal(t, 2, geminiValue.UsageMetadata.ToolUsePromptTokenCount)
	assert.Equal(t, 5, geminiValue.UsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 3, geminiValue.UsageMetadata.ThoughtsTokenCount)
	assert.Equal(t, 17, geminiValue.UsageMetadata.TotalTokenCount)
}

func TestConvertStreamResponseDirectConverters(t *testing.T) {
	info := &convmeta.Values{
		ClaudeConvertInfo: &convmeta.ClaudeConvertInfo{
			LastMessagesType: convmeta.LastMessageTypeNone,
		},
	}
	info.SendResponseCount = 1
	finishReason := "stop"
	result, err := ConvertStreamResponse(nil, info, types.RelayFormatClaude, &dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				FinishReason: &finishReason,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: respPtr("hello"),
				},
			},
		},
		Usage: &dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	})
	require.NoError(t, err)
	assert.True(t, result.Stream)
	assert.Equal(t, ConverterOpenAIChatToClaudeMessages, result.Converter)
	require.IsType(t, []*dto.ClaudeResponse{}, result.Value)
	assert.Equal(t, 5, result.Usage.TotalTokens)

	result, err = ConvertStreamResponse(nil, &convmeta.Values{ChannelMetaAttached: true, UpstreamModelName: "gemini-test"}, types.RelayFormatOpenAI, &dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{Content: dto.GeminiChatContent{Parts: []dto.GeminiPart{{Text: "hello"}}}}},
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     1,
			CandidatesTokenCount: 2,
			TotalTokenCount:      3,
		},
	})
	require.NoError(t, err)
	assert.True(t, result.Stream)
	assert.Equal(t, ConverterGeminiContentToOpenAIChat, result.Converter)
	require.IsType(t, &dto.ChatCompletionsStreamResponse{}, result.Value)
	assert.Equal(t, 3, result.Usage.TotalTokens)
}

func TestConvertStreamResponseStatefulDirectConverters(t *testing.T) {
	chatState, err := NewResponseStreamState(types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, ResponseStreamOptions{
		ID:    "resp_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)
	chatResults, err := ConvertStreamResponseChunk(nil, nil, chatState, &dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: respPtr("hello")}},
		},
		Usage: &dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	})
	require.NoError(t, err)
	require.NotEmpty(t, chatResults)
	assert.Equal(t, ConverterOpenAIChatToOpenAIResponses, chatResults[0].Converter)
	assert.Equal(t, []ResponseStep{{Converter: ConverterOpenAIChatToOpenAIResponses, From: types.RelayFormatOpenAI, To: types.RelayFormatOpenAIResponses}}, chatResults[0].Steps)
	assert.Equal(t, 5, chatState.Usage().TotalTokens)

	finalResults, err := FinalizeStreamResponse(nil, nil, chatState)
	require.NoError(t, err)
	require.NotEmpty(t, finalResults)
	lastEvent, ok := finalResults[len(finalResults)-1].Value.(ChatToResponsesStreamEvent)
	require.True(t, ok)
	assert.Equal(t, "response.completed", lastEvent.Type)

	responsesState, err := NewResponseStreamState(types.RelayFormatOpenAIResponses, types.RelayFormatOpenAI, ResponseStreamOptions{
		ID:    "chatcmpl_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)
	responsesResults, err := ConvertStreamResponseChunk(nil, nil, responsesState, &dto.ResponsesStreamResponse{
		Type:  "response.output_text.delta",
		Delta: "hello",
	})
	require.NoError(t, err)
	require.NotEmpty(t, responsesResults)
	assert.Equal(t, ConverterOpenAIResponsesToOpenAIChat, responsesResults[0].Converter)
	assert.Equal(t, []ResponseStep{{Converter: ConverterOpenAIResponsesToOpenAIChat, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatOpenAI}}, responsesResults[0].Steps)
	require.IsType(t, dto.ChatCompletionsStreamResponse{}, responsesResults[len(responsesResults)-1].Value)
}

func TestConvertStreamResponseStatefulDirectResponsesToClaude(t *testing.T) {
	info := &convmeta.Values{
		ClaudeConvertInfo: &convmeta.ClaudeConvertInfo{
			LastMessagesType: convmeta.LastMessageTypeNone,
		},
	}
	state, err := NewResponseStreamState(types.RelayFormatOpenAIResponses, types.RelayFormatClaude, ResponseStreamOptions{
		ID:    "chatcmpl_1",
		Model: "gpt-test",
	})
	require.NoError(t, err)

	results, err := ConvertStreamResponseChunk(nil, info, state, &dto.ResponsesStreamResponse{
		Type:  "response.output_text.delta",
		Delta: "hello",
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, requestConverterResponsesToClaude, results[0].Converter)
	assert.Equal(t, []ResponseStep{
		{Converter: ConverterOpenAIResponsesToClaudeMessages, From: types.RelayFormatOpenAIResponses, To: types.RelayFormatClaude},
	}, results[0].Steps)

	var sawTextDelta bool
	for _, result := range results {
		claudeResponse, ok := result.Value.(*dto.ClaudeResponse)
		if !ok || claudeResponse == nil {
			continue
		}
		if claudeResponse.Type == "content_block_delta" && claudeResponse.Delta != nil && claudeResponse.Delta.Text != nil && *claudeResponse.Delta.Text == "hello" {
			sawTextDelta = true
		}
	}
	assert.True(t, sawTextDelta)

	state.SetUsage(&dto.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	_, err = FinalizeStreamResponse(nil, info, state)
	require.NoError(t, err)
	assert.Equal(t, 5, state.Usage().TotalTokens)
}

func TestResponseUsageMatrixChatAndResponsesDetails(t *testing.T) {
	chat := textRegistryChatResponse()
	chat.Usage = dto.Usage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         3,
			CachedCreationTokens: 2,
			CacheWriteTokens:     6,
			TextTokens:           4,
			AudioTokens:          1,
			ImageTokens:          5,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			ReasoningTokens: 2,
			TextTokens:      2,
			AudioTokens:     1,
			ImageTokens:     2,
		},
	}
	result, err := ConvertResponse(nil, nil, types.RelayFormatOpenAIResponses, chat)
	require.NoError(t, err)
	assert.Equal(t, 10, result.Usage.InputTokens)
	assert.Equal(t, 5, result.Usage.OutputTokens)
	assert.Equal(t, 20, result.Usage.TotalTokens)
	require.NotNil(t, result.Usage.InputTokensDetails)
	assert.Equal(t, 3, result.Usage.InputTokensDetails.CachedTokens)
	assert.Equal(t, 2, result.Usage.InputTokensDetails.CachedCreationTokens)
	assert.Equal(t, 6, result.Usage.InputTokensDetails.CacheWriteTokens)
	assert.Equal(t, 4, result.Usage.InputTokensDetails.TextTokens)
	assert.Equal(t, 1, result.Usage.InputTokensDetails.AudioTokens)
	assert.Equal(t, 5, result.Usage.InputTokensDetails.ImageTokens)
	assert.Equal(t, 2, result.Usage.CompletionTokenDetails.ReasoningTokens)
	assert.Equal(t, 2, result.Usage.CompletionTokenDetails.TextTokens)
	assert.Equal(t, 1, result.Usage.CompletionTokenDetails.AudioTokens)
	assert.Equal(t, 2, result.Usage.CompletionTokenDetails.ImageTokens)

	responses := &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		Status:    []byte(`"completed"`),
		Model:     "gpt-test",
		Output:    []dto.ResponsesOutput{},
		CreatedAt: 123,
		Usage: &dto.Usage{
			InputTokens:  12,
			OutputTokens: 8,
			TotalTokens:  21,
			InputTokensDetails: &dto.InputTokenDetails{
				CachedTokens:         4,
				CachedCreationTokens: 1,
				CacheWriteTokens:     7,
				TextTokens:           5,
				AudioTokens:          2,
				ImageTokens:          1,
			},
			CompletionTokenDetails: dto.OutputTokenDetails{
				ReasoningTokens: 3,
				TextTokens:      4,
				AudioTokens:     1,
				ImageTokens:     3,
			},
		},
	}
	result, err = ConvertResponse(nil, nil, types.RelayFormatOpenAI, responses)
	require.NoError(t, err)
	assert.Equal(t, 12, result.Usage.PromptTokens)
	assert.Equal(t, 8, result.Usage.CompletionTokens)
	assert.Equal(t, 21, result.Usage.TotalTokens)
	assert.Equal(t, 4, result.Usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 1, result.Usage.PromptTokensDetails.CachedCreationTokens)
	assert.Equal(t, 7, result.Usage.PromptTokensDetails.CacheWriteTokens)
	assert.Equal(t, 5, result.Usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 2, result.Usage.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 1, result.Usage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 3, result.Usage.CompletionTokenDetails.ReasoningTokens)
	assert.Equal(t, 4, result.Usage.CompletionTokenDetails.TextTokens)
	assert.Equal(t, 1, result.Usage.CompletionTokenDetails.AudioTokens)
	assert.Equal(t, 3, result.Usage.CompletionTokenDetails.ImageTokens)
}

func textRegistryChatResponse() *dto.OpenAITextResponse {
	msg := dto.Message{
		Role:    "assistant",
		Content: "hello",
	}
	msg.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   "call_1",
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "lookup",
				Arguments: `{"q":"x"}`,
			},
		},
	})
	return &dto.OpenAITextResponse{
		Id:      "chatcmpl_1",
		Model:   "gpt-test",
		Created: 123,
		Choices: []dto.OpenAITextResponseChoice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: "tool_calls",
			},
		},
		Usage: dto.Usage{PromptTokens: 4, CompletionTokens: 5, TotalTokens: 9},
	}
}

func textRegistryResponsesResponse() *dto.OpenAIResponsesResponse {
	return &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		CreatedAt: 123,
		Model:     "gpt-test",
		Status:    []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type: "message",
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "hello"},
				},
			},
			{
				Type:      "function_call",
				ID:        "call_1",
				CallId:    "call_1",
				Name:      "lookup",
				Arguments: []byte(`{"q":"x"}`),
			},
		},
		Usage: &dto.Usage{InputTokens: 4, OutputTokens: 7, TotalTokens: 11},
	}
}

func respPtr[T any](value T) *T {
	return &value
}
