package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlibabaWan3(t *testing.T) {
	source, err := builtinplugins.Source("alibaba")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "alibaba"})
	require.NoError(t, err)

	roundTrip := func(t *testing.T, value any) map[string]any {
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		var decoded map[string]any
		require.NoError(t, common.Unmarshal(encoded, &decoded))
		return decoded
	}
	submitCtx := func(model, upstream string, body map[string]any) map[string]any {
		return map[string]any{"model": model, "upstreamModel": upstream, "baseUrl": "https://dashscope.aliyuncs.com", "apiKey": "k", "requestBody": body}
	}
	usageCtx := func(purpose string, body map[string]any) map[string]any {
		return map[string]any{"model": "wan3.0-video", "upstreamModel": "wan3.0-video", "usagePurpose": purpose, "requestBody": body}
	}
	decodeResponses := func(model string, body map[string]any) (map[string]any, error) {
		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{"model": model, "body": map[string]any{"kind": "json", "value": body}, "stream": false})
		if callErr != nil {
			return nil, callErr
		}
		return roundTrip(t, value), nil
	}

	t.Run("duration -1 becomes the auto_duration marker so the host accepts the body", func(t *testing.T) {
		resolved, callErr := decodeResponses("wan3.0-video", map[string]any{"model": "wan3.0-video", "input": "a cat", "duration": -1})
		require.NoError(t, callErr)
		requestBody := resolved["requestBody"].(map[string]any)
		assert.Equal(t, true, requestBody["auto_duration"])
		assert.NotContains(t, requestBody, "duration")

		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "wan3.0-video", "body": map[string]any{"kind": "json", "value": map[string]any{"model": "wan3.0-video", "prompt": "a cat", "seconds": -1}}})
		require.NoError(t, callErr)
		requestBody = roundTrip(t, value)["requestBody"].(map[string]any)
		assert.Equal(t, true, requestBody["auto_duration"])
		assert.NotContains(t, requestBody, "seconds")
		assert.NotContains(t, requestBody, "duration")

		value, callErr = plugin.Engine.CallPath(t.Context(), "native", []string{"createVideoTask"}, map[string]any{"body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "wan3.0-video", "input": map[string]any{"prompt": "a cat"}, "parameters": map[string]any{"duration": -1, "ratio": "16:9"},
		}}})
		require.NoError(t, callErr)
		requestBody = roundTrip(t, value)["requestBody"].(map[string]any)
		assert.Equal(t, true, requestBody["auto_duration"])
		assert.NotContains(t, requestBody, "duration")
		parameters := requestBody["metadata"].(map[string]any)["parameters"].(map[string]any)
		assert.Equal(t, "16:9", parameters["ratio"])
		assert.NotContains(t, parameters, "duration")
	})

	t.Run("auto_duration submits -1 upstream and bills 30 seconds up front", func(t *testing.T) {
		body := map[string]any{"model": "wan3.0-video", "prompt": "a cat", "auto_duration": true}
		value, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("wan3.0-video", "wan3.0-video", body))
		require.NoError(t, callErr)
		parameters := roundTrip(t, value)["body"].(map[string]any)["parameters"].(map[string]any)
		assert.Equal(t, float64(-1), parameters["duration"])

		value, callErr = plugin.Engine.Call(t.Context(), "extractUsage", usageCtx("facts", body))
		require.NoError(t, callErr)
		assert.Equal(t, map[string]any{"seconds": float64(30), "resolution": "1080P"}, roundTrip(t, value))

		value, callErr = plugin.Engine.Call(t.Context(), "extractUsage", usageCtx("billing_ratios", body))
		require.NoError(t, callErr)
		assert.Equal(t, float64(30), roundTrip(t, value)["seconds"])

		_, callErr = plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("wan2.7-t2v", "wan2.7-t2v", map[string]any{"model": "wan2.7-t2v", "prompt": "a cat", "auto_duration": true}))
		require.ErrorContains(t, callErr, "only supported by wan3.0")
	})

	t.Run("channel-mapped alias resolves defaults from the upstream model", func(t *testing.T) {
		direct, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("wan3.0-video", "wan3.0-video", map[string]any{"model": "wan3.0-video", "prompt": "a cat"}))
		require.NoError(t, callErr)
		alias, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("my-wan3", "wan3.0-video", map[string]any{"model": "my-wan3", "prompt": "a cat"}))
		require.NoError(t, callErr)
		assert.Equal(t, roundTrip(t, direct)["body"], roundTrip(t, alias)["body"])
		assert.Equal(t, "1080P", roundTrip(t, alias)["body"].(map[string]any)["parameters"].(map[string]any)["resolution"])
	})

	t.Run("size maps to a resolution tier and unknown sizes are rejected", func(t *testing.T) {
		value, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("wan3.0-video", "wan3.0-video", map[string]any{"model": "wan3.0-video", "prompt": "a cat", "size": "1280*720"}))
		require.NoError(t, callErr)
		parameters := roundTrip(t, value)["body"].(map[string]any)["parameters"].(map[string]any)
		assert.Equal(t, "720P", parameters["resolution"])
		assert.NotContains(t, parameters, "size")
		assert.Equal(t, "16:9", parameters["ratio"])

		_, callErr = plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("wan3.0-video", "wan3.0-video", map[string]any{"model": "wan3.0-video", "prompt": "a cat", "size": "1000*1000"}))
		require.ErrorContains(t, callErr, "invalid size")
		_, callErr = plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("wan3.0-video", "wan3.0-video", map[string]any{"model": "wan3.0-video", "prompt": "a cat", "duration": 31}))
		require.ErrorContains(t, callErr, "between 2 and 30")
	})

	t.Run("image-only input stays rejected for t2v models and accepted for wan3.0", func(t *testing.T) {
		imageOnly := []any{map[string]any{"type": "input_image", "image_url": "https://cdn.example/first.png"}}
		_, callErr := decodeResponses("wan2.7-t2v", map[string]any{"model": "wan2.7-t2v", "input": imageOnly})
		require.ErrorContains(t, callErr, "input is required")

		resolved, callErr := decodeResponses("wan3.0-video", map[string]any{"model": "wan3.0-video", "input": imageOnly})
		require.NoError(t, callErr)
		assert.Equal(t, "image_to_video", resolved["action"])

		value, callErr := plugin.Engine.Call(t.Context(), "buildSubmitRequest", submitCtx("wan3.0-video", "wan3.0-video", map[string]any{"model": "wan3.0-video", "prompt": "", "images": []any{"https://cdn.example/first.png"}}))
		require.NoError(t, callErr)
		input := roundTrip(t, value)["body"].(map[string]any)["input"].(map[string]any)
		assert.Equal(t, []any{map[string]any{"type": "first_frame", "url": "https://cdn.example/first.png"}}, input["media"])
		assert.NotContains(t, input, "img_url")
	})

	t.Run("completion facts read the wan3.0 usage block", func(t *testing.T) {
		value, callErr := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{}, map[string]any{}, map[string]any{
			"output": map[string]any{"task_status": "SUCCEEDED", "video_url": "https://upstream.example/v.mp4"},
			"usage":  map[string]any{"video_count": 1, "duration": 7.5, "output_video_duration": 7.5, "SR": 720, "ratio": "16:9"},
		})
		require.NoError(t, callErr)
		assert.Equal(t, map[string]any{"seconds": 7.5, "resolution": "720P"}, roundTrip(t, value))

		value, callErr = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", map[string]any{}, map[string]any{}, map[string]any{
			"output": map[string]any{"task_status": "SUCCEEDED", "duration": 5, "resolution": "1080p"},
		})
		require.NoError(t, callErr)
		assert.Equal(t, map[string]any{"seconds": float64(5), "resolution": "1080P"}, roundTrip(t, value))
	})
}
