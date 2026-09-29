package oairesponses

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeResponsesUsagePreservesImageCacheDetails(t *testing.T) {
	for _, tc := range []struct {
		name        string
		imageTokens int
	}{
		{name: "image cache", imageTokens: 200},
		{name: "explicit zero image cache", imageTokens: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imageTokens, audioTokens := tc.imageTokens, 0
			source := &dto.Usage{
				InputTokens:  1000,
				OutputTokens: 100,
				InputTokensDetails: &dto.InputTokenDetails{
					CachedTokens:         300,
					CachedCreationTokens: 20,
					CacheWriteTokens:     10,
					TextTokens:           400,
					ImageTokens:          600,
					CachedTokensDetails: &dto.CachedTokenDetails{
						ImageTokens: &imageTokens,
						AudioTokens: &audioTokens,
					},
				},
			}

			usage := NormalizeResponsesUsage(source)
			assert.Equal(t, *source.InputTokensDetails, usage.PromptTokensDetails)
			details := usage.PromptTokensDetails.CachedTokensDetails
			require.NotNil(t, details)
			require.NotNil(t, details.ImageTokens)
			require.NotNil(t, details.AudioTokens)
			assert.Nil(t, details.TextTokens, "missing modalities must remain absent")

			imageTokens, audioTokens = 999, 888
			source.InputTokensDetails.ImageTokens = 9999
			assert.Equal(t, tc.imageTokens, *details.ImageTokens, "later source updates must not change billable image cache")
			assert.Zero(t, *details.AudioTokens, "explicit zero must survive normalization and source mutation")
			assert.Equal(t, 600, usage.PromptTokensDetails.ImageTokens)
		})
	}
}

func TestResponsesResponseToChatCompletionsPreservesTextAndToolCalls(t *testing.T) {
	resp := &dto.OpenAIResponsesResponse{
		ID:        "resp_1",
		CreatedAt: 123,
		Model:     "gpt-test",
		Status:    []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type: responsesOutputTypeMessage,
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "I will call a tool."},
				},
			},
			{
				Type:      responsesOutputTypeFunctionCall,
				ID:        "fc_1",
				CallId:    "call_1",
				Name:      "lookup",
				Arguments: []byte(`{"q":"x"}`),
			},
		},
		Usage: &dto.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
	}

	chat, usage, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl_1")
	require.NoError(t, err)
	require.NotNil(t, usage)

	require.Len(t, chat.Choices, 1)
	assert.Equal(t, "tool_calls", chat.Choices[0].FinishReason)
	assert.Equal(t, "I will call a tool.", chat.Choices[0].Message.StringContent())
	toolCalls := chat.Choices[0].Message.ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call_1", toolCalls[0].ID)
	assert.Equal(t, "lookup", toolCalls[0].Function.Name)
	assert.Equal(t, `{"q":"x"}`, toolCalls[0].Function.Arguments)
	assert.Equal(t, 7, usage.TotalTokens)
}

func TestResponsesResponseToChatCompletionsPreservesReasoningSummary(t *testing.T) {
	resp := &dto.OpenAIResponsesResponse{
		ID:     "resp_1",
		Model:  "gpt-test",
		Status: []byte(`"completed"`),
		Output: []dto.ResponsesOutput{
			{
				Type: responsesOutputTypeReasoning,
				Summary: []dto.ResponsesReasoningSummaryPart{
					{Type: "summary_text", Text: "first summary"},
					{Type: "summary_text", Text: "second summary"},
				},
			},
			{
				Type: responsesOutputTypeMessage,
				Role: "assistant",
				Content: []dto.ResponsesOutputContent{
					{Type: "output_text", Text: "final"},
				},
			},
		},
	}

	chat, _, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl_1")
	require.NoError(t, err)
	assert.Equal(t, "first summary\n\nsecond summary", chat.Choices[0].Message.GetReasoningContent())
	assert.Equal(t, "final", chat.Choices[0].Message.StringContent())
}

func TestResponsesResponseToChatCompletionsSeparatesInterleavedOutputItems(t *testing.T) {
	resp := &dto.OpenAIResponsesResponse{
		ID:     "resp_1",
		Model:  "gpt-test",
		Status: []byte(`"completed"`),
		Output: interleavedReasoningAndTextOutput(),
	}

	chat, _, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl_1")
	require.NoError(t, err)
	assert.Equal(t, "**Planning file inspection**\n\n**Clarifying environment task requirements**", chat.Choices[0].Message.GetReasoningContent())
	assert.Equal(t, "I’ll inspect the starter repository.\n\nWhat would you like me to build?", chat.Choices[0].Message.StringContent())
}

