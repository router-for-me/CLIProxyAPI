package oairesponses

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesToClaudeStreamDoesNotRepeatBlocksFromDoneAndCompletedEvents(t *testing.T) {
	state := NewResponsesToClaudeStreamState("", "")
	arguments := `{"q":"x"}`
	argumentRaw, err := kitutil.Marshal(arguments)
	require.NoError(t, err)
	statusRaw, err := kitutil.Marshal("completed")
	require.NoError(t, err)

	reasoningItem := dto.ResponsesOutput{
		Type:    responsesOutputTypeReasoning,
		ID:      "rs_1",
		Summary: []dto.ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "plan"}},
	}
	messageItem := dto.ResponsesOutput{
		Type:    responsesOutputTypeMessage,
		ID:      "msg_1",
		Role:    "assistant",
		Content: []dto.ResponsesOutputContent{{Type: "output_text", Text: "hello"}},
	}
	toolItem := dto.ResponsesOutput{
		Type:      responsesOutputTypeFunctionCall,
		ID:        "fc_1",
		CallId:    "call_1",
		Name:      "lookup",
		Arguments: argumentRaw,
	}

	events := []*dto.ResponsesStreamResponse{
		{Type: responsesEventCreated, Response: &dto.OpenAIResponsesResponse{ID: "resp_1", Model: "gpt-test"}},
		{Type: responsesEventOutputItemAdded, OutputIndex: kitutil.GetPointer(0), ItemID: reasoningItem.ID, Item: &dto.ResponsesOutput{Type: reasoningItem.Type, ID: reasoningItem.ID}},
		{Type: responsesEventReasoningSummaryDelta, OutputIndex: kitutil.GetPointer(0), ItemID: reasoningItem.ID, Delta: "plan"},
		{Type: responsesEventReasoningSummaryDone, OutputIndex: kitutil.GetPointer(0), ItemID: reasoningItem.ID, Text: kitutil.GetPointer("plan")},
		{Type: responsesEventOutputItemDone, OutputIndex: kitutil.GetPointer(0), ItemID: reasoningItem.ID, Item: &reasoningItem},
		{Type: responsesEventOutputItemAdded, OutputIndex: kitutil.GetPointer(1), ItemID: messageItem.ID, Item: &dto.ResponsesOutput{Type: messageItem.Type, ID: messageItem.ID, Role: "assistant"}},
		{Type: responsesEventOutputTextDelta, OutputIndex: kitutil.GetPointer(1), ItemID: messageItem.ID, Delta: "hello"},
		{Type: responsesEventOutputTextDone, OutputIndex: kitutil.GetPointer(1), ItemID: messageItem.ID, Text: kitutil.GetPointer("hello")},
		{Type: responsesEventOutputItemDone, OutputIndex: kitutil.GetPointer(1), ItemID: messageItem.ID, Item: &messageItem},
		{Type: responsesEventOutputItemAdded, OutputIndex: kitutil.GetPointer(2), ItemID: toolItem.ID, Item: &dto.ResponsesOutput{Type: toolItem.Type, ID: toolItem.ID, CallId: toolItem.CallId, Name: toolItem.Name}},
		{Type: responsesEventFunctionArgsDelta, OutputIndex: kitutil.GetPointer(2), ItemID: toolItem.ID, Delta: `{"q":`},
		{Type: responsesEventFunctionArgsDelta, OutputIndex: kitutil.GetPointer(2), ItemID: toolItem.ID, Delta: `"x"}`},
		{Type: responsesEventFunctionArgsDone, OutputIndex: kitutil.GetPointer(2), ItemID: toolItem.ID, Arguments: &arguments},
		{Type: responsesEventOutputItemDone, OutputIndex: kitutil.GetPointer(2), ItemID: toolItem.ID, Item: &toolItem},
		{
			Type: responsesEventCompleted,
			Response: &dto.OpenAIResponsesResponse{
				ID:     "resp_1",
				Model:  "gpt-test",
				Status: statusRaw,
				Output: []dto.ResponsesOutput{reasoningItem, messageItem, toolItem},
				Usage:  &dto.Usage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
			},
		},
	}

	var output []*dto.ClaudeResponse
	for _, event := range events {
		converted, _, err := state.ConvertChunk(event, 9)
		require.NoError(t, err)
		output = append(output, converted...)
	}

	starts := responsesOfType(output, "content_block_start")
	stops := responsesOfType(output, "content_block_stop")
	require.Len(t, responsesOfType(output, "message_start"), 1)
	require.Len(t, starts, 3)
	require.Len(t, stops, 3)
	require.Len(t, responsesOfType(output, "message_delta"), 1)
	require.Len(t, responsesOfType(output, "message_stop"), 1)
	assert.Equal(t, []int{0, 1, 2}, []int{starts[0].GetIndex(), starts[1].GetIndex(), starts[2].GetIndex()})
	assert.Equal(t, []string{"thinking", "text", "tool_use"}, []string{starts[0].ContentBlock.Type, starts[1].ContentBlock.Type, starts[2].ContentBlock.Type})
	assert.Equal(t, "plan", joinedClaudeDeltas(output, "thinking_delta"))
	assert.Equal(t, "hello", joinedClaudeDeltas(output, "text_delta"))
	assert.Equal(t, arguments, joinedClaudeDeltas(output, "input_json_delta"))
	messageDelta := responsesOfType(output, "message_delta")[0]
	require.NotNil(t, messageDelta.Delta.StopReason)
	assert.Equal(t, "tool_use", *messageDelta.Delta.StopReason)

	finalized, err := state.Finalize(9)
	require.NoError(t, err)
	assert.Empty(t, finalized)
	repeated, _, err := state.ConvertChunk(events[len(events)-1], 9)
	require.NoError(t, err)
	assert.Empty(t, repeated)
}

func responsesOfType(responses []*dto.ClaudeResponse, responseType string) []*dto.ClaudeResponse {
	filtered := make([]*dto.ClaudeResponse, 0)
	for _, response := range responses {
		if response != nil && response.Type == responseType {
			filtered = append(filtered, response)
		}
	}
	return filtered
}

func joinedClaudeDeltas(responses []*dto.ClaudeResponse, deltaType string) string {
	var result strings.Builder
	for _, response := range responses {
		if response == nil || response.Type != "content_block_delta" || response.Delta == nil || response.Delta.Type != deltaType {
			continue
		}
		switch deltaType {
		case "thinking_delta":
			if response.Delta.Thinking != nil {
				result.WriteString(*response.Delta.Thinking)
			}
		case "text_delta":
			if response.Delta.Text != nil {
				result.WriteString(*response.Delta.Text)
			}
		case "input_json_delta":
			if response.Delta.PartialJson != nil {
				result.WriteString(*response.Delta.PartialJson)
			}
		}
	}
	return result.String()
}
