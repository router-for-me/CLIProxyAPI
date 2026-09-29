package oaichat

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseOpenAI2ClaudeToolUseInputIsObject(t *testing.T) {
	tests := []struct {
		name string
		args string
		want map[string]any
	}{
		{name: "object", args: `{"q":"x"}`, want: map[string]any{"q": "x"}},
		{name: "empty", args: "", want: map[string]any{}},
		{name: "invalid", args: "{", want: map[string]any{}},
		{name: "null", args: "null", want: map[string]any{}},
		{name: "array", args: `["x"]`, want: map[string]any{}},
		{name: "string", args: `"x"`, want: map[string]any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := dto.Message{Role: "assistant"}
			msg.SetToolCalls([]dto.ToolCallRequest{
				{
					ID:   "call_1",
					Type: "function",
					Function: dto.FunctionRequest{
						Name:      "lookup",
						Arguments: tt.args,
					},
				},
			})

			resp := ResponseOpenAI2Claude(&dto.OpenAITextResponse{
				Id:    "chatcmpl_1",
				Model: "gpt-test",
				Choices: []dto.OpenAITextResponseChoice{
					{Message: msg, FinishReason: "tool_calls"},
				},
			}, nil)

			require.Len(t, resp.Content, 1)
			assert.Equal(t, "tool_use", resp.Content[0].Type)
			assert.Equal(t, tt.want, resp.Content[0].Input)
		})
	}
}

func TestResponseOpenAI2ClaudeUsageCarriesOpenAIBillingUsage(t *testing.T) {
	resp := ResponseOpenAI2Claude(&dto.OpenAITextResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.OpenAITextResponseChoice{
			{Message: dto.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"},
		},
		Usage: dto.Usage{
			PromptTokens:     11,
			CompletionTokens: 5,
			TotalTokens:      16,
		},
	}, nil)

	require.NotNil(t, resp.Usage)
	assert.Equal(t, 11, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	require.NotNil(t, resp.Usage.BillingUsage)
	require.NotNil(t, resp.Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIChat, resp.Usage.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticOpenAI, resp.Usage.BillingUsage.Semantic)
	assert.Equal(t, 11, resp.Usage.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Equal(t, 16, resp.Usage.BillingUsage.OpenAIUsage.TotalTokens)
	assert.Nil(t, resp.Usage.BillingUsage.OpenAIUsage.BillingUsage)
}

func TestResponseOpenAI2ClaudePreservesReasoningBeforeText(t *testing.T) {
	message := dto.Message{Role: "assistant", Content: "final answer"}
	message.ReasoningContent = ptr("considering the request")
	resp := ResponseOpenAI2Claude(&dto.OpenAITextResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.OpenAITextResponseChoice{
			{Message: message, FinishReason: "stop"},
		},
	}, nil)

	require.Len(t, resp.Content, 2)
	assert.Equal(t, "thinking", resp.Content[0].Type)
	require.NotNil(t, resp.Content[0].Thinking)
	assert.Equal(t, "considering the request", *resp.Content[0].Thinking)
	assert.Equal(t, "text", resp.Content[1].Type)
	assert.Equal(t, "final answer", resp.Content[1].GetText())
}

func TestBuildClaudeUsageFromOpenAICacheWriteUsage(t *testing.T) {
	usage := buildClaudeUsageFromOpenAIUsage(&dto.Usage{
		PromptTokens:     3619,
		CompletionTokens: 36,
		TotalTokens:      3655,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:     2921,
			CacheWriteTokens: 3616,
		},
	})

	require.NotNil(t, usage)
	// Claude semantics reports input_tokens excluding cache read/write; the
	// overlapping unadjusted prefixes drive the remainder negative, clamp to 0.
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 2921, usage.CacheReadInputTokens)
	assert.Equal(t, 3616, usage.CacheCreationInputTokens)
	assert.Equal(t, 36, usage.OutputTokens)
	require.NotNil(t, usage.BillingUsage)
	require.NotNil(t, usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSemanticOpenAI, usage.BillingUsage.Semantic)
	assert.Equal(t, 3616, usage.BillingUsage.OpenAIUsage.PromptTokensDetails.CacheWriteTokens)
}

