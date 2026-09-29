package relay

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodePluginProtocolEventResultPreservesStatePresence(t *testing.T) {
	tests := []struct {
		name        string
		value       map[string]any
		wantPresent bool
		wantNull    bool
		wantValue   string
	}{
		{
			name:        "omitted",
			value:       map[string]any{"events": []any{}, "done": false},
			wantPresent: false,
		},
		{
			name:        "explicit null",
			value:       map[string]any{"events": []any{}, "state": nil, "done": false},
			wantPresent: true,
			wantNull:    true,
			wantValue:   "null",
		},
		{
			name:        "json value",
			value:       map[string]any{"events": []any{}, "state": map[string]any{"seen": 1}, "done": false},
			wantPresent: true,
			wantValue:   `{"seen":1}`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := DecodePluginProtocolEventResult(testCase.value, PluginProtocolLimits{})
			require.NoError(t, err)
			assert.Equal(t, testCase.wantPresent, result.State.Present)
			assert.Equal(t, testCase.wantNull, result.State.Null)
			if testCase.wantPresent {
				assert.JSONEq(t, testCase.wantValue, string(result.State.Value))
			} else {
				assert.Empty(t, result.State.Value)
			}

			if result.State.Present {
				_, err = result.State.PluginValue()
				require.NoError(t, err)
			}
		})
	}
}

func TestDecodePluginProtocolEventResultValidatesSemanticContract(t *testing.T) {
	result, err := DecodePluginProtocolEventResult(map[string]any{
		"events": []any{
			map[string]any{"type": "progress", "progress": 42.5, "message": "working"},
			map[string]any{"type": "output", "data": map[string]any{"url": "https://example.invalid/video.mp4"}},
			map[string]any{"type": "error", "code": "provider_failed", "message": "not exposed"},
		},
		"done": true,
	}, PluginProtocolLimits{})
	require.NoError(t, err)
	require.Len(t, result.Events, 3)
	assert.Equal(t, "progress", result.Events[0].Type)
	assert.Equal(t, 42.5, *result.Events[0].Progress)
	assert.Equal(t, "working", *result.Events[0].Message)
	assert.JSONEq(t, `{"url":"https://example.invalid/video.mp4"}`, string(result.Events[1].Data))
	assert.Equal(t, "provider_failed", *result.Events[2].Code)
	assert.True(t, result.Done)

	invalid := []struct {
		name  string
		value any
	}{
		{
			name:  "unknown top-level field",
			value: map[string]any{"events": []any{}, "done": false, "sequence_number": 99},
		},
		{
			name:  "unknown event field",
			value: map[string]any{"events": []any{map[string]any{"type": "output", "data": "x", "id": "plugin-id"}}, "done": false},
		},
		{
			name:  "progress outside range",
			value: map[string]any{"events": []any{map[string]any{"type": "progress", "progress": 101}}, "done": false},
		},
		{
			name:  "missing output data",
			value: map[string]any{"events": []any{map[string]any{"type": "output"}}, "done": false},
		},
		{
			name:  "empty error message",
			value: map[string]any{"events": []any{map[string]any{"type": "error", "message": " "}}, "done": false},
		},
		{
			name:  "unsupported event",
			value: map[string]any{"events": []any{map[string]any{"type": "response.completed"}}, "done": false},
		},
		{
			name:  "non-json number",
			value: map[string]any{"events": []any{}, "done": false, "state": math.Inf(1)},
		},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := DecodePluginProtocolEventResult(testCase.value, PluginProtocolLimits{})
			require.Error(t, err)
		})
	}
}

func TestDecodePluginProtocolEventResultEnforcesBounds(t *testing.T) {
	limits := DefaultPluginProtocolLimits()
	limits.MaxEventsPerTick = 1
	_, err := DecodePluginProtocolEventResult(map[string]any{
		"events": []any{
			map[string]any{"type": "progress"},
			map[string]any{"type": "progress"},
		},
		"done": false,
	}, limits)
	require.ErrorContains(t, err, "exceed limit")

	limits = DefaultPluginProtocolLimits()
	limits.MaxEventBytes = 32
	_, err = DecodePluginProtocolEventResult(map[string]any{
		"events": []any{map[string]any{"type": "output", "data": strings.Repeat("x", 64)}},
		"done":   false,
	}, limits)
	require.ErrorContains(t, err, "protocol event exceeds")

	limits = DefaultPluginProtocolLimits()
	limits.MaxStateDepth = 3
	_, err = DecodePluginProtocolEventResult(map[string]any{
		"events": []any{},
		"state":  map[string]any{"one": map[string]any{"two": map[string]any{"three": true}}},
		"done":   false,
	}, limits)
	require.ErrorContains(t, err, "depth limit")

	limits = DefaultPluginProtocolLimits()
	_, err = DecodePluginProtocolEventResult(map[string]any{
		"events": []any{map[string]any{
			"type":    "progress",
			"message": strings.Repeat("x", limits.MaxMetadataValueBytes+1),
		}},
		"done": false,
	}, limits)
	require.ErrorContains(t, err, "progress event message exceeds")
}

