package oairesponses

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesRequestToChatCompletionsRequestInstructionsAndScalarInput(t *testing.T) {
	stream := true
	temperature := 0.0
	topP := 0.9
	maxOutputTokens := uint(128)
	parallelToolCalls := true

	got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
		Model:                "gpt-test",
		Instructions:         mustRawMessage(t, "system rules"),
		Input:                mustRawMessage(t, "hello"),
		Stream:               &stream,
		StreamOptions:        &dto.StreamOptions{IncludeUsage: true},
		MaxOutputTokens:      &maxOutputTokens,
		Temperature:          &temperature,
		TopP:                 &topP,
		User:                 mustRawMessage(t, "user-1"),
		Store:                mustRawMessage(t, false),
		Metadata:             mustRawMessage(t, map[string]any{"trace": "abc"}),
		ParallelToolCalls:    mustRawMessage(t, parallelToolCalls),
		PromptCacheKey:       mustRawMessage(t, "cache-key"),
		PromptCacheRetention: mustRawMessage(t, "24h"),
		Reasoning:            &dto.Reasoning{Effort: "medium"},
	})
	require.NoError(t, err)

	assert.Equal(t, "gpt-test", got.Model)
	require.Len(t, got.Messages, 2)
	assert.Equal(t, dto.Message{Role: "system", Content: "system rules"}, got.Messages[0])
	assert.Equal(t, dto.Message{Role: "user", Content: "hello"}, got.Messages[1])
	assert.Same(t, &stream, got.Stream)
	require.NotNil(t, got.StreamOptions)
	assert.True(t, got.StreamOptions.IncludeUsage)
	assert.Equal(t, maxOutputTokens, lo.FromPtr(got.MaxCompletionTokens))
	assert.Equal(t, 0.0, lo.FromPtr(got.Temperature))
	assert.Equal(t, 0.9, lo.FromPtr(got.TopP))
	assert.True(t, lo.FromPtr(got.ParallelTooCalls))
	assert.Equal(t, "cache-key", got.PromptCacheKey)
	assert.Equal(t, "medium", got.ReasoningEffort)
	assert.Equal(t, `"user-1"`, string(got.User))
	assert.Equal(t, `false`, string(got.Store))
	assert.Equal(t, "abc", gjson.GetBytes(got.Metadata, "trace").String())
}

func TestResponsesRequestToChatCompletionsRequestPreservesQwenThinkingBudget(t *testing.T) {
	tests := []struct {
		name   string
		budget json.RawMessage
		want   int64
	}{
		{name: "positive budget", budget: json.RawMessage(`128`), want: 128},
		{name: "zero budget", budget: json.RawMessage(`0`), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
				Model:          "qwen-plus",
				Input:          mustRawMessage(t, "hello"),
				EnableThinking: json.RawMessage(`true`),
				ThinkingBudget: tt.budget,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.budget, got.ThinkingBudget)

			encoded, err := kitutil.Marshal(got)
			require.NoError(t, err)

			assert.True(t, gjson.GetBytes(encoded, "enable_thinking").Bool())
			value := gjson.GetBytes(encoded, "thinking_budget")
			assert.True(t, value.Exists())
			assert.Equal(t, tt.want, value.Int())
		})
	}
}

func TestResponsesRequestToChatCompletionsRequestMultimodalInput(t *testing.T) {
	got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "look"},
					{"type": "input_image", "image_url": "https://example.test/a.png", "detail": "low"},
					{"type": "input_file", "file_id": "file_1", "filename": "a.txt"},
					{"type": "input_audio", "input_audio": map[string]any{"data": "abc", "format": "wav"}},
					{"type": "input_video", "video_url": map[string]any{"url": "https://example.test/v.mp4"}},
				},
			},
		}),
	})
	require.NoError(t, err)

	require.Len(t, got.Messages, 1)
	assert.Equal(t, "user", got.Messages[0].Role)
	parts := got.Messages[0].ParseContent()
	require.Len(t, parts, 5)
	assert.Equal(t, dto.ContentTypeText, parts[0].Type)
	assert.Equal(t, "look", parts[0].Text)
	assert.Equal(t, dto.ContentTypeImageURL, parts[1].Type)
	assert.Equal(t, "https://example.test/a.png", parts[1].GetImageMedia().Url)
	assert.Equal(t, dto.ContentTypeFile, parts[2].Type)
	assert.Equal(t, "file_1", parts[2].GetFile().FileId)
	assert.Equal(t, dto.ContentTypeInputAudio, parts[3].Type)
	assert.Equal(t, "wav", parts[3].GetInputAudio().Format)
	assert.Equal(t, dto.ContentTypeVideoUrl, parts[4].Type)
	assert.Equal(t, "https://example.test/v.mp4", parts[4].GetVideoUrl().Url)
}