func TestStreamResponseOpenAI2ClaudeClosesTextThinkingAndToolBlocks(t *testing.T) {
	info := &convmeta.Values{
		ClaudeConvertInfo: &convmeta.ClaudeConvertInfo{
			LastMessagesType: convmeta.LastMessageTypeNone,
		},
	}

	info.SendResponseCount = 1
	textResponses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: ptr("hello"),
				},
			},
		},
	}, info)
	require.Len(t, textResponses, 3)
	assert.Equal(t, "message_start", textResponses[0].Type)
	assert.Equal(t, "content_block_start", textResponses[1].Type)
	assert.Equal(t, 0, textResponses[1].GetIndex())
	assert.Equal(t, "content_block_delta", textResponses[2].Type)

	info.SendResponseCount = 2
	thinkingResponses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ReasoningContent: ptr("thinking"),
				},
			},
		},
	}, info)
	require.Len(t, thinkingResponses, 3)
	assert.Equal(t, "content_block_stop", thinkingResponses[0].Type)
	assert.Equal(t, 0, thinkingResponses[0].GetIndex())
	assert.Equal(t, "content_block_start", thinkingResponses[1].Type)
	assert.Equal(t, 1, thinkingResponses[1].GetIndex())
	assert.Equal(t, "thinking", thinkingResponses[1].ContentBlock.Type)
	assert.Equal(t, "content_block_delta", thinkingResponses[2].Type)

	info.SendResponseCount = 3
	toolResponses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ToolCalls: []dto.ToolCallResponse{
						{
							Index: ptr(0),
							ID:    "call_1",
							Type:  "function",
							Function: dto.FunctionResponse{
								Name:      "lookup",
								Arguments: `{"q":"x"}`,
							},
						},
					},
				},
			},
		},
	}, info)
	require.Len(t, toolResponses, 3)
	assert.Equal(t, "content_block_stop", toolResponses[0].Type)
	assert.Equal(t, 1, toolResponses[0].GetIndex())
	assert.Equal(t, "content_block_start", toolResponses[1].Type)
	assert.Equal(t, 2, toolResponses[1].GetIndex())
	assert.Equal(t, "tool_use", toolResponses[1].ContentBlock.Type)
	assert.Equal(t, "content_block_delta", toolResponses[2].Type)

	info.SendResponseCount = 4
	finishResponses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{FinishReason: ptr("tool_calls")},
		},
		Usage: &dto.Usage{
			PromptTokens:     7,
			CompletionTokens: 3,
			TotalTokens:      10,
		},
	}, info)
	require.Len(t, finishResponses, 3)
	assert.Equal(t, "content_block_stop", finishResponses[0].Type)
	assert.Equal(t, 2, finishResponses[0].GetIndex())
	assert.Equal(t, "message_delta", finishResponses[1].Type)
	assert.Equal(t, "tool_use", *finishResponses[1].Delta.StopReason)
	require.NotNil(t, finishResponses[1].Usage)
	require.NotNil(t, finishResponses[1].Usage.BillingUsage)
	require.NotNil(t, finishResponses[1].Usage.BillingUsage.OpenAIUsage)
	assert.Equal(t, 7, finishResponses[1].Usage.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, 3, finishResponses[1].Usage.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Equal(t, "message_stop", finishResponses[2].Type)
}

func TestStreamResponseOpenAI2ClaudeFirstFrameUsesUpstreamUsageWhenPresent(t *testing.T) {
	info := &convmeta.Values{
		EstimatePromptTokens: 32,
		SendResponseCount:    1,
		ClaudeConvertInfo:    &convmeta.ClaudeConvertInfo{LastMessagesType: convmeta.LastMessageTypeNone},
	}

	responses := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: ptr("hello")},
		}},
		Usage: &dto.Usage{PromptTokens: 29, CompletionTokens: 0, TotalTokens: 29},
	}, info)
	require.NotEmpty(t, responses)
	require.Equal(t, "message_start", responses[0].Type)
	require.NotNil(t, responses[0].Message)
	require.NotNil(t, responses[0].Message.Usage)
	assert.Equal(t, 29, responses[0].Message.Usage.InputTokens)
}

func TestStreamResponseOpenAI2ClaudeMessageDeltaCorrectsEstimatedFirstFrame(t *testing.T) {
	info := &convmeta.Values{
		EstimatePromptTokens: 32,
		SendResponseCount:    1,
		ClaudeConvertInfo:    &convmeta.ClaudeConvertInfo{LastMessagesType: convmeta.LastMessageTypeNone},
	}

	first := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: ptr("hello")},
		}},
	}, info)
	require.NotEmpty(t, first)
	require.Equal(t, "message_start", first[0].Type)
	require.NotNil(t, first[0].Message.Usage)
	assert.Equal(t, 32, first[0].Message.Usage.InputTokens)

	info.SendResponseCount = 2
	finish := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			FinishReason: ptr("stop"),
		}},
		Usage: &dto.Usage{PromptTokens: 29, CompletionTokens: 4, TotalTokens: 33},
	}, info)
	var delta *dto.ClaudeResponse
	for _, resp := range finish {
		if resp.Type == "message_delta" {
			delta = resp
			break
		}
	}
	require.NotNil(t, delta)
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 29, delta.Usage.InputTokens)
	assert.Equal(t, 4, delta.Usage.OutputTokens)
}