func TestResponsesFinishReasonFromIncompleteStatus(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "max output", reason: responsesIncompleteReasonMaxTokens, want: "length"},
		{name: "content filter", reason: responsesIncompleteReasonContentFilter, want: "content_filter"},
		{name: "unknown", reason: "other", want: "length"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResponsesFinishReasonFromStatus(&dto.OpenAIResponsesResponse{
				Status:            []byte(`"incomplete"`),
				IncompleteDetails: &dto.IncompleteDetails{Reason: tt.reason},
			})
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResponsesStreamEventToChatChunksUsesOutputIndexForToolArguments(t *testing.T) {
	state := newTestResponsesStreamState()
	outputIndex := 1

	var chunks []dto.ChatCompletionsStreamResponse
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{Type: responsesEventCreated})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{Type: responsesEventOutputTextDelta, Delta: "text before tool"})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventFunctionArgsDelta,
		OutputIndex: &outputIndex,
		Delta:       `{"cmd":"ls"}`,
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		Item: &dto.ResponsesOutput{
			Type:   responsesOutputTypeFunctionCall,
			ID:     "fc_1",
			CallId: "call_1",
			Name:   "exec",
		},
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type: responsesEventCompleted,
		Response: &dto.OpenAIResponsesResponse{
			Status: []byte(`"completed"`),
			Usage:  &dto.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		},
	})...)

	require.Len(t, chunks, 4)
	assert.Equal(t, "assistant", chunks[0].Choices[0].Delta.Role)
	assert.Equal(t, "text before tool", chunks[1].Choices[0].Delta.GetContentString())
	tool := chunks[2].Choices[0].Delta.ToolCalls[0]
	require.NotNil(t, tool.Index)
	assert.Equal(t, 0, *tool.Index)
	assert.Equal(t, "call_1", tool.ID)
	assert.Equal(t, "exec", tool.Function.Name)
	assert.Equal(t, `{"cmd":"ls"}`, tool.Function.Arguments)
	require.NotNil(t, chunks[3].Choices[0].FinishReason)
	assert.Equal(t, "tool_calls", *chunks[3].Choices[0].FinishReason)
	assert.Equal(t, 3, state.Usage.TotalTokens)
}

func TestResponsesStreamEventToChatChunksDoesNotDuplicatePendingArgsWithOutputIndexAndItemID(t *testing.T) {
	state := newTestResponsesStreamState()
	outputIndex := 1

	var chunks []dto.ChatCompletionsStreamResponse
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{Type: responsesEventCreated})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventFunctionArgsDelta,
		OutputIndex: &outputIndex,
		ItemID:      "fc_1",
		Delta:       `{"q":"x"}`,
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		ItemID:      "fc_1",
		Item: &dto.ResponsesOutput{
			Type:   responsesOutputTypeFunctionCall,
			ID:     "fc_1",
			CallId: "call_1",
			Name:   "lookup",
		},
	})...)

	require.Len(t, chunks, 2)
	tool := chunks[1].Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_1", tool.ID)
	assert.Equal(t, "lookup", tool.Function.Name)
	assert.Equal(t, `{"q":"x"}`, tool.Function.Arguments)
	assert.Empty(t, state.pendingArgsByOutputIndex)
	assert.Empty(t, state.pendingArgsByItemID)
}

func TestResponsesStreamEventToChatChunksDrainsItemOnlyPendingArgsWhenOutputIndexArrives(t *testing.T) {
	state := newTestResponsesStreamState()
	outputIndex := 1

	var chunks []dto.ChatCompletionsStreamResponse
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{Type: responsesEventCreated})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:   responsesEventFunctionArgsDelta,
		ItemID: "fc_1",
		Delta:  `{"q":"x"}`,
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		ItemID:      "fc_1",
		Item: &dto.ResponsesOutput{
			Type:   responsesOutputTypeFunctionCall,
			CallId: "call_1",
			Name:   "lookup",
		},
	})...)

	require.Len(t, chunks, 2)
	tool := chunks[1].Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_1", tool.ID)
	assert.Equal(t, "lookup", tool.Function.Name)
	assert.Equal(t, `{"q":"x"}`, tool.Function.Arguments)
	assert.Empty(t, state.pendingArgsByOutputIndex)
	assert.Empty(t, state.pendingArgsByItemID)
}

