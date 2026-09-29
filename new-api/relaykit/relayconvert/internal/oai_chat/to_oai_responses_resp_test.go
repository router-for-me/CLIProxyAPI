package oaichat

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionsResponseToResponsesPreservesTextToolCallsAndUsage(t *testing.T) {
	chat := &dto.OpenAITextResponse{
		Id:      "chatcmpl_1",
		Model:   "gpt-test",
		Created: 456,
		Choices: []dto.OpenAITextResponseChoice{
			{
				Message:      assistantMessageWithTool("I will call.", "call_1", "lookup", `{"q":"x"}`),
				FinishReason: "tool_calls",
			},
		},
		Usage: dto.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	}

	resp, usage, err := ChatCompletionsResponseToResponsesResponse(chat, "resp_1")
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, "resp_1", resp.ID)
	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, `"completed"`, string(resp.Status))
	assert.Equal(t, 3, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	require.Len(t, resp.Output, 2)
	assert.Equal(t, responsesOutputTypeMessage, resp.Output[0].Type)
	assert.Equal(t, "I will call.", resp.Output[0].Content[0].Text)
	assert.Equal(t, responsesOutputTypeFunctionCall, resp.Output[1].Type)
	assert.Equal(t, "call_1", resp.Output[1].CallId)
	assert.Equal(t, "lookup", resp.Output[1].Name)
	assert.Equal(t, `"{\"q\":\"x\"}"`, string(resp.Output[1].Arguments))
}

func TestChatCompletionsResponseToResponsesEmitsReasoningSummaryBeforeText(t *testing.T) {
	message := dto.Message{Role: "assistant", Content: "final answer"}
	message.ReasoningContent = lo.ToPtr("thinking summary")
	resp, _, err := ChatCompletionsResponseToResponsesResponse(&dto.OpenAITextResponse{
		Id:    "chatcmpl_1",
		Model: "gpt-test",
		Choices: []dto.OpenAITextResponseChoice{
			{Message: message, FinishReason: "stop"},
		},
	}, "resp_1")
	require.NoError(t, err)

	require.Len(t, resp.Output, 2)
	assert.Equal(t, responsesOutputTypeReasoning, resp.Output[0].Type)
	require.Len(t, resp.Output[0].Summary, 1)
	assert.Equal(t, "thinking summary", resp.Output[0].Summary[0].Text)
	assert.Empty(t, resp.Output[0].Content)
	assert.Equal(t, responsesOutputTypeMessage, resp.Output[1].Type)
	assert.Equal(t, "final answer", resp.Output[1].Content[0].Text)
}

func TestChatCompletionsResponseToResponsesMapsIncompleteFinishReasons(t *testing.T) {
	tests := []struct {
		name         string
		finishReason string
		wantReason   string
	}{
		{name: "length", finishReason: "length", wantReason: responsesIncompleteReasonMaxTokens},
		{name: "content filter", finishReason: "content_filter", wantReason: responsesIncompleteReasonContentFilter},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, _, err := ChatCompletionsResponseToResponsesResponse(&dto.OpenAITextResponse{
				Id:    "chatcmpl_1",
				Model: "gpt-test",
				Choices: []dto.OpenAITextResponseChoice{
					{
						Message:      dto.Message{Role: "assistant", Content: "partial"},
						FinishReason: tt.finishReason,
					},
				},
			}, "resp_1")
			require.NoError(t, err)

			assert.Equal(t, `"incomplete"`, string(resp.Status))
			require.NotNil(t, resp.IncompleteDetails)
			assert.Equal(t, tt.wantReason, resp.IncompleteDetails.Reason)
			require.Len(t, resp.Output, 1)
			assert.Equal(t, "incomplete", resp.Output[0].Status)
		})
	}
}