func TestResponsesRequestToChatCompletionsRequestAssistantTextAndFunctionCallCoexist(t *testing.T) {
	got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
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
	})
	require.NoError(t, err)

	require.Len(t, got.Messages, 2)
	assert.Equal(t, "assistant", got.Messages[0].Role)
	assert.Equal(t, "I will call.", got.Messages[0].StringContent())
	toolCalls := got.Messages[0].ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call_1", toolCalls[0].ID)
	assert.Equal(t, "function", toolCalls[0].Type)
	assert.Equal(t, "lookup", toolCalls[0].Function.Name)
	assert.JSONEq(t, `{"q":"x"}`, toolCalls[0].Function.Arguments)
	assert.Equal(t, "tool", got.Messages[1].Role)
	assert.Equal(t, "call_1", got.Messages[1].ToolCallId)
	assert.JSONEq(t, `{"ok":true}`, got.Messages[1].StringContent())
}

func TestResponsesRequestToChatCompletionsRequestOnlyFunctionCallCreatesAssistant(t *testing.T) {
	got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": `{"q":"x"}`,
			},
		}),
	})
	require.NoError(t, err)

	require.Len(t, got.Messages, 1)
	assert.Equal(t, "assistant", got.Messages[0].Role)
	assert.Nil(t, got.Messages[0].Content)
	toolCalls := got.Messages[0].ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, `{"q":"x"}`, toolCalls[0].Function.Arguments)
}

func TestResponsesRequestToChatCompletionsRequestToolsToolChoiceAndTextFormat(t *testing.T) {
	got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: mustRawMessage(t, "hello"),
		Tools: mustRawMessage(t, []map[string]any{
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
		}),
		ToolChoice: mustRawMessage(t, map[string]any{
			"type": "function",
			"name": "lookup",
		}),
		Text: mustRawMessage(t, map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   "answer",
				"schema": map[string]any{"type": "object"},
				"strict": true,
			},
		}),
	})
	require.NoError(t, err)

	require.Len(t, got.Tools, 1)
	assert.Equal(t, "function", got.Tools[0].Type)
	assert.Equal(t, "lookup", got.Tools[0].Function.Name)
	assert.Equal(t, "Lookup data", got.Tools[0].Function.Description)
	assert.Equal(t, "object", got.Tools[0].Function.Parameters.(map[string]any)["type"])
	assert.Equal(t, map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "lookup",
		},
	}, got.ToolChoice)
	require.NotNil(t, got.ResponseFormat)
	assert.Equal(t, "json_schema", got.ResponseFormat.Type)
	assert.Equal(t, "answer", gjson.GetBytes(got.ResponseFormat.JsonSchema, "name").String())
	assert.True(t, gjson.GetBytes(got.ResponseFormat.JsonSchema, "strict").Bool())
}

func TestResponsesRequestToChatCompletionsRequestCustomToolCallPreservesRawShape(t *testing.T) {
	got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: mustRawMessage(t, []map[string]any{
			{
				"type":    "custom_tool_call",
				"call_id": "call_custom",
				"name":    "apply_patch",
				"input":   "patch body",
			},
		}),
	})
	require.NoError(t, err)

	require.Len(t, got.Messages, 1)
	toolCalls := got.Messages[0].ParseToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, dto.CustomType, toolCalls[0].Type)
	assert.Equal(t, "call_custom", toolCalls[0].ID)
	assert.Equal(t, "apply_patch", toolCalls[0].Function.Name)
	assert.Equal(t, "patch body", toolCalls[0].Function.Arguments)
	assert.Equal(t, "custom_tool_call", gjson.GetBytes(toolCalls[0].Custom, "type").String())
	assert.Equal(t, "patch body", gjson.GetBytes(toolCalls[0].Custom, "input").String())
}