func TestResponsesStreamEventToChatChunksCustomToolAndReasoning(t *testing.T) {
	state := newTestResponsesStreamState()
	outputIndex := 0

	chunks := mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:  responsesEventReasoningTextDelta,
		Delta: "thinking",
	})
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		Item: &dto.ResponsesOutput{
			Type:   responsesOutputTypeCustomToolCall,
			ID:     "ct_1",
			CallId: "call_custom",
			Name:   "apply_patch",
		},
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventCustomToolInputDelta,
		OutputIndex: &outputIndex,
		Delta:       "patch body",
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type: responsesEventIncomplete,
		Response: &dto.OpenAIResponsesResponse{
			IncompleteDetails: &dto.IncompleteDetails{Reason: responsesIncompleteReasonContentFilter},
		},
	})...)

	require.Len(t, chunks, 5)
	assert.Equal(t, "thinking", chunks[1].Choices[0].Delta.GetReasoningContent())
	assert.Equal(t, "apply_patch", chunks[2].Choices[0].Delta.ToolCalls[0].Function.Name)
	assert.Equal(t, "patch body", chunks[3].Choices[0].Delta.ToolCalls[0].Function.Arguments)
	require.NotNil(t, chunks[4].Choices[0].FinishReason)
	assert.Equal(t, "content_filter", *chunks[4].Choices[0].FinishReason)
}

func TestResponsesStreamEventToChatChunksUsesTerminalDoneOutput(t *testing.T) {
	state := newTestResponsesStreamState()
	chunks := mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type: responsesEventDone,
		Response: &dto.OpenAIResponsesResponse{
			Status: []byte(`"completed"`),
			Output: []dto.ResponsesOutput{
				{
					Type: responsesOutputTypeMessage,
					Role: "assistant",
					Content: []dto.ResponsesOutputContent{
						{Type: "output_text", Text: "terminal text"},
					},
				},
				{
					Type:      responsesOutputTypeFunctionCall,
					ID:        "fc_1",
					CallId:    "call_1",
					Name:      "lookup",
					Arguments: []byte(`{"q":"x"}`),
				},
			},
		},
	})

	require.Len(t, chunks, 4)
	assert.Equal(t, "assistant", chunks[0].Choices[0].Delta.Role)
	assert.Equal(t, "terminal text", chunks[1].Choices[0].Delta.GetContentString())
	tool := chunks[2].Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "lookup", tool.Function.Name)
	assert.Equal(t, `{"q":"x"}`, tool.Function.Arguments)
	require.NotNil(t, chunks[3].Choices[0].FinishReason)
	assert.Equal(t, "tool_calls", *chunks[3].Choices[0].FinishReason)
}

func TestResponsesStreamEventToChatChunksDoesNotResendToolOnTerminalOutput(t *testing.T) {
	state := newTestResponsesStreamState()
	outputIndex := 0

	var chunks []dto.ChatCompletionsStreamResponse
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{Type: responsesEventCreated})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		Item: &dto.ResponsesOutput{
			Type:   responsesOutputTypeFunctionCall,
			ID:     "fc_1",
			CallId: "call_1",
			Name:   "lookup",
		},
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type:        responsesEventFunctionArgsDelta,
		OutputIndex: &outputIndex,
		Delta:       `{"q":"x"}`,
	})...)
	chunks = append(chunks, mustStreamChunks(t, state, &dto.ResponsesStreamResponse{
		Type: responsesEventCompleted,
		Response: &dto.OpenAIResponsesResponse{
			Status: []byte(`"completed"`),
			Output: []dto.ResponsesOutput{
				{
					Type:      responsesOutputTypeFunctionCall,
					ID:        "fc_1",
					CallId:    "call_1",
					Name:      "lookup",
					Arguments: []byte(`{"q":"x"}`),
				},
			},
		},
	})...)

	var totalArgs strings.Builder
	toolIndexes := map[int]bool{}
	var finishReason string
	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			for _, tc := range choice.Delta.ToolCalls {
				require.NotNil(t, tc.Index)
				toolIndexes[*tc.Index] = true
				totalArgs.WriteString(tc.Function.Arguments)
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
	}

	assert.Equal(t, map[int]bool{0: true}, toolIndexes)
	assert.Equal(t, `{"q":"x"}`, totalArgs.String())
	assert.Equal(t, "tool_calls", finishReason)
}