func TestChatCompletionsStreamToResponsesEventsAggregatesUsageAndToolArgs(t *testing.T) {
	state := NewChatToResponsesStreamState("resp_1", "gpt-test")
	state.Created = 123
	toolIndex := 0

	var events []ChatToResponsesStreamEvent
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Id:      "chatcmpl_1",
		Model:   "gpt-test",
		Created: 123,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"}},
		},
	})...)
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: lo.ToPtr("hello")}},
		},
	})...)
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []dto.ToolCallResponse{
				{Index: &toolIndex, ID: "call_1", Type: "function", Function: dto.FunctionResponse{Name: "lookup"}},
			}}},
		},
	})...)
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []dto.ToolCallResponse{
				{Index: &toolIndex, Function: dto.FunctionResponse{Arguments: `{"q":"x"}`}},
			}}},
		},
	})...)
	finishReason := "tool_calls"
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{Index: 0, FinishReason: &finishReason},
		},
	})...)
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Usage: &dto.Usage{PromptTokens: 2, CompletionTokens: 4, TotalTokens: 6},
	})...)
	events = append(events, FinalizeChatCompletionsStreamToResponses(state)...)

	require.Len(t, events, 10)
	assert.Equal(t, responsesEventCreated, events[0].Type)
	assert.Equal(t, responsesEventOutputTextDelta, events[2].Type)
	assert.Equal(t, "hello", events[2].Payload.Delta)
	assert.Equal(t, responsesEventFunctionArgsDelta, events[4].Type)
	assert.Equal(t, `{"q":"x"}`, events[4].Payload.Delta)
	assert.Equal(t, responsesEventCompleted, events[9].Type)
	require.NotNil(t, events[9].Payload.Response)
	assert.Equal(t, 6, events[9].Payload.Response.Usage.TotalTokens)
	require.Len(t, events[9].Payload.Response.Output, 2)
	assert.Equal(t, "hello", events[9].Payload.Response.Output[0].Content[0].Text)
	assert.Equal(t, `"{\"q\":\"x\"}"`, string(events[9].Payload.Response.Output[1].Arguments))
}

func TestChatCompletionsStreamToResponsesEmitsReasoningSummaryPartLifecycle(t *testing.T) {
	state := NewChatToResponsesStreamState("resp_1", "gpt-test")
	stop := "stop"

	var events []ChatToResponsesStreamEvent
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Id: "chatcmpl_1", Model: "gpt-test",
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: lo.ToPtr("think")},
		}},
	})...)
	events = append(events, mustResponsesEventsFromChatChunk(t, state, &dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta:        dto.ChatCompletionsStreamResponseChoiceDelta{Content: lo.ToPtr("answer")},
			FinishReason: &stop,
		}},
	})...)
	events = append(events, FinalizeChatCompletionsStreamToResponses(state)...)

	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	assert.Equal(t, []string{
		responsesEventCreated,
		responsesEventOutputItemAdded,
		responsesEventReasoningSummaryPartAdded,
		responsesEventReasoningSummaryDelta,
		responsesEventOutputItemAdded,
		responsesEventOutputTextDelta,
		"response.output_text.done",
		responsesEventOutputItemDone,
		responsesEventReasoningSummaryDone,
		responsesEventReasoningSummaryPartDone,
		responsesEventOutputItemDone,
		responsesEventCompleted,
	}, types)

	partAdded := events[2].Payload
	assert.Equal(t, "resp_1_reasoning_0", partAdded.ItemID)
	assert.Equal(t, 0, lo.FromPtr(partAdded.OutputIndex))
	assert.Equal(t, 0, lo.FromPtr(partAdded.SummaryIndex))
	require.NotNil(t, partAdded.Part)
	assert.Equal(t, dto.ResponsesReasoningSummaryPart{Type: "summary_text"}, *partAdded.Part)

	partDone := events[9].Payload
	assert.Equal(t, "resp_1_reasoning_0", partDone.ItemID)
	require.NotNil(t, partDone.Part)
	assert.Equal(t, dto.ResponsesReasoningSummaryPart{Type: "summary_text", Text: "think"}, *partDone.Part)
}