func TestResponsesRequestToChatCompletionsRequestRejectsStatefulFields(t *testing.T) {
	tests := []struct {
		name string
		req  *dto.OpenAIResponsesRequest
		want string
	}{
		{
			name: "conversation",
			req:  &dto.OpenAIResponsesRequest{Model: "gpt-test", Conversation: mustRawMessage(t, "conv_1")},
			want: "conversation",
		},
		{
			name: "previous response",
			req:  &dto.OpenAIResponsesRequest{Model: "gpt-test", PreviousResponseID: "resp_1"},
			want: "previous_response_id",
		},
		{
			name: "prompt",
			req:  &dto.OpenAIResponsesRequest{Model: "gpt-test", Prompt: mustRawMessage(t, map[string]any{"id": "pmpt_1"})},
			want: "prompt",
		},
		{
			name: "context management",
			req:  &dto.OpenAIResponsesRequest{Model: "gpt-test", ContextManagement: mustRawMessage(t, map[string]any{"type": "auto"})},
			want: "context_management",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResponsesRequestToChatCompletionsRequest(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Contains(t, err.Error(), "stateful fields")
		})
	}
}

func TestResponsesRequestToChatCompletionsRequestPreservesPenalties(t *testing.T) {
	tests := []struct {
		name          string
		frequencyRaw  json.RawMessage
		frequencyWant *float64
		presenceRaw   json.RawMessage
		presenceWant  *float64
	}{
		{
			name:          "positive values",
			frequencyRaw:  json.RawMessage(`0.5`),
			frequencyWant: lo.ToPtr(0.5),
			presenceRaw:   json.RawMessage(`1.5`),
			presenceWant:  lo.ToPtr(1.5),
		},
		{
			name:          "explicit zero values",
			frequencyRaw:  json.RawMessage(`0.0`),
			frequencyWant: lo.ToPtr(0.0),
			presenceRaw:   json.RawMessage(`0.0`),
			presenceWant:  lo.ToPtr(0.0),
		},
		{
			name:         "unset stays nil",
			frequencyRaw: nil,
			presenceRaw:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
				Model:            "gpt-test",
				Input:            mustRawMessage(t, "hello"),
				FrequencyPenalty: tt.frequencyRaw,
				PresencePenalty:  tt.presenceRaw,
			})
			require.NoError(t, err)

			assert.Equal(t, tt.frequencyWant, got.FrequencyPenalty)
			assert.Equal(t, tt.presenceWant, got.PresencePenalty)
		})
	}
}

func TestResponsesRequestToChatCompletionsRequestRejectsMalformedPenalty(t *testing.T) {
	_, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
		Model:            "gpt-test",
		Input:            mustRawMessage(t, "hello"),
		FrequencyPenalty: json.RawMessage(`"not-a-number"`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "frequency_penalty")
}

func TestResponsesRequestToChatCompletionsRequestToolOutputContentParts(t *testing.T) {
	const dataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	imagePart := map[string]any{"type": "input_image", "image_url": dataURL}

	tests := []struct {
		name        string
		output      any
		wantContent string
		jsonContent bool
		wantMedia   []string
	}{
		{
			name:        "text and image keep text on tool message",
			output:      []any{map[string]any{"type": "input_text", "text": "screenshot taken"}, imagePart},
			wantContent: "screenshot taken",
			wantMedia:   []string{dto.ContentTypeImageURL},
		},
		{
			name:        "image only uses placeholder",
			output:      []any{imagePart},
			wantContent: "[image]",
			wantMedia:   []string{dto.ContentTypeImageURL},
		},
		{
			name:        "mixed media dedupes placeholder labels",
			output:      []any{imagePart, map[string]any{"type": "input_file", "file_id": "file_1"}, imagePart},
			wantContent: "[image] [file]",
			wantMedia:   []string{dto.ContentTypeImageURL, dto.ContentTypeFile, dto.ContentTypeImageURL},
		},
		{
			name: "text parts join with newline",
			output: []any{
				map[string]any{"type": "input_text", "text": "first"},
				map[string]any{"type": "output_text", "text": ""},
				map[string]any{"type": "text", "text": "second"},
			},
			wantContent: "first\nsecond",
		},
		{
			name:        "string passes through",
			output:      "done",
			wantContent: "done",
		},
		{
			name:        "object stays json",
			output:      map[string]any{"ok": true},
			wantContent: `{"ok":true}`,
			jsonContent: true,
		},
		{
			name:        "plain array stays json",
			output:      []any{1, 2},
			wantContent: `[1,2]`,
			jsonContent: true,
		},
		{
			name:        "unknown part keeps whole array",
			output:      []any{imagePart, map[string]any{"type": "refusal", "refusal": "no"}},
			wantContent: `[{"type":"input_image","image_url":"` + dataURL + `"},{"type":"refusal","refusal":"no"}]`,
			jsonContent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
				Model: "gpt-test",
				Input: mustRawMessage(t, []map[string]any{
					{"type": "function_call", "call_id": "call_1", "name": "view_image", "arguments": "{}"},
					{"type": "function_call_output", "call_id": "call_1", "output": tt.output},
					{"role": "user", "content": "next"},
				}),
			})
			require.NoError(t, err)

			wantLen := 3
			if tt.wantMedia != nil {
				wantLen = 4
			}
			require.Len(t, got.Messages, wantLen)
			assert.Equal(t, "tool", got.Messages[1].Role)
			assert.Equal(t, "call_1", got.Messages[1].ToolCallId)
			if tt.jsonContent {
				assert.JSONEq(t, tt.wantContent, got.Messages[1].StringContent())
			} else {
				assert.Equal(t, tt.wantContent, got.Messages[1].StringContent())
			}
			assert.Equal(t, dto.Message{Role: "user", Content: "next"}, got.Messages[wantLen-1])
			if tt.wantMedia == nil {
				return
			}

			assert.Equal(t, "user", got.Messages[2].Role)
			parts := got.Messages[2].ParseContent()
			require.Len(t, parts, len(tt.wantMedia))
			for i, wantType := range tt.wantMedia {
				assert.Equal(t, wantType, parts[i].Type)
			}
			require.NotNil(t, parts[0].GetImageMedia())
			assert.Equal(t, dataURL, parts[0].GetImageMedia().Url)
		})
	}
}