func TestFinalizeResponsesToChatStreamFlushesPendingDeltaOnlyArguments(t *testing.T) {
	state := newTestResponsesStreamState()
	outputIndex := 2
	_, err := ResponsesStreamEventToChatChunks(&dto.ResponsesStreamResponse{
		Type:        responsesEventFunctionArgsDelta,
		OutputIndex: &outputIndex,
		Delta:       `{"pending":true}`,
	}, state)
	require.NoError(t, err)

	chunks := FinalizeResponsesToChatStream(state)
	require.Len(t, chunks, 3)
	tool := chunks[1].Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_output_2", tool.ID)
	assert.Equal(t, `{"pending":true}`, tool.Function.Arguments)
	require.NotNil(t, chunks[2].Choices[0].FinishReason)
	assert.Equal(t, "tool_calls", *chunks[2].Choices[0].FinishReason)
}

func TestResponsesStreamEventToChatChunksFailedEventReturnsError(t *testing.T) {
	_, err := ResponsesStreamEventToChatChunks(&dto.ResponsesStreamResponse{Type: responsesEventFailed}, newTestResponsesStreamState())
	require.Error(t, err)
}

func TestResponsesBufferedAccumulatorSupplementsEmptyTerminalOutput(t *testing.T) {
	acc := NewResponsesBufferedAccumulator()
	outputIndex := 1
	acc.ProcessEvent(&dto.ResponsesStreamResponse{Type: responsesEventOutputTextDelta, Delta: "buffered text"})
	acc.ProcessEvent(&dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		Item: &dto.ResponsesOutput{
			Type:   responsesOutputTypeFunctionCall,
			ID:     "fc_1",
			CallId: "call_1",
			Name:   "lookup",
		},
	})
	acc.ProcessEvent(&dto.ResponsesStreamResponse{
		Type:        responsesEventFunctionArgsDelta,
		OutputIndex: &outputIndex,
		Delta:       `{"q":"x"}`,
	})

	resp := &dto.OpenAIResponsesResponse{
		Status: []byte(`"completed"`),
		Model:  "gpt-test",
	}
	acc.SupplementResponseOutput(resp)

	chat, _, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl_1")
	require.NoError(t, err)
	assert.Equal(t, "buffered text", chat.Choices[0].Message.StringContent())
	toolCalls := chat.Choices[0].Message.ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, `{"q":"x"}`, toolCalls[0].Function.Arguments)
}

func TestResponsesBufferedAccumulatorDoesNotDuplicatePendingArgsWithOutputIndexAndItemID(t *testing.T) {
	acc := NewResponsesBufferedAccumulator()
	outputIndex := 1
	acc.ProcessEvent(&dto.ResponsesStreamResponse{
		Type:        responsesEventFunctionArgsDelta,
		OutputIndex: &outputIndex,
		ItemID:      "fc_1",
		Delta:       `{"q":"x"}`,
	})
	acc.ProcessEvent(&dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		ItemID:      "fc_1",
		Item: &dto.ResponsesOutput{
			Type:   responsesOutputTypeFunctionCall,
			ID:     "fc_1",
			CallId: "call_1",
			Name:   "lookup",
		},
	})

	resp := &dto.OpenAIResponsesResponse{
		Status: []byte(`"completed"`),
		Model:  "gpt-test",
	}
	acc.SupplementResponseOutput(resp)

	chat, _, err := ResponsesResponseToChatCompletionsResponse(resp, "chatcmpl_1")
	require.NoError(t, err)
	toolCalls := chat.Choices[0].Message.ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, `{"q":"x"}`, toolCalls[0].Function.Arguments)
	assert.Empty(t, acc.pendingByOutputIndex)
	assert.Empty(t, acc.pendingByItemID)
}

