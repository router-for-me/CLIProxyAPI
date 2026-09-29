package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVertexAIResponsesProtocol(t *testing.T) {
	source, err := builtinplugins.Source("vertex-ai")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "vertex-ai"})
	require.NoError(t, err)

	t.Run("claims every model", func(t *testing.T) {
		for _, model := range plugin.Meta.Models {
			binding, found := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
			require.True(t, found, model)
			assert.Same(t, plugin, binding.Plugin)
			assert.Equal(t, "openai_responses", binding.Protocol)
		}
	})

	t.Run("shares models with Gemini without losing either provider", func(t *testing.T) {
		candidates := jsplugin.DefaultRegistry.Generation().LookupEndpointCandidates("POST", "/v1/responses", "veo-3.0-generate-001")
		require.Len(t, candidates, 2)
		assert.Equal(t, "google", candidates[0].Plugin.Meta.Key)
		assert.Equal(t, "vertex-ai", candidates[1].Plugin.Meta.Key)

		request := map[string]any{"model": "veo-3.0-generate-001", "body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "veo-3.0-generate-001", "input": "waves", "seconds": 8, "size": "1280x720",
		}}}
		first, callErr := candidates[0].Plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, request)
		require.NoError(t, callErr)
		second, callErr := candidates[1].Plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, request)
		require.NoError(t, callErr)
		assert.Equal(t, decodePluginValue(t, first), decodePluginValue(t, second))
	})

	t.Run("declares documented usage facts", func(t *testing.T) {
		require.Len(t, plugin.Meta.UsageSchema, 3)
		for _, key := range []string{"seconds", "resolution", "generate_audio"} {
			schema, exists := plugin.Meta.UsageSchema[key]
			require.True(t, exists, key)
			assert.NotEmpty(t, schema.Description, key)
		}
		assert.Equal(t, []string{"720p", "1080p", "4k"}, plugin.Meta.UsageSchema["resolution"].Enum)
		assert.Equal(t, "boolean", plugin.Meta.UsageSchema["generate_audio"].Type)
	})

	callProtocol := func(t *testing.T, hook string, args ...any) any {
		t.Helper()
		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", hook}, args...)
		require.NoError(t, callErr)
		return value
	}
	decodeMap := func(t *testing.T, value any) map[string]any {
		t.Helper()
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		var decoded map[string]any
		require.NoError(t, common.Unmarshal(encoded, &decoded))
		return decoded
	}

	requestBody := map[string]any{
		"model": "veo-3.1-fast-generate-preview",
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "animate this frame"},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,aGVsbG8="},
		}}},
		"seconds":    8,
		"size":       "1920x1080",
		"resolution": "1080P",
	}

	t.Run("parses Responses input", func(t *testing.T) {
		resolved := decodeMap(t, callProtocol(t, "decodeRequest", map[string]any{"model": requestBody["model"], "body": map[string]any{"kind": "json", "value": requestBody}}))
		assert.Equal(t, "veo-3.1-fast-generate-preview", resolved["model"])
		assert.Equal(t, "image_to_video", resolved["action"])
		assert.Equal(t, map[string]any{
			"model":    "veo-3.1-fast-generate-preview",
			"prompt":   "animate this frame",
			"images":   []any{"data:image/png;base64,aGVsbG8="},
			"duration": float64(8),
			"size":     "1920x1080",
			"metadata": map[string]any{"resolution": "1080P"},
		}, resolved["requestBody"])
	})

	t.Run("rejects malformed input", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "veo-3.0-generate-001", "body": map[string]any{"kind": "json", "value": map[string]any{"model": "veo-3.0-generate-001", "input": map[string]any{"text": "bad"}}},
		})
		require.ErrorContains(t, callErr, "input must be a string or array")

		_, callErr = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "veo-3.0-generate-001", "body": map[string]any{"kind": "json", "value": map[string]any{"model": "veo-3.0-generate-001", "input": []any{map[string]any{"type": "input_image", "image_url": "https://example.com/frame.png"}}}},
		})
		require.ErrorContains(t, callErr, "input image must be a data URL or base64 value")
	})

	t.Run("extracts schema-declared usage", func(t *testing.T) {
		value, callErr := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
			"requestBody":  map[string]any{"duration": 8, "size": "1920x1080", "metadata": map[string]any{}},
			"usagePurpose": "facts",
		})
		require.NoError(t, callErr)
		assert.Equal(t, map[string]any{"seconds": int64(8), "resolution": "1080p", "generate_audio": true}, value)

		value, callErr = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", nil, map[string]any{}, map[string]any{
			"response": map[string]any{"videos": []any{map[string]any{"durationSeconds": 7, "resolution": "4K"}}},
		})
		require.NoError(t, callErr)
		assert.Nil(t, value)
	})

	protocolContext := map[string]any{
		"requestBody": map[string]any{"model": "veo-3.1-fast-generate-preview", "duration": 8},
		"stream":      true,
	}
	successTask := map[string]any{
		"task_id": "task-public", "status": "SUCCESS", "progress": "100%", "created_at": 10, "updated_at": 20,
		"data": map[string]any{"url": "data:video/mp4;base64,MUST_NOT_LEAK"},
	}

	t.Run("renders stream state transitions", func(t *testing.T) {
		progressValue := callProtocol(t, "renderEvents", protocolContext, map[string]any{"status": "IN_PROGRESS", "progress": "42%"})
		progress, decodeErr := relay.DecodePluginProtocolEventResult(progressValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, progress.Events, 1)
		require.NotNil(t, progress.Events[0].Progress)
		assert.Equal(t, float64(42), *progress.Events[0].Progress)

		duplicateValue := callProtocol(t, "renderEvents", protocolContext, map[string]any{"status": "IN_PROGRESS", "progress": "42%"}, map[string]any{"status": "IN_PROGRESS", "progress": float64(42)})
		duplicate, decodeErr := relay.DecodePluginProtocolEventResult(duplicateValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		assert.Empty(t, duplicate.Events)
		assert.False(t, duplicate.Done)

		failureValue := callProtocol(t, "renderEvents", protocolContext, map[string]any{"status": "FAILURE", "fail_reason": "blocked"})
		failure, decodeErr := relay.DecodePluginProtocolEventResult(failureValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, failure.Events, 1)
		assert.Equal(t, "error", failure.Events[0].Type)
		assert.True(t, failure.Done)

		successValue := callProtocol(t, "renderEvents", protocolContext, successTask)
		success, decodeErr := relay.DecodePluginProtocolEventResult(successValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, success.Events, 1)
		assert.True(t, success.Done)
		var text string
		require.NoError(t, common.Unmarshal(success.Events[0].Data, &text))
		assert.Contains(t, text, "veo-3.1-fast-generate-preview")
		assert.Contains(t, text, "8 seconds")
		assert.Contains(t, text, "/v1/videos")
		assert.NotContains(t, text, "MUST_NOT_LEAK")
		assert.NotContains(t, text, "<video")
	})

	t.Run("renders a degraded final response without an artifact", func(t *testing.T) {
		value := callProtocol(t, "renderFinal", protocolContext, successTask)
		machine := relay.NewPluginResponsesMachine("task-public", "veo-3.1-fast-generate-preview", 10, relay.DefaultPluginProtocolLimits())
		response, finalErr := machine.FinalResponse(value, "SUCCESS")
		require.NoError(t, finalErr)
		assert.Equal(t, "completed", response["status"])
		output, ok := response["output"].([]any)
		require.True(t, ok)
		require.Len(t, output, 1)
		message, ok := output[0].(map[string]any)
		require.True(t, ok)
		content, ok := message["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		part, ok := content[0].(map[string]any)
		require.True(t, ok)
		text, ok := part["text"].(string)
		require.True(t, ok)
		assert.Contains(t, text, "/v1/videos")
		assert.NotContains(t, text, "MUST_NOT_LEAK")
		assert.NotContains(t, text, "<video")
		metadata, ok := response["metadata"].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "vertex", metadata["vendor"])
		assert.Equal(t, "native_videos", metadata["artifact_mode"])
	})
}

func decodePluginValue(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}