func TestChatCompletionsStreamToResponsesReopensItemsAfterMidStreamFinishReason(t *testing.T) {
	for _, emitSequenceNumber := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "sequence_numbers"}[emitSequenceNumber], func(t *testing.T) {
			state := NewChatToResponsesStreamState("resp_1", "gpt-test")
			state.EmitSequenceNumber = emitSequenceNumber
			toolIndex := 0
			toolCalls := "tool_calls"
			stop := "stop"

			chunks := []*dto.ChatCompletionsStreamResponse{
				{Id: "chatcmpl_1", Model: "gpt-test", Choices: []dto.ChatCompletionsStreamResponseChoice{{
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: lo.ToPtr("round 1")},
				}}},
				{Choices: []dto.ChatCompletionsStreamResponseChoice{{
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Content: lo.ToPtr("calling")},
				}}},
				{Choices: []dto.ChatCompletionsStreamResponseChoice{{
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ToolCalls: []dto.ToolCallResponse{{
						Index: &toolIndex, ID: "call_1", Type: "function",
						Function: dto.FunctionResponse{Name: "lookup", Arguments: "{}"},
					}}},
				}}},
				{Choices: []dto.ChatCompletionsStreamResponseChoice{{FinishReason: &toolCalls}}},
				{Choices: []dto.ChatCompletionsStreamResponseChoice{{
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: lo.ToPtr("round 2")},
				}}},
				{Choices: []dto.ChatCompletionsStreamResponseChoice{{
					Delta:        dto.ChatCompletionsStreamResponseChoiceDelta{Content: lo.ToPtr("final")},
					FinishReason: &stop,
				}}},
			}
			var events []ChatToResponsesStreamEvent
			for _, chunk := range chunks {
				events = append(events, mustResponsesEventsFromChatChunk(t, state, chunk)...)
			}
			events = append(events, FinalizeChatCompletionsStreamToResponses(state)...)

			open := map[string]bool{}
			partAdded, partDone := 0, 0
			for _, event := range events {
				itemID := event.Payload.ItemID
				if event.Payload.Item != nil {
					itemID = event.Payload.Item.ID
				}
				switch event.Type {
				case responsesEventOutputItemAdded:
					assert.Falsef(t, open[itemID], "item %q added twice", itemID)
					open[itemID] = true
				case responsesEventOutputItemDone:
					assert.Truef(t, open[itemID], "item %q done without being open", itemID)
					delete(open, itemID)
				case responsesEventReasoningSummaryPartAdded:
					partAdded++
					assert.Truef(t, open[itemID], "part added for closed item %q", itemID)
				case responsesEventReasoningSummaryPartDone:
					partDone++
					assert.Truef(t, open[itemID], "part done for closed item %q", itemID)
				case responsesEventReasoningSummaryDelta, responsesEventOutputTextDelta:
					assert.Truef(t, open[itemID], "%s for %q arrived without an active item", event.Type, itemID)
				}
			}
			assert.Empty(t, open)
			assert.Equal(t, 2, partAdded)
			assert.Equal(t, 2, partDone)

			completed := events[len(events)-1]
			require.Equal(t, responsesEventCompleted, completed.Type)
			require.NotNil(t, completed.Payload.Response)
			output := completed.Payload.Response.Output
			require.Len(t, output, 5)
			assert.Equal(t, []string{
				responsesOutputTypeReasoning, responsesOutputTypeMessage, responsesOutputTypeFunctionCall,
				responsesOutputTypeReasoning, responsesOutputTypeMessage,
			}, []string{output[0].Type, output[1].Type, output[2].Type, output[3].Type, output[4].Type})
			assert.Equal(t, "resp_1_reasoning_0", output[0].ID)
			assert.Equal(t, "round 1", output[0].Summary[0].Text)
			assert.Equal(t, "resp_1_msg_0", output[1].ID)
			assert.Equal(t, "calling", output[1].Content[0].Text)
			assert.Equal(t, "call_1", output[2].CallId)
			assert.Equal(t, "resp_1_reasoning_1", output[3].ID)
			assert.Equal(t, "round 2", output[3].Summary[0].Text)
			assert.Equal(t, "resp_1_msg_1", output[4].ID)
			assert.Equal(t, "final", output[4].Content[0].Text)
			for _, item := range output {
				assert.Equal(t, "completed", item.Status)
			}
		})
	}
}

func mustResponsesEventsFromChatChunk(t *testing.T, state *ChatToResponsesStreamState, chunk *dto.ChatCompletionsStreamResponse) []ChatToResponsesStreamEvent {
	t.Helper()
	events, err := ChatCompletionsStreamChunkToResponsesEvents(chunk, state)
	require.NoError(t, err)
	return events
}