func TestResponsesBufferedAccumulatorPreservesInterleavedReasoningAndTextItems(t *testing.T) {
	acc := NewResponsesBufferedAccumulator()
	events := []dto.ResponsesStreamResponse{
		bufferedOutputItemAdded(0, "rs_1", responsesOutputTypeReasoning),
		{Type: responsesEventReasoningSummaryDelta, OutputIndex: intPointer(0), ItemID: "rs_1", Delta: "**Planning file inspection**"},
		bufferedOutputItemAdded(1, "msg_1", responsesOutputTypeMessage),
		{Type: responsesEventOutputTextDelta, OutputIndex: intPointer(1), ItemID: "msg_1", Delta: "I’ll inspect the starter repository."},
		bufferedOutputItemAdded(2, "rs_2", responsesOutputTypeReasoning),
		{Type: responsesEventReasoningSummaryDelta, OutputIndex: intPointer(2), ItemID: "rs_2", Delta: "**Clarifying environment task requirements**"},
		bufferedOutputItemAdded(3, "msg_2", responsesOutputTypeMessage),
		{Type: responsesEventOutputTextDelta, OutputIndex: intPointer(3), ItemID: "msg_2", Delta: "What would you like me to build?"},
	}
	for index := range events {
		acc.ProcessEvent(&events[index])
	}

	output := acc.BuildOutput()
	require.Len(t, output, 4)
	assert.Equal(t, []string{
		responsesOutputTypeReasoning,
		responsesOutputTypeMessage,
		responsesOutputTypeReasoning,
		responsesOutputTypeMessage,
	}, []string{output[0].Type, output[1].Type, output[2].Type, output[3].Type})
	assert.Equal(t, "**Planning file inspection**", output[0].Summary[0].Text)
	assert.Equal(t, "I’ll inspect the starter repository.", output[1].Content[0].Text)
	assert.Equal(t, "**Clarifying environment task requirements**", output[2].Summary[0].Text)
	assert.Equal(t, "What would you like me to build?", output[3].Content[0].Text)
}

func TestResponsesStreamTerminalOutputPreservesInterleavedReasoningAndTextItems(t *testing.T) {
	state := newTestResponsesStreamState()
	chunks, err := ResponsesStreamEventToChatChunks(&dto.ResponsesStreamResponse{
		Type: responsesEventCompleted,
		Response: &dto.OpenAIResponsesResponse{
			Status: []byte(`"completed"`),
			Output: interleavedReasoningAndTextOutput(),
		},
	}, state)
	require.NoError(t, err)

	var deltas []string
	for _, chunk := range chunks {
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.ReasoningContent != nil {
			deltas = append(deltas, "thinking:"+*delta.ReasoningContent)
		}
		if delta.Content != nil && *delta.Content != "" {
			deltas = append(deltas, "text:"+*delta.Content)
		}
	}
	assert.Equal(t, []string{
		"thinking:**Planning file inspection**",
		"text:I’ll inspect the starter repository.",
		"thinking:**Clarifying environment task requirements**",
		"text:What would you like me to build?",
	}, deltas)
}

func bufferedOutputItemAdded(outputIndex int, itemID string, itemType string) dto.ResponsesStreamResponse {
	return dto.ResponsesStreamResponse{
		Type:        responsesEventOutputItemAdded,
		OutputIndex: &outputIndex,
		ItemID:      itemID,
		Item:        &dto.ResponsesOutput{ID: itemID, Type: itemType},
	}
}

func interleavedReasoningAndTextOutput() []dto.ResponsesOutput {
	return []dto.ResponsesOutput{
		{Type: responsesOutputTypeReasoning, Summary: []dto.ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "**Planning file inspection**"}}},
		{Type: responsesOutputTypeMessage, Role: "assistant", Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "I’ll inspect the starter repository."}}},
		{Type: responsesOutputTypeReasoning, Summary: []dto.ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "**Clarifying environment task requirements**"}}},
		{Type: responsesOutputTypeMessage, Role: "assistant", Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "What would you like me to build?"}}},
	}
}

func intPointer(value int) *int {
	return &value
}

func newTestResponsesStreamState() *ResponsesToChatStreamState {
	state := NewResponsesToChatStreamState("gpt-test", false)
	state.ID = "chatcmpl_test"
	state.Created = 123
	return state
}

func mustStreamChunks(t *testing.T, state *ResponsesToChatStreamState, event *dto.ResponsesStreamResponse) []dto.ChatCompletionsStreamResponse {
	t.Helper()
	chunks, err := ResponsesStreamEventToChatChunks(event, state)
	require.NoError(t, err)
	return chunks
}