func TestPluginResponsesMachineOwnsIDsSequenceAndLifecycle(t *testing.T) {
	machine := NewPluginResponsesMachine("task_public", "video-model", 1710000000, PluginProtocolLimits{})
	created, err := machine.CreatedEvent()
	require.NoError(t, err)
	assert.Equal(t, "response.created", created.Type)
	assert.Equal(t, 0, created.SequenceNumber)
	require.NotNil(t, created.Response)
	assert.Equal(t, "resp_public", created.Response.ID)
	assert.Equal(t, "in_progress", created.Response.Status)
	assert.Equal(t, "task_public", created.Response.Metadata["task_id"])
	assert.Equal(t, "/v1/responses/resp_public", created.Response.Metadata["retrieval_path"])

	result, err := DecodePluginProtocolEventResult(map[string]any{
		"events": []any{
			map[string]any{"type": "progress", "progress": 25, "message": "rendering"},
			map[string]any{"type": "output", "data": map[string]any{"url": "https://cdn.invalid/v.mp4"}},
		},
		"done": false,
	}, PluginProtocolLimits{})
	require.NoError(t, err)
	events, err := machine.ApplyTick(result, "SUCCESS")
	require.NoError(t, err)
	require.Len(t, events, 8)
	for index, event := range events {
		assert.Equal(t, index+1, event.SequenceNumber)
	}
	assert.Equal(t, "response.in_progress", events[0].Type)
	assert.Equal(t, "25", events[0].Response.Metadata["task_progress"])
	assert.Equal(t, "rendering", events[0].Response.Metadata["task_message"])

	itemAdded := events[1]
	assert.Equal(t, "response.output_item.added", itemAdded.Type)
	assert.Equal(t, 0, *itemAdded.OutputIndex)
	assert.Equal(t, "msg_task_public_0", itemAdded.Item.ID)
	assert.Empty(t, itemAdded.Item.Content)

	partAdded := events[2]
	assert.Equal(t, "response.content_part.added", partAdded.Type)
	assert.Equal(t, "msg_task_public_0", partAdded.ItemID)
	assert.Equal(t, "content_task_public_0", partAdded.Part.ID)

	delta := events[3]
	assert.Equal(t, "response.output_text.delta", delta.Type)
	assert.Equal(t, `{"url":"https://cdn.invalid/v.mp4"}`, *delta.Delta)
	require.NotNil(t, delta.Logprobs)
	assert.Empty(t, *delta.Logprobs)

	assert.Equal(t, "response.output_text.done", events[4].Type)
	assert.Equal(t, "response.content_part.done", events[5].Type)
	assert.Equal(t, "response.output_item.done", events[6].Type)
	completed := events[7]
	assert.Equal(t, "response.completed", completed.Type)
	assert.Equal(t, "completed", completed.Response.Status)
	assert.Nil(t, completed.Response.Error)
	require.NotNil(t, completed.Response.Usage)
	assert.Zero(t, completed.Response.Usage.InputTokens)
	assert.Zero(t, completed.Response.Usage.OutputTokens)
	assert.Zero(t, completed.Response.Usage.TotalTokens)
	require.Len(t, completed.Response.Output, 1)
	assert.Equal(t, "completed", completed.Response.Output[0].Status)

	encoded, err := common.Marshal(completed)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}`)

	encoded, err = common.Marshal(delta)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"logprobs":[]`)
	assert.NotContains(t, string(encoded), `"response"`)

	_, err = machine.ApplyTick(ProtocolEventResult{}, "SUCCESS")
	require.ErrorContains(t, err, "already terminal")
}

