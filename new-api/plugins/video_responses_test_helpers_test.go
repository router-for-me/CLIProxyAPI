package plugins_test

import (
	"sort"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type videoResponsesTestCase struct {
	pluginKey           string
	model               string
	requestBody         map[string]any
	wantAction          string
	wantRequest         map[string]any
	wantUsageKeys       []string
	wantSubmitUsageKeys []string
	wantVendorName      string
}

func testVideoResponsesProtocol(t *testing.T, testCase videoResponsesTestCase) {
	t.Helper()
	source, err := builtinplugins.Source(testCase.pluginKey)
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: testCase.pluginKey})
	require.NoError(t, err)

	t.Run("claims every model", func(t *testing.T) {
		for _, model := range plugin.Meta.Models {
			binding, found := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
			require.True(t, found, model)
			assert.Same(t, plugin, binding.Plugin)
			assert.Equal(t, "openai_responses", binding.Protocol)
		}
	})

	t.Run("declares documented usage facts", func(t *testing.T) {
		keys := make([]string, 0, len(plugin.Meta.UsageSchema))
		for key, schema := range plugin.Meta.UsageSchema {
			keys = append(keys, key)
			assert.NotEmpty(t, schema.Description, key)
		}
		sort.Strings(keys)
		expected := append([]string(nil), testCase.wantUsageKeys...)
		sort.Strings(expected)
		assert.Equal(t, expected, keys)
	})

	var resolved map[string]any
	t.Run("parses Responses input", func(t *testing.T) {
		value, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "decodeRequest"},
			map[string]any{"body": map[string]any{"kind": "json", "value": testCase.requestBody}, "model": testCase.model, "stream": false},
		)
		require.NoError(t, callErr)
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		require.NoError(t, common.Unmarshal(encoded, &resolved))

		assert.Equal(t, testCase.model, resolved["model"])
		assert.Equal(t, testCase.wantAction, resolved["action"])
		requestBody, ok := resolved["requestBody"].(map[string]any)
		require.True(t, ok)
		for key, expected := range testCase.wantRequest {
			assert.Equal(t, expected, requestBody[key], key)
		}
	})

	t.Run("rejects malformed Responses input", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "decodeRequest"},
			map[string]any{
				"body":  map[string]any{"kind": "json", "value": map[string]any{"model": testCase.model, "input": map[string]any{"text": "bad"}}},
				"model": testCase.model,
			},
		)
		require.ErrorContains(t, callErr, "input must be a string or array")
	})

	t.Run("extracts exactly the declared submit facts", func(t *testing.T) {
		requestBody, ok := resolved["requestBody"].(map[string]any)
		require.True(t, ok)
		value, callErr := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
			"model":         testCase.model,
			"upstreamModel": testCase.model,
			"action":        testCase.wantAction,
			"requestBody":   requestBody,
		})
		require.NoError(t, callErr)
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		var facts map[string]any
		require.NoError(t, common.Unmarshal(encoded, &facts))
		keys := make([]string, 0, len(facts))
		for key := range facts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		expected := append([]string(nil), testCase.wantSubmitUsageKeys...)
		if len(expected) == 0 {
			expected = append([]string(nil), testCase.wantUsageKeys...)
		}
		sort.Strings(expected)
		assert.Equal(t, expected, keys)
	})

	protocolContext := map[string]any{
		"requestBody": map[string]any{"model": testCase.model},
		"stream":      true,
		"artifacts": map[string]any{
			"video": map[string]any{
				"key":      "video",
				"type":     "video",
				"mimeType": "video/mp4",
				"url":      "https://gateway.example/v1/tasks/task_public/artifacts/video/content?access=host%2Bcapability%3D",
			},
		},
	}
	successTask := map[string]any{
		"task_id":    "task_public",
		"status":     "SUCCESS",
		"progress":   "100%",
		"created_at": 10,
		"updated_at": 20,
		"data": map[string]any{
			"url": "https://upstream.example/video.mp4?signature=must-not-leak",
		},
	}

	t.Run("renders stream state transitions", func(t *testing.T) {
		progressValue, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "renderEvents"},
			protocolContext,
			map[string]any{"task_id": "task_public", "status": "IN_PROGRESS", "progress": "42%"},
		)
		require.NoError(t, callErr)
		progress, decodeErr := relay.DecodePluginProtocolEventResult(progressValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, progress.Events, 1)
		require.NotNil(t, progress.Events[0].Progress)
		assert.Equal(t, float64(42), *progress.Events[0].Progress)

		duplicateValue, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "renderEvents"},
			protocolContext,
			map[string]any{"task_id": "task_public", "status": "IN_PROGRESS", "progress": "42%"},
			map[string]any{"status": "IN_PROGRESS", "progress": float64(42)},
		)
		require.NoError(t, callErr)
		duplicate, decodeErr := relay.DecodePluginProtocolEventResult(duplicateValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		assert.Empty(t, duplicate.Events)
		assert.False(t, duplicate.Done)

		failureValue, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "renderEvents"},
			protocolContext,
			map[string]any{"task_id": "task_public", "status": "FAILURE", "fail_reason": "blocked"},
		)
		require.NoError(t, callErr)
		failure, decodeErr := relay.DecodePluginProtocolEventResult(failureValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, failure.Events, 1)
		assert.Equal(t, "error", failure.Events[0].Type)
		assert.True(t, failure.Done)

		value, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "renderEvents"},
			protocolContext,
			successTask,
		)
		require.NoError(t, callErr)
		result, decodeErr := relay.DecodePluginProtocolEventResult(value, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, result.Events, 1)
		assert.Equal(t, "output", result.Events[0].Type)
		assert.True(t, result.Done)
		var text string
		require.NoError(t, common.Unmarshal(result.Events[0].Data, &text))
		assert.Contains(t, text, "gateway.example")
		assert.NotContains(t, text, "upstream.example")
	})

	t.Run("renders a valid final response", func(t *testing.T) {
		value, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "renderFinal"},
			protocolContext,
			successTask,
		)
		require.NoError(t, callErr)
		machine := relay.NewPluginResponsesMachine("task_public", testCase.model, 10, relay.DefaultPluginProtocolLimits())
		response, finalErr := machine.FinalResponse(value, "SUCCESS")
		require.NoError(t, finalErr)
		assert.Equal(t, "completed", response["status"])
		output, ok := response["output"].([]any)
		require.True(t, ok)
		require.Len(t, output, 1)
		item, ok := output[0].(map[string]any)
		require.True(t, ok)
		content, ok := item["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		part, ok := content[0].(map[string]any)
		require.True(t, ok)
		text, ok := part["text"].(string)
		require.True(t, ok)
		assert.Contains(t, text, "gateway.example")
		assert.NotContains(t, text, "upstream.example")
		metadata, ok := response["metadata"].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, testCase.wantVendorName, metadata["vendor"])
	})

	t.Run("requires a host artifact", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "renderFinal"},
			map[string]any{"requestBody": map[string]any{"model": testCase.model}},
			successTask,
		)
		require.ErrorContains(t, callErr, "video artifact is unavailable")
	})
}
