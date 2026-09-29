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

func TestSunoResponsesProtocol(t *testing.T) {
	source, err := builtinplugins.Source("sunoapi")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "sunoapi"})
	require.NoError(t, err)

	t.Run("claims both Suno models", func(t *testing.T) {
		for _, model := range plugin.Meta.Models {
			binding, found := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
			require.True(t, found, model)
			assert.Same(t, plugin, binding.Plugin)
			assert.Equal(t, "openai_responses", binding.Protocol)
		}
	})

	t.Run("declares documented usage facts", func(t *testing.T) {
		require.Len(t, plugin.Meta.UsageSchema, 2)
		for _, key := range []string{"clips", "action"} {
			schema, exists := plugin.Meta.UsageSchema[key]
			require.True(t, exists, key)
			assert.NotEmpty(t, schema.Description, key)
		}
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

	t.Run("parses music and lyrics requests", func(t *testing.T) {
		music := decodeMap(t, callProtocol(t, "decodeRequest", map[string]any{"model": "suno_music", "body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "suno_music", "input": "summer pop", "metadata": map[string]any{"title": "Sunset"},
		}}}))
		assert.Equal(t, "suno_music", music["model"])
		assert.Equal(t, "MUSIC", music["action"])
		assert.Equal(t, map[string]any{"gpt_description_prompt": "summer pop", "title": "Sunset"}, music["requestBody"])

		lyrics := decodeMap(t, callProtocol(t, "decodeRequest", map[string]any{"model": "suno_lyrics", "body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "suno_lyrics", "input": "write about the sea",
		}}}))
		assert.Equal(t, "suno_lyrics", lyrics["model"])
		assert.Equal(t, "LYRICS", lyrics["action"])
		assert.Equal(t, map[string]any{"prompt": "write about the sea"}, lyrics["requestBody"])
	})

	t.Run("rejects malformed input", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "suno_music", "body": map[string]any{"kind": "json", "value": map[string]any{"model": "suno_music", "input": map[string]any{"text": "bad"}}},
		})
		require.ErrorContains(t, callErr, "input must be a string or array")
	})

	t.Run("echoes a channel-mapped alias", func(t *testing.T) {
		music := decodeMap(t, callProtocol(t, "decodeRequest", map[string]any{
			"model": "my-suno", "upstreamModel": "suno_music", "stream": false,
			"body": map[string]any{"kind": "json", "value": map[string]any{"model": "my-suno", "input": "summer pop"}},
		}))
		assert.Equal(t, "my-suno", music["model"])
		assert.Equal(t, "MUSIC", music["action"])

		lyrics := decodeMap(t, callProtocol(t, "decodeRequest", map[string]any{
			"model": "my-suno", "upstreamModel": "suno_lyrics", "stream": false,
			"body": map[string]any{"kind": "json", "value": map[string]any{"model": "my-suno", "input": "write about the sea"}},
		}))
		assert.Equal(t, "my-suno", lyrics["model"])
		assert.Equal(t, "LYRICS", lyrics["action"])
		assert.Equal(t, map[string]any{"prompt": "write about the sea"}, lyrics["requestBody"])
	})

	t.Run("rejects a model this plugin does not serve", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "gpt-4o", "body": map[string]any{"kind": "json", "value": map[string]any{"model": "gpt-4o", "input": "hello"}},
		})
		require.ErrorContains(t, callErr, "suno_music or suno_lyrics")
	})

	t.Run("extracts schema-declared usage", func(t *testing.T) {
		value, callErr := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
			"model": "suno_music", "action": "MUSIC", "usagePurpose": "facts", "requestBody": map[string]any{},
		})
		require.NoError(t, callErr)
		assert.Equal(t, map[string]any{"clips": int64(2), "action": "music"}, value)

		value, callErr = plugin.Engine.Call(t.Context(), "extractUsageOnComplete", nil, map[string]any{}, []any{
			map[string]any{"id": "song-1", "audio_url": "https://upstream.example/one.mp3"},
			map[string]any{"id": "song-2", "audio_url": "https://upstream.example/two.mp3"},
		})
		require.NoError(t, callErr)
		assert.Equal(t, map[string]any{"clips": int64(2), "action": "music"}, value)
	})

	const firstAudioKey = "audio-5fc0a0cd3367274b4b6de056fc754263f8726a704bb4814ffeb88495f22dad35"
	const secondAudioKey = "audio-a9c04b840373f4ef4e8d80140b745c6f647819fa375bc34368cdccced7e2b455"
	protocolContext := map[string]any{
		"model":  "suno_music",
		"stream": true,
		"artifacts": map[string]any{
			firstAudioKey:  map[string]any{"key": firstAudioKey, "type": "audio", "url": "https://gateway.example/artifacts/song-1"},
			secondAudioKey: map[string]any{"key": secondAudioKey, "type": "audio", "url": "https://gateway.example/artifacts/song-2"},
		},
	}
	successTask := map[string]any{
		"task_id": "task-public", "status": "SUCCESS", "progress": "100%", "created_at": 10, "updated_at": 20,
		"data": []any{
			map[string]any{"id": "song-1", "title": "First", "text": "First lyrics", "audio_url": "https://upstream.example/one.mp3"},
			map[string]any{"id": "song-2", "title": "Second", "text": "Second lyrics", "audio_url": "https://upstream.example/two.mp3"},
		},
	}

	t.Run("renders stream state transitions", func(t *testing.T) {
		progressValue := callProtocol(t, "renderEvents", protocolContext, map[string]any{"status": "IN_PROGRESS", "progress": "40%"})
		progress, decodeErr := relay.DecodePluginProtocolEventResult(progressValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, progress.Events, 1)
		require.NotNil(t, progress.Events[0].Progress)
		assert.Equal(t, float64(40), *progress.Events[0].Progress)

		duplicateValue := callProtocol(t, "renderEvents", protocolContext, map[string]any{"status": "IN_PROGRESS", "progress": "40%"}, map[string]any{"status": "IN_PROGRESS", "progress": float64(40)})
		duplicate, decodeErr := relay.DecodePluginProtocolEventResult(duplicateValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		assert.Empty(t, duplicate.Events)

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
		var text string
		require.NoError(t, common.Unmarshal(success.Events[0].Data, &text))
		assert.Contains(t, text, "First lyrics")
		assert.Contains(t, text, "gateway.example/artifacts/song-1")
		assert.Contains(t, text, "gateway.example/artifacts/song-2")
		assert.NotContains(t, text, "upstream.example")
		assert.True(t, success.Done)
	})

	t.Run("renders one music message with lyrics and audio segments", func(t *testing.T) {
		value := callProtocol(t, "renderFinal", protocolContext, successTask)
		machine := relay.NewPluginResponsesMachine("task-public", "suno_music", 10, relay.DefaultPluginProtocolLimits())
		response, finalErr := machine.FinalResponse(value, "SUCCESS")
		require.NoError(t, finalErr)
		output, ok := response["output"].([]any)
		require.True(t, ok)
		require.Len(t, output, 1)
		message, ok := output[0].(map[string]any)
		require.True(t, ok)
		content, ok := message["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 3)
		for index, expected := range []string{"First lyrics", "gateway.example/artifacts/song-1", "gateway.example/artifacts/song-2"} {
			part, partOK := content[index].(map[string]any)
			require.True(t, partOK)
			text, textOK := part["text"].(string)
			require.True(t, textOK)
			assert.Contains(t, text, expected)
			assert.NotContains(t, text, "upstream.example")
		}
		metadata, ok := response["metadata"].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "sunoapi", metadata["vendor"])
	})

	t.Run("renders lyrics without audio artifacts", func(t *testing.T) {
		value := callProtocol(t, "renderFinal", map[string]any{"model": "suno_lyrics"}, map[string]any{
			"task_id": "lyrics-public", "status": "SUCCESS", "data": map[string]any{"id": "lyrics-1", "title": "Tide", "text": "Sea lyrics"},
		})
		machine := relay.NewPluginResponsesMachine("lyrics-public", "suno_lyrics", 10, relay.DefaultPluginProtocolLimits())
		response, finalErr := machine.FinalResponse(value, "SUCCESS")
		require.NoError(t, finalErr)
		output, ok := response["output"].([]any)
		require.True(t, ok)
		message, ok := output[0].(map[string]any)
		require.True(t, ok)
		content, ok := message["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		part, ok := content[0].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, part["text"], "Sea lyrics")
	})

	t.Run("requires host audio artifacts", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"}, map[string]any{
			"model": "suno_music",
		}, successTask)
		require.ErrorContains(t, callErr, "audio artifact is unavailable")
		assert.NotContains(t, callErr.Error(), "upstream.example")
	})

	t.Run("renders lyrics for an alias on retrieval without upstreamModel", func(t *testing.T) {
		lyricsTask := map[string]any{
			"task_id": "lyrics-alias", "status": "SUCCESS", "data": map[string]any{"id": "lyrics-1", "title": "Tide", "text": "Sea lyrics"},
		}
		finalValue := callProtocol(t, "renderFinal", map[string]any{"model": "my-lyrics"}, lyricsTask)
		machine := relay.NewPluginResponsesMachine("lyrics-alias", "my-lyrics", 10, relay.DefaultPluginProtocolLimits())
		response, finalErr := machine.FinalResponse(finalValue, "SUCCESS")
		require.NoError(t, finalErr)
		output, ok := response["output"].([]any)
		require.True(t, ok)
		message, ok := output[0].(map[string]any)
		require.True(t, ok)
		content, ok := message["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		part, ok := content[0].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, part["text"], "Sea lyrics")

		eventsValue := callProtocol(t, "renderEvents", map[string]any{"model": "my-lyrics"}, lyricsTask)
		events, decodeErr := relay.DecodePluginProtocolEventResult(eventsValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, events.Events, 1)
		var text string
		require.NoError(t, common.Unmarshal(events.Events[0].Data, &text))
		assert.Contains(t, text, "Sea lyrics")
	})
}