func TestPluginResponsesMachineUsesVerbatimStringOutput(t *testing.T) {
	machine := NewPluginResponsesMachine("task_text", "model", 1, PluginProtocolLimits{})
	_, err := machine.CreatedEvent()
	require.NoError(t, err)
	result, err := DecodePluginProtocolEventResult(map[string]any{
		"events": []any{map[string]any{"type": "output", "data": "verbatim\ntext"}},
		"done":   true,
	}, PluginProtocolLimits{})
	require.NoError(t, err)
	events, err := machine.ApplyTick(result, "IN_PROGRESS")
	require.NoError(t, err)
	require.Len(t, events, 7)
	assert.Equal(t, "verbatim\ntext", *events[2].Delta)
	assert.Equal(t, "response.incomplete", events[6].Type)
	assert.Nil(t, events[6].Response.IncompleteDetails)
	require.NotNil(t, events[6].Response.Usage)
}

func TestPluginResponsesMachineSanitizesFailure(t *testing.T) {
	t.Run("plugin semantic error", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_error", "model", 1, PluginProtocolLimits{})
		_, err := machine.CreatedEvent()
		require.NoError(t, err)
		result, err := DecodePluginProtocolEventResult(map[string]any{
			"events": []any{map[string]any{
				"type":    "error",
				"code":    "upstream_secret_code",
				"message": "https://secret.invalid/?credential=hidden",
			}},
			"done": true,
		}, PluginProtocolLimits{})
		require.NoError(t, err)
		events, err := machine.ApplyTick(result, "IN_PROGRESS")
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.NotNil(t, events[0].Response.Error)
		assert.Equal(t, "server_error", events[0].Response.Error.Code)
		assert.Equal(t, "The task failed.", events[0].Response.Error.Message)
		encoded, marshalErr := common.Marshal(events[0])
		require.NoError(t, marshalErr)
		assert.NotContains(t, string(encoded), "secret")
	})

	t.Run("database failure ignores plugin output", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_db_error", "model", 1, PluginProtocolLimits{})
		_, err := machine.CreatedEvent()
		require.NoError(t, err)
		result := ProtocolEventResult{Events: []ProtocolSemanticEvent{
			{Type: "output", Data: json.RawMessage(`"must not leak"`)},
		}}
		events, err := machine.ApplyTick(result, "FAILURE")
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "response.failed", events[0].Type)
		assert.Empty(t, events[0].Response.Output)
		encoded, marshalErr := common.Marshal(events[0])
		require.NoError(t, marshalErr)
		assert.NotContains(t, string(encoded), "must not leak")
	})

	t.Run("host observation error", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_host_error", "model", 1, PluginProtocolLimits{})
		_, err := machine.CreatedEvent()
		require.NoError(t, err)
		failed, err := machine.FailureEvent("SUCCESS")
		require.NoError(t, err)
		assert.Equal(t, 1, failed.SequenceNumber)
		assert.Equal(t, "server_error", failed.Response.Error.Code)
		assert.Equal(t, "The task could not be observed.", failed.Response.Error.Message)
		assert.Equal(t, "failed", failed.Response.Status)
		assert.Equal(t, "completed", failed.Response.Metadata["task_status"])
	})
}