func TestStreamResponseOpenAI2ClaudeMessageDeltaDoesNotZeroFirstFrameCache(t *testing.T) {
	info := &convmeta.Values{
		EstimatePromptTokens: 8,
		SendResponseCount:    1,
		ClaudeConvertInfo:    &convmeta.ClaudeConvertInfo{LastMessagesType: convmeta.LastMessageTypeNone},
	}

	first := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: ptr("hello")},
		}},
		Usage: &dto.Usage{
			PromptTokens:     40,
			CompletionTokens: 0,
			TotalTokens:      40,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         20,
				CachedCreationTokens: 10,
			},
		},
	}, info)
	require.Equal(t, "message_start", first[0].Type)
	require.NotNil(t, first[0].Message.Usage)
	assert.Equal(t, 20, first[0].Message.Usage.CacheReadInputTokens)
	assert.Equal(t, 10, first[0].Message.Usage.CacheCreationInputTokens)

	info.SendResponseCount = 2
	finish := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			FinishReason: ptr("stop"),
		}},
		Usage: &dto.Usage{PromptTokens: 29, CompletionTokens: 4, TotalTokens: 33},
	}, info)
	var delta *dto.ClaudeResponse
	for _, resp := range finish {
		if resp.Type == "message_delta" {
			delta = resp
			break
		}
	}
	require.NotNil(t, delta)
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 29, delta.Usage.InputTokens)
	assert.Equal(t, 20, delta.Usage.CacheReadInputTokens)
	assert.Equal(t, 10, delta.Usage.CacheCreationInputTokens)
}

func TestStreamResponseOpenAI2ClaudeGeminiBillingUsageOnStartAndDelta(t *testing.T) {
	info := &convmeta.Values{
		EstimatePromptTokens: 4994,
		SendResponseCount:    1,
		ClaudeConvertInfo:    &convmeta.ClaudeConvertInfo{LastMessagesType: convmeta.LastMessageTypeNone},
	}

	firstUsage := &dto.Usage{
		PromptTokens:     3868,
		CompletionTokens: 0,
		TotalTokens:      3868,
		BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
			PromptTokenCount: 3868,
			TotalTokenCount:  3868,
		}),
	}
	first := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: ptr("hello")},
		}},
		Usage: firstUsage,
	}, info)
	require.NotEmpty(t, first)
	require.Equal(t, "message_start", first[0].Type)
	require.NotNil(t, first[0].Message)
	require.NotNil(t, first[0].Message.Usage)
	assert.Equal(t, 3868, first[0].Message.Usage.InputTokens)
	require.NotNil(t, first[0].Message.Usage.BillingUsage)
	assert.Equal(t, dto.BillingUsageSourceGeminiChat, first[0].Message.Usage.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticGemini, first[0].Message.Usage.BillingUsage.Semantic)
	require.NotNil(t, first[0].Message.Usage.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, 3868, first[0].Message.Usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)

	info.SendResponseCount = 2
	finish := StreamResponseOpenAI2Claude(&dto.ChatCompletionsStreamResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			FinishReason: ptr("stop"),
		}},
		Usage: &dto.Usage{
			PromptTokens:     3868,
			CompletionTokens: 12,
			TotalTokens:      3880,
			BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
				PromptTokenCount:     3868,
				CandidatesTokenCount: 12,
				TotalTokenCount:      3880,
			}),
		},
	}, info)
	var delta *dto.ClaudeResponse
	for _, resp := range finish {
		if resp.Type == "message_delta" {
			delta = resp
			break
		}
	}
	require.NotNil(t, delta)
	require.NotNil(t, delta.Usage)
	assert.Equal(t, 3868, delta.Usage.InputTokens)
	assert.Equal(t, 12, delta.Usage.OutputTokens)
	require.NotNil(t, delta.Usage.BillingUsage)
	assert.Equal(t, dto.BillingUsageSourceGeminiChat, delta.Usage.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticGemini, delta.Usage.BillingUsage.Semantic)
	require.NotNil(t, delta.Usage.BillingUsage.GeminiUsageMetadata)
	assert.Equal(t, 3868, delta.Usage.BillingUsage.GeminiUsageMetadata.PromptTokenCount)
	assert.Equal(t, 12, delta.Usage.BillingUsage.GeminiUsageMetadata.CandidatesTokenCount)
}

func TestNormalizeCacheCreationSplit(t *testing.T) {
	cache5m, cache1h := NormalizeCacheCreationSplit(10, 3, 2)
	assert.Equal(t, 8, cache5m)
	assert.Equal(t, 2, cache1h)

	cache5m, cache1h = NormalizeCacheCreationSplit(3, 5, 1)
	assert.Equal(t, 5, cache5m)
	assert.Equal(t, 1, cache1h)
}

func ptr[T any](value T) *T {
	return &value
}