func TestResponsesRequestToChatCompletionsRequestHoistsToolOutputMediaAfterToolBatch(t *testing.T) {
	const dataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	imageOutput := []any{map[string]any{"type": "input_image", "image_url": dataURL}}

	t.Run("parallel outputs stay contiguous", func(t *testing.T) {
		got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
			Model: "gpt-test",
			Input: mustRawMessage(t, []map[string]any{
				{"type": "function_call", "call_id": "call_1", "name": "screenshot", "arguments": "{}"},
				{"type": "function_call", "call_id": "call_2", "name": "read_file", "arguments": "{}"},
				{"type": "function_call_output", "call_id": "call_1", "output": imageOutput},
				{"type": "function_call_output", "call_id": "call_2", "output": "file contents"},
				{"role": "user", "content": "what do you see?"},
			}),
		})
		require.NoError(t, err)

		require.Len(t, got.Messages, 5)
		assert.Equal(t, "assistant", got.Messages[0].Role)
		assert.Len(t, got.Messages[0].ParseToolCalls(), 2)
		assert.Equal(t, dto.Message{Role: "tool", ToolCallId: "call_1", Content: "[image]"}, got.Messages[1])
		assert.Equal(t, dto.Message{Role: "tool", ToolCallId: "call_2", Content: "file contents"}, got.Messages[2])
		assert.Equal(t, "user", got.Messages[3].Role)
		parts := got.Messages[3].ParseContent()
		require.Len(t, parts, 1)
		assert.Equal(t, dto.ContentTypeImageURL, parts[0].Type)
		assert.Equal(t, dto.Message{Role: "user", Content: "what do you see?"}, got.Messages[4])
	})

	t.Run("trailing output flushes media at end of input", func(t *testing.T) {
		got, err := ResponsesRequestToChatCompletionsRequest(context.Background(), &dto.OpenAIResponsesRequest{
			Model: "gpt-test",
			Input: mustRawMessage(t, []map[string]any{
				{"type": "function_call", "call_id": "call_1", "name": "screenshot", "arguments": "{}"},
				{"type": "function_call_output", "call_id": "call_1", "output": imageOutput},
			}),
		})
		require.NoError(t, err)

		require.Len(t, got.Messages, 3)
		assert.Equal(t, dto.Message{Role: "tool", ToolCallId: "call_1", Content: "[image]"}, got.Messages[1])
		assert.Equal(t, "user", got.Messages[2].Role)
		parts := got.Messages[2].ParseContent()
		require.Len(t, parts, 1)
		require.NotNil(t, parts[0].GetImageMedia())
		assert.Equal(t, dataURL, parts[0].GetImageMedia().Url)
	})
}

func mustRawMessage(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := kitutil.Marshal(value)
	require.NoError(t, err)
	return raw
}