func TestPluginResponsesMachineNonStreamShapes(t *testing.T) {
	t.Run("completed canonicalizes renderFinal response", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_final", "model", 99, PluginProtocolLimits{})
		response, err := machine.FinalResponse(map[string]any{
			"id":         "plugin-controlled-id",
			"object":     "plugin-controlled-object",
			"created_at": -1,
			"status":     "plugin-controlled-status",
			"model":      "plugin-controlled-model",
			"error":      map[string]any{"message": "plugin-controlled-error"},
			"metadata": map[string]any{
				"plugin_field": "kept",
				"task_id":      "plugin-controlled-task",
			},
			"output": []any{
				map[string]any{
					"id":     "plugin-controlled-item",
					"type":   "message",
					"status": "plugin-controlled-item-status",
					"role":   "assistant",
					"content": []any{
						map[string]any{
							"id":          "plugin-controlled-content",
							"type":        "output_text",
							"text":        "hello",
							"annotations": []any{},
							"logprobs":    []any{},
						},
					},
				},
			},
			"custom_field": map[string]any{"kept": true},
		}, "SUCCESS")
		require.NoError(t, err)
		assert.Equal(t, "resp_final", response["id"])
		assert.Equal(t, "response", response["object"])
		assert.Equal(t, int64(99), response["created_at"])
		assert.Equal(t, "completed", response["status"])
		assert.Equal(t, "model", response["model"])
		assert.Nil(t, response["error"])
		assert.Nil(t, response["incomplete_details"])
		assert.Nil(t, response["instructions"])
		assert.Equal(t, true, response["parallel_tool_calls"])
		assert.Equal(t, float64(1), response["temperature"])
		assert.Equal(t, "auto", response["tool_choice"])
		assert.Empty(t, response["tools"])
		assert.Equal(t, float64(1), response["top_p"])
		assert.Equal(t, map[string]any{"kept": true}, response["custom_field"])

		metadata, ok := response["metadata"].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "kept", metadata["plugin_field"])
		assert.Equal(t, "task_final", metadata["task_id"])
		assert.Equal(t, "completed", metadata["task_status"])

		output, ok := response["output"].([]any)
		require.True(t, ok)
		require.Len(t, output, 1)
		item, ok := output[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "item_task_final_0", item["id"])
		assert.Equal(t, "completed", item["status"])
		content, ok := item["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		part, ok := content[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "content_task_final_0_0", part["id"])
		assert.Equal(t, "hello", part["text"])
	})

	t.Run("failed is generic", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_failed", "model", 99, PluginProtocolLimits{})
		response, err := machine.FinalResponse(map[string]any{"secret": "ignored"}, "FAILURE")
		require.NoError(t, err)
		assert.Equal(t, "failed", response["status"])
		assert.Empty(t, response["output"])
		responseError, ok := response["error"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "server_error", responseError["code"])
		encoded, marshalErr := common.Marshal(response)
		require.NoError(t, marshalErr)
		assert.NotContains(t, string(encoded), "secret")
	})

	t.Run("invalid output union is rejected", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_invalid", "model", 99, PluginProtocolLimits{})
		_, err := machine.FinalResponse(map[string]any{
			"output": []any{map[string]any{}},
		}, "SUCCESS")
		require.ErrorContains(t, err, "message objects")
	})

	t.Run("poll timeout stays queued with retrieval path", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_wait", "model", 99, PluginProtocolLimits{})
		response, err := machine.TimeoutResponse("QUEUED")
		require.NoError(t, err)
		assert.Equal(t, "queued", response.Status)
		assert.Nil(t, response.Error)
		assert.Nil(t, response.IncompleteDetails)
		assert.Equal(t, "queued", response.Metadata["task_status"])
		assert.Equal(t, "/v1/responses/resp_wait", response.Metadata["retrieval_path"])
	})

	t.Run("host failure preserves last persisted status", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_observe", "model", 99, PluginProtocolLimits{})
		response, err := machine.FailureResponse("IN_PROGRESS")
		require.NoError(t, err)
		assert.Equal(t, "failed", response.Status)
		assert.Equal(t, "in_progress", response.Metadata["task_status"])
	})

	t.Run("unknown persisted status stays in documented vocabulary", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_unknown", "model", 99, PluginProtocolLimits{})
		response, err := machine.TimeoutResponse("UNKNOWN_PROVIDER_STATE")
		require.NoError(t, err)
		assert.Equal(t, "queued", response.Metadata["task_status"])
	})
}

func TestPluginResponsesMachinePendingResponse(t *testing.T) {
	tests := []struct {
		name           string
		taskStatus     string
		background     bool
		wantStatus     string
		wantBackground bool
	}{
		{name: "queued from submitted", taskStatus: "SUBMITTED", wantStatus: "queued"},
		{name: "queued from not start", taskStatus: "NOT_START", wantStatus: "queued"},
		{name: "in progress", taskStatus: "IN_PROGRESS", wantStatus: "in_progress"},
		{name: "background flag", taskStatus: "QUEUED", background: true, wantStatus: "queued", wantBackground: true},
		{name: "terminal input stays pending", taskStatus: "SUCCESS", wantStatus: "in_progress"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			machine := NewPluginResponsesMachine("task_pending", "video-model", 1_710_000_000, PluginProtocolLimits{})
			machine.SetBackground(testCase.background)
			response := machine.PendingResponse(testCase.taskStatus)
			assert.Equal(t, "resp_pending", response["id"])
			assert.Equal(t, "response", response["object"])
			assert.Equal(t, int64(1_710_000_000), response["created_at"])
			assert.Equal(t, testCase.wantStatus, response["status"])
			assert.Equal(t, testCase.wantBackground, response["background"])
			assert.Nil(t, response["completed_at"])
			assert.Nil(t, response["error"])
			assert.Nil(t, response["incomplete_details"])
			assert.Equal(t, "video-model", response["model"])
			assert.Empty(t, response["output"])
			assert.Nil(t, response["usage"])
			metadata, ok := response["metadata"].(map[string]string)
			require.True(t, ok)
			assert.Equal(t, map[string]string{
				"task_id":        "task_pending",
				"task_status":    testCase.wantStatus,
				"retrieval_path": "/v1/responses/resp_pending",
			}, metadata)

			encoded, err := common.Marshal(response)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"completed_at":null`)
			assert.Contains(t, string(encoded), `"output":[]`)
			assert.Contains(t, string(encoded), `"usage":null`)
		})
	}
}

func TestPluginResponsesMachineProgressDoesNotRepeatAccumulatedOutput(t *testing.T) {
	machine := NewPluginResponsesMachine("task_progress", "model", 1, PluginProtocolLimits{})
	_, err := machine.CreatedEvent()
	require.NoError(t, err)
	events, err := machine.ApplyTick(ProtocolEventResult{Events: []ProtocolSemanticEvent{
		{Type: "output", Data: json.RawMessage(`"large-output"`)},
	}}, "IN_PROGRESS")
	require.NoError(t, err)
	require.Len(t, events, 6)

	message := "still working"
	events, err = machine.ApplyTick(ProtocolEventResult{Events: []ProtocolSemanticEvent{
		{Type: "progress", Message: &message},
	}}, "IN_PROGRESS")
	require.NoError(t, err)
	assert.Empty(t, events)

	events, err = machine.ApplyTick(ProtocolEventResult{}, "SUCCESS")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Response)
	require.Len(t, events[0].Response.Output, 1)
	assert.Equal(t, "still working", events[0].Response.Metadata["task_message"])
}

func TestPluginResponsesMachineEnforcesCumulativeOutputLimit(t *testing.T) {
	limits := DefaultPluginProtocolLimits()
	limits.MaxOutputs = 1
	machine := NewPluginResponsesMachine("task_bounded", "model", 1, limits)
	_, err := machine.CreatedEvent()
	require.NoError(t, err)

	first := ProtocolEventResult{Events: []ProtocolSemanticEvent{
		{Type: "output", Data: json.RawMessage(`"first"`)},
	}}
	_, err = machine.ApplyTick(first, "IN_PROGRESS")
	require.NoError(t, err)

	second := ProtocolEventResult{Events: []ProtocolSemanticEvent{
		{Type: "output", Data: json.RawMessage(`"second"`)},
	}}
	_, err = machine.ApplyTick(second, "IN_PROGRESS")
	require.ErrorContains(t, err, "outputs exceed limit")
}

func TestPluginResponsesMachineFinalFromEvents(t *testing.T) {
	t.Run("output event becomes completed response", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_from_events", "model", 99, PluginProtocolLimits{})
		result, err := DecodePluginProtocolEventResult(map[string]any{
			"events": []any{map[string]any{"type": "output", "data": "synthesized-output"}},
			"done":   true,
		}, PluginProtocolLimits{})
		require.NoError(t, err)

		response, err := machine.FinalFromEvents(result, "SUCCESS")
		require.NoError(t, err)
		assert.Equal(t, "completed", response["status"])
		output, ok := response["output"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, output)
	})

	t.Run("error event leaves receiver unstarted", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_from_events_fail", "model", 99, PluginProtocolLimits{})
		result, err := DecodePluginProtocolEventResult(map[string]any{
			"events": []any{map[string]any{"type": "error", "message": "provider failed"}},
			"done":   true,
		}, PluginProtocolLimits{})
		require.NoError(t, err)

		_, err = machine.FinalFromEvents(result, "SUCCESS")
		require.ErrorContains(t, err, "reported failure")

		response, failErr := machine.FailureResponse("FAILURE")
		require.NoError(t, failErr)
		require.NotNil(t, response)
		assert.Equal(t, "failed", response.Status)
	})

	t.Run("started machine is rejected", func(t *testing.T) {
		machine := NewPluginResponsesMachine("task_from_events_started", "model", 99, PluginProtocolLimits{})
		_, err := machine.CreatedEvent()
		require.NoError(t, err)
		_, err = machine.FinalFromEvents(ProtocolEventResult{}, "SUCCESS")
		require.ErrorContains(t, err, "already started")
	})
}
