package plugins_test

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlibabaResponsesProtocol(t *testing.T) {
	source, err := builtinplugins.Source("alibaba")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "alibaba"})
	require.NoError(t, err)

	t.Run("claims every Ali model", func(t *testing.T) {
		for _, model := range plugin.Meta.Models {
			binding, found := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
			require.True(t, found, model)
			assert.Same(t, plugin, binding.Plugin)
			assert.Equal(t, "openai_responses", binding.Protocol)
		}
	})

	t.Run("selects image and video usage profiles for every declared model", func(t *testing.T) {
		require.Len(t, plugin.Meta.UsageProfiles, 9)
		covered := make(map[string]bool)
		for _, profile := range plugin.Meta.UsageProfiles {
			for _, name := range profile.Models {
				require.False(t, covered[name], name)
				covered[name] = true
			}
		}
		for _, name := range plugin.Meta.Models {
			require.True(t, covered[name], name)
			schema, examples := plugin.Meta.UsageForModel(name)
			assert.Empty(t, examples)
			isImage := strings.Contains(name, "-image") || strings.Contains(name, "-t2i") || strings.Contains(name, "-i2i")
			switch {
			case strings.HasPrefix(name, "qwen-image-3.0"):
				assert.ElementsMatch(t, []string{"image_count", "output_image_type", "input_image_count"}, keysOf(schema), name)
				assert.Equal(t, plugin.Meta.UsageSchema["image_count"], schema["image_count"], name)
				assert.Equal(t, []string{"qima_output_1k", "qima_output_2k"}, schema["output_image_type"].Enum, name)
			case name == "z-image-turbo":
				assert.ElementsMatch(t, []string{"image_count", "prompt_extend"}, keysOf(schema), name)
				assert.Equal(t, "boolean", schema["prompt_extend"].Type, name)
			case isImage:
				assert.Equal(t, map[string]jsplugin.UsageFieldSchema{"image_count": plugin.Meta.UsageSchema["image_count"]}, schema, name)
			case name == "wan2.6-i2v-flash":
				assert.ElementsMatch(t, []string{"seconds", "resolution", "audio"}, keysOf(schema), name)
				assert.Equal(t, plugin.Meta.UsageSchema["seconds"], schema["seconds"], name)
				assert.Equal(t, []string{"720P", "1080P"}, schema["resolution"].Enum, name)
				assert.Equal(t, "boolean", schema["audio"].Type, name)
			default:
				assert.ElementsMatch(t, []string{"seconds", "resolution"}, keysOf(schema), name)
				assert.Equal(t, plugin.Meta.UsageSchema["seconds"], schema["seconds"], name)
				assert.Subset(t, plugin.Meta.UsageSchema["resolution"].Enum, schema["resolution"].Enum, name)
			}
			for key, field := range schema {
				assert.NotEmpty(t, field.Description["en"], name+" "+key)
				assert.NotEmpty(t, field.Description["zh"], name+" "+key)
			}
		}
	})

	t.Run("declares documented usage facts", func(t *testing.T) {
		for _, key := range []string{"seconds", "resolution", "image_count"} {
			schema, exists := plugin.Meta.UsageSchema[key]
			require.True(t, exists, key)
			assert.NotEmpty(t, schema.Description, key)
		}

		value, callErr := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
			"model":         "wan2.5-i2v-preview",
			"upstreamModel": "wan2.5-i2v-preview",
			"usagePurpose":  "facts",
			"requestBody": map[string]any{
				"model":    "wan2.5-i2v-preview",
				"duration": 10,
				"size":     "1080p",
				"image":    "https://cdn.example/first.png",
			},
		})
		require.NoError(t, callErr)
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		var facts map[string]any
		require.NoError(t, common.Unmarshal(encoded, &facts))
		assert.Equal(t, map[string]any{"seconds": float64(10), "resolution": "1080P"}, facts)
	})

	t.Run("parses text input and options", func(t *testing.T) {
		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "wan2.7-t2v", "body": map[string]any{"kind": "json", "value": map[string]any{
				"model":    "wan2.7-t2v",
				"input":    "waves at sunset",
				"size":     "1280*720",
				"duration": 6,
				"metadata": map[string]any{"parameters": map[string]any{"watermark": true}},
			}},
			"stream": false,
		})
		require.NoError(t, callErr)
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		var resolved map[string]any
		require.NoError(t, common.Unmarshal(encoded, &resolved))

		assert.Equal(t, map[string]any{
			"kind":   "submit",
			"model":  "wan2.7-t2v",
			"action": "text_to_video",
			"requestBody": map[string]any{
				"model":    "wan2.7-t2v",
				"prompt":   "waves at sunset",
				"size":     "1280*720",
				"duration": float64(6),
				"metadata": map[string]any{"parameters": map[string]any{"watermark": true}},
			},
		}, resolved)
	})

	t.Run("parses multimodal image input", func(t *testing.T) {
		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "wan2.7-i2v", "body": map[string]any{"kind": "json", "value": map[string]any{
				"model": "wan2.7-i2v",
				"input": []any{
					map[string]any{
						"role": "user",
						"content": []any{
							map[string]any{"type": "input_text", "text": "animate between frames"},
							map[string]any{"type": "input_image", "image_url": "https://cdn.example/first.png"},
							map[string]any{"type": "input_image", "image_url": map[string]any{"url": "https://cdn.example/last.png"}},
						},
					},
				},
			}},
			"stream": true,
		})
		require.NoError(t, callErr)
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		var resolved map[string]any
		require.NoError(t, common.Unmarshal(encoded, &resolved))

		assert.Equal(t, "wan2.7-i2v", resolved["model"])
		assert.Equal(t, "image_to_video", resolved["action"])
		requestBody, ok := resolved["requestBody"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "animate between frames", requestBody["prompt"])
		assert.Equal(t, []any{"https://cdn.example/first.png", "https://cdn.example/last.png"}, requestBody["images"])
	})

	t.Run("accepts image-only i2v input", func(t *testing.T) {
		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "wan2.7-i2v", "body": map[string]any{"kind": "json", "value": map[string]any{
				"model": "wan2.7-i2v",
				"input": []any{
					map[string]any{"type": "input_image", "image_url": "https://cdn.example/first.png"},
				},
			}},
			"stream": false,
		})
		require.NoError(t, callErr)
		encoded, marshalErr := common.Marshal(value)
		require.NoError(t, marshalErr)
		var resolved map[string]any
		require.NoError(t, common.Unmarshal(encoded, &resolved))

		assert.Equal(t, "image_to_video", resolved["action"])
		requestBody, ok := resolved["requestBody"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "", requestBody["prompt"])
		assert.Equal(t, []any{"https://cdn.example/first.png"}, requestBody["images"])
	})

	t.Run("rejects a request without input text", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "wan2.7-t2v", "body": map[string]any{"kind": "json", "value": map[string]any{"model": "wan2.7-t2v"}},
			"stream": false,
		})
		require.ErrorContains(t, callErr, "input is required")
	})

	protocolContext := map[string]any{
		"requestBody": map[string]any{"model": "wan2.7-t2v"},
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
			"output": map[string]any{
				"video_url": "https://upstream.example/video.mp4?Expires=1&Signature=must-not-leak",
			},
		},
	}

	t.Run("renders stream semantics accepted by the host", func(t *testing.T) {
		progressValue, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderEvents"}, protocolContext, map[string]any{
			"task_id":  "task_public",
			"status":   "IN_PROGRESS",
			"progress": "42%",
		})
		require.NoError(t, callErr)
		progressResult, decodeErr := relay.DecodePluginProtocolEventResult(progressValue, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, progressResult.Events, 1)
		require.NotNil(t, progressResult.Events[0].Progress)
		assert.Equal(t, float64(42), *progressResult.Events[0].Progress)
		assert.False(t, progressResult.Done)

		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderEvents"}, protocolContext, successTask)
		require.NoError(t, callErr)
		result, decodeErr := relay.DecodePluginProtocolEventResult(value, relay.DefaultPluginProtocolLimits())
		require.NoError(t, decodeErr)
		require.Len(t, result.Events, 1)
		assert.Equal(t, "output", result.Events[0].Type)
		assert.True(t, result.Done)
		var text string
		require.NoError(t, common.Unmarshal(result.Events[0].Data, &text))
		assert.Equal(t, `<video controls src="https://gateway.example/v1/tasks/task_public/artifacts/video/content?access=host%2Bcapability%3D"></video>`, text)
		assert.NotContains(t, text, "upstream.example")

		machine := relay.NewPluginResponsesMachine("task_public", "wan2.7-t2v", 10, relay.DefaultPluginProtocolLimits())
		_, machineErr := machine.CreatedEvent()
		require.NoError(t, machineErr)
		wireEvents, machineErr := machine.ApplyTick(result, "SUCCESS")
		require.NoError(t, machineErr)
		require.NotEmpty(t, wireEvents)
		assert.Equal(t, "response.completed", wireEvents[len(wireEvents)-1].Type)
	})

	t.Run("renders a valid non-stream response", func(t *testing.T) {
		value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"}, protocolContext, successTask)
		require.NoError(t, callErr)
		machine := relay.NewPluginResponsesMachine("task_public", "wan2.7-t2v", 10, relay.DefaultPluginProtocolLimits())
		response, finalErr := machine.FinalResponse(value, "SUCCESS")
		require.NoError(t, finalErr)
		assert.Equal(t, "resp_public", response["id"])
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
		assert.Equal(t, `<video controls src="https://gateway.example/v1/tasks/task_public/artifacts/video/content?access=host%2Bcapability%3D"></video>`, text)
		assert.NotContains(t, text, "upstream.example")
		metadata, ok := response["metadata"].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "ali", metadata["vendor"])
	})

	t.Run("does not fall back to the upstream URL when the host artifact is absent", func(t *testing.T) {
		_, callErr := plugin.Engine.CallPath(
			t.Context(),
			"protocols",
			[]string{"openai_responses", "renderFinal"},
			map[string]any{
				"requestBody": map[string]any{"model": "wan2.7-t2v"},
				"stream":      false,
			},
			successTask,
		)
		require.ErrorContains(t, callErr, "video artifact is unavailable")
		assert.NotContains(t, callErr.Error(), "upstream.example")
	})
}

func newAlibabaPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("alibaba")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "alibaba"})
	require.NoError(t, err)
	return plugin
}

func alibabaObject(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.Unmarshal(encoded, &result))
	return result
}

// Exercise the production host's recursive usage validation as well as the
// converter, without sending a paid generation request or needing a database.
func submitAlibabaRequest(t *testing.T, plugin *jsplugin.LoadedPlugin, upstream string, request map[string]any) (map[string]any, map[string]any, string) {
	t.Helper()
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelBaseUrl: "https://dashscope.aliyuncs.com", UpstreamModelName: upstream},
		OriginModelName: request["model"].(string),
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
	}
	adaptor := taskplugin.New(plugin)
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("task_request", request)
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	reader, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, common.Unmarshal(encoded, &body))
	facts, err := adaptor.ExtractUsageFactsValidated(c, info)
	require.NoError(t, err)
	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	return body, alibabaObject(t, facts), url
}

func TestAlibabaWanModelCapabilities(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	for _, tc := range []struct {
		model      string
		input      map[string]any
		resolution string
		allowed    []string
		size       string
		ratio      string
		service    string
		seconds    float64
	}{
		{"wan3.0-video", map[string]any{"prompt": "a cat"}, "1080P", []string{"480P", "720P", "1080P"}, "", "adaptive", "video-generation", 5},
		{"wan3.0-video-prime", map[string]any{"prompt": "a cat"}, "1080P", []string{"480P", "720P", "1080P"}, "", "adaptive", "video-generation", 5},
		{"wan2.7-t2v", map[string]any{"prompt": "a cat"}, "1080P", []string{"720P", "1080P"}, "", "16:9", "video-generation", 5},
		{"wan2.7-t2v-2026-04-25", map[string]any{"prompt": "a cat"}, "1080P", []string{"720P", "1080P"}, "", "16:9", "video-generation", 5},
		{"wan2.7-t2v-2026-06-12", map[string]any{"prompt": "a cat"}, "1080P", []string{"720P", "1080P"}, "", "16:9", "video-generation", 5},
		{"wan2.7-i2v", map[string]any{"media": []any{map[string]any{"type": "first_frame", "url": "https://cdn.example/first.png"}}}, "1080P", []string{"720P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.7-i2v-2026-04-25", map[string]any{"media": []any{map[string]any{"type": "first_clip", "url": "https://cdn.example/first.mp4"}}}, "1080P", []string{"720P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.6-t2v", map[string]any{"prompt": "a cat"}, "1080P", []string{"720P", "1080P"}, "1920*1080", "", "video-generation", 5},
		{"wan2.6-t2v-us", map[string]any{"prompt": "a cat"}, "1080P", []string{"720P", "1080P"}, "1920*1080", "", "video-generation", 5},
		{"wan2.6-i2v", map[string]any{"img_url": "https://cdn.example/first.png"}, "1080P", []string{"720P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.6-i2v-flash", map[string]any{"img_url": "https://cdn.example/first.png"}, "1080P", []string{"720P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.6-i2v-us", map[string]any{"img_url": "https://cdn.example/first.png"}, "1080P", []string{"720P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.5-t2v-preview", map[string]any{"prompt": "a cat"}, "1080P", []string{"480P", "720P", "1080P"}, "1920*1080", "", "video-generation", 5},
		{"wan2.5-i2v-preview", map[string]any{"img_url": "https://cdn.example/first.png"}, "1080P", []string{"480P", "720P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.2-t2v-plus", map[string]any{"prompt": "a cat"}, "1080P", []string{"480P", "1080P"}, "1920*1080", "", "video-generation", 5},
		{"wan2.2-i2v-plus", map[string]any{"img_url": "https://cdn.example/first.png"}, "1080P", []string{"480P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.2-i2v-flash", map[string]any{"img_url": "https://cdn.example/first.png"}, "720P", []string{"480P", "720P", "1080P"}, "", "", "video-generation", 5},
		{"wan2.2-kf2v-flash", map[string]any{"first_frame_url": "https://cdn.example/first.png", "last_frame_url": "https://cdn.example/last.png"}, "720P", []string{"480P", "720P", "1080P"}, "", "", "image2video", 5},
		{"wan2.2-s2v", map[string]any{"image_url": "https://cdn.example/first.png", "audio_url": "https://cdn.example/audio.mp3"}, "480P", []string{"480P", "720P"}, "", "", "image2video", 20},
		{"wanx2.1-t2v-plus", map[string]any{"prompt": "a cat"}, "720P", []string{"720P"}, "1280*720", "", "video-generation", 5},
		{"wanx2.1-t2v-turbo", map[string]any{"prompt": "a cat"}, "720P", []string{"480P", "720P"}, "1280*720", "", "video-generation", 5},
		{"wanx2.1-i2v-plus", map[string]any{"img_url": "https://cdn.example/first.png"}, "720P", []string{"720P"}, "", "", "video-generation", 5},
		{"wanx2.1-i2v-turbo", map[string]any{"img_url": "https://cdn.example/first.png"}, "720P", []string{"480P", "720P"}, "", "", "video-generation", 5},
	} {
		t.Run(tc.model, func(t *testing.T) {
			value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"createVideoTask"}, map[string]any{
				"body": map[string]any{"kind": "json", "value": map[string]any{"model": tc.model, "input": tc.input}},
			})
			require.NoError(t, err)
			request := alibabaObject(t, value)["requestBody"].(map[string]any)
			body, facts, url := submitAlibabaRequest(t, plugin, tc.model, request)
			assert.Equal(t, "https://dashscope.aliyuncs.com/api/v1/services/aigc/"+tc.service+"/video-synthesis", url)
			// The pricing table lists exactly the resolutions the model accepts.
			schema, _ := plugin.Meta.UsageForModel(tc.model)
			assert.Equal(t, tc.allowed, schema["resolution"].Enum)
			wantFacts := map[string]any{"seconds": tc.seconds, "resolution": tc.resolution}
			if tc.model == "wan2.6-i2v-flash" {
				wantFacts["audio"] = true // priced separately for silent output; defaults to audio
			}
			assert.Equal(t, wantFacts, facts)
			assert.Equal(t, tc.input, body["input"])
			parameters := body["parameters"].(map[string]any)
			if tc.size != "" {
				assert.Equal(t, tc.size, parameters["size"])
				assert.NotContains(t, parameters, "resolution")
			} else {
				assert.Equal(t, tc.resolution, parameters["resolution"])
				assert.NotContains(t, parameters, "size")
			}
			if tc.ratio != "" {
				assert.Equal(t, tc.ratio, parameters["ratio"])
			} else {
				assert.NotContains(t, parameters, "ratio")
			}
			if tc.model == "wan2.2-s2v" {
				assert.NotContains(t, parameters, "duration")
			} else {
				assert.Equal(t, float64(5), parameters["duration"])
			}
			for _, resolution := range tc.allowed {
				request["resolution"] = resolution
				_, facts, _ := submitAlibabaRequest(t, plugin, tc.model, request)
				assert.Equal(t, resolution, facts["resolution"])
			}
			for _, resolution := range []string{"480P", "720P", "1080P", "4K"} {
				if slices.Contains(tc.allowed, resolution) {
					continue
				}
				request["metadata"].(map[string]any)["parameters"] = map[string]any{"resolution": resolution}
				_, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"requestBody": request, "upstreamModel": tc.model})
				require.ErrorContains(t, err, "resolution must be", resolution)
			}
		})
	}
}

func TestAlibabaWanCompatibilityParameters(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	for _, tc := range []struct {
		name       string
		model      string
		request    map[string]any
		parameters map[string]any
	}{
		{"legacy dimensions retain vertical ratio", "wan2.7-t2v", map[string]any{"size": "720*1280"}, map[string]any{"resolution": "720P", "ratio": "9:16"}},
		{"OpenAI dimension separator", "wan3.0-video", map[string]any{"size": "1080x1920"}, map[string]any{"resolution": "1080P", "ratio": "9:16"}},
		{"legacy four thirds", "wan2.7-t2v", map[string]any{"size": "1088*832"}, map[string]any{"resolution": "720P", "ratio": "4:3"}},
		{"modern four thirds", "wan2.7-t2v", map[string]any{"size": "1648*1248"}, map[string]any{"resolution": "1080P", "ratio": "4:3"}},
		{"modern dimensions to legacy protocol", "wan2.5-t2v-preview", map[string]any{"size": "832*1104"}, map[string]any{"size": "832*1088"}},
		{"resolution tier compatibility", "wan2.6-t2v", map[string]any{"size": "720p", "ratio": "1:1"}, map[string]any{"size": "960*960"}},
		{"unified fields override compatibility size", "wan2.7-t2v", map[string]any{"size": "720*1280", "resolution": "1080P", "ratio": "1:1"}, map[string]any{"resolution": "1080P", "ratio": "1:1"}},
		{"native fields override unified fields", "wan2.7-t2v", map[string]any{"size": "720*1280", "resolution": "720P", "ratio": "1:1", "metadata": map[string]any{"parameters": map[string]any{"resolution": "1080P", "ratio": "3:4"}}}, map[string]any{"resolution": "1080P", "ratio": "3:4"}},
		{"native legacy size overrides unified fields", "wan2.6-t2v", map[string]any{"resolution": "1080P", "ratio": "1:1", "metadata": map[string]any{"parameters": map[string]any{"size": "720*1280"}}}, map[string]any{"size": "720*1280"}},
		{"image size only selects tier", "wan2.7-i2v", map[string]any{"image": "https://cdn.example/first.png", "size": "720*1280", "ratio": "1:1"}, map[string]any{"resolution": "720P"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := maps.Clone(tc.request)
			request["model"], request["prompt"] = "my-wan-alias", "a cat"
			body, _, _ := submitAlibabaRequest(t, plugin, tc.model, request)
			assert.Equal(t, tc.model, body["model"])
			parameters := body["parameters"].(map[string]any)
			delete(parameters, "duration")
			delete(parameters, "prompt_extend")
			assert.Equal(t, tc.parameters, parameters)
		})
	}
}

func TestAlibabaWanFlashAudioFact(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	for _, tc := range []struct {
		name    string
		request map[string]any
		audio   bool
	}{
		{"audio defaults to true", map[string]any{}, true},
		{"explicit audio", map[string]any{"audio": true}, true},
		{"silent output", map[string]any{"audio": false}, false},
		{"native parameters win", map[string]any{"audio": true, "metadata": map[string]any{"parameters": map[string]any{"audio": false}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"model": "wan2.6-i2v-flash", "prompt": "a cat", "image": "https://cdn.example/first.png", "resolution": "720P", "duration": float64(5)}
			maps.Copy(request, tc.request)
			body, facts, _ := submitAlibabaRequest(t, plugin, "wan2.6-i2v-flash", request)
			assert.Equal(t, map[string]any{"seconds": float64(5), "resolution": "720P", "audio": tc.audio}, facts)
			parameters := body["parameters"].(map[string]any)
			if _, explicit := tc.request["audio"]; explicit || tc.request["metadata"] != nil {
				assert.Equal(t, tc.audio, parameters["audio"])
			} else {
				assert.NotContains(t, parameters, "audio")
			}
		})
	}
	// Other Wan models keep their two-fact schema even when a client sends audio.
	_, facts, _ := submitAlibabaRequest(t, plugin, "wan2.6-i2v", map[string]any{"model": "wan2.6-i2v", "prompt": "a cat", "image": "https://cdn.example/first.png", "audio": false, "duration": float64(5)})
	assert.Equal(t, map[string]any{"seconds": float64(5), "resolution": "1080P"}, facts)
}

func TestAlibabaWanEntrypointsPreserveOptions(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	input := map[string]any{"prompt": "a cat", "negative_prompt": "blur", "audio_url": "https://cdn.example/audio.mp3"}
	parameters := map[string]any{"resolution": "720P", "ratio": "9:16", "duration": float64(6), "prompt_extend": false, "watermark": false, "seed": float64(0)}
	for _, entry := range []string{"native", "openai_responses", "openai_video", "multipart"} {
		t.Run(entry, func(t *testing.T) {
			ctx := map[string]any{"model": "wan2.7-t2v"}
			var value any
			var err error
			if entry == "native" {
				ctx["body"] = map[string]any{"kind": "json", "value": map[string]any{"model": "wan2.7-t2v", "input": input, "parameters": parameters}}
				value, err = plugin.Engine.CallPath(t.Context(), "native", []string{"createVideoTask"}, ctx)
			} else {
				request := maps.Clone(input)
				maps.Copy(request, parameters)
				request["model"] = "wan2.7-t2v"
				request["resolution"] = "720p"
				if entry == "openai_responses" {
					request["input"] = request["prompt"]
				}
				protocol := entry
				ctx["body"] = map[string]any{"kind": "json", "value": request}
				if entry == "multipart" {
					protocol = "openai_video"
					ctx["body"] = map[string]any{"kind": "multipart", "fields": map[string]any{
						"prompt": []string{"a cat"}, "negative_prompt": []string{"blur"}, "audio_url": []string{"https://cdn.example/audio.mp3"},
						"resolution": []string{"720p"}, "ratio": []string{"9:16"}, "duration": []string{"6"},
						"prompt_extend": []string{"false"}, "watermark": []string{"false"}, "seed": []string{"0"},
					}}
				}
				value, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{protocol, "decodeRequest"}, ctx)
			}
			require.NoError(t, err)
			request := alibabaObject(t, value)["requestBody"].(map[string]any)
			body, facts, _ := submitAlibabaRequest(t, plugin, "wan2.7-t2v", request)
			assert.Equal(t, input, body["input"])
			assert.Equal(t, parameters, body["parameters"])
			assert.Equal(t, map[string]any{"seconds": float64(6), "resolution": "720P"}, facts)
		})
	}
}

func TestAlibabaWanDurationValidation(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	for _, tc := range []struct {
		model   string
		valid   []int
		invalid []any
	}{
		{"wan3.0-video", []int{-1, 2, 30}, []any{0, 1, 31, 2.5}},
		{"wan2.7-t2v", []int{2, 15}, []any{-1, 0, 1, 16, 2.5}},
		{"wan2.6-i2v", []int{2, 15}, []any{1, 16}},
		{"wan2.6-t2v-us", []int{5, 10, 15}, []any{2, 6, 16}},
		{"wan2.5-t2v-preview", []int{5, 10}, []any{6, 15}},
		{"wan2.2-i2v-flash", []int{5}, []any{3, 10}},
		{"wan2.2-kf2v-flash", []int{5}, []any{3, 10}},
		{"wanx2.1-i2v-turbo", []int{3, 4, 5}, []any{2, 6}},
		{"wan2.2-s2v", []int{1, 19}, []any{-1, 0, 20, 3601}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			request := map[string]any{"model": tc.model, "prompt": "a cat", "image": "https://cdn.example/first.png"}
			if tc.model == "wan2.2-s2v" {
				request["audio_url"] = "https://cdn.example/audio.mp3"
			}
			ctx := map[string]any{"requestBody": request, "upstreamModel": tc.model}
			for _, duration := range tc.valid {
				request["duration"] = duration
				_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", ctx)
				require.NoError(t, err, "duration=%v", duration)
			}
			request["duration"] = 5
			for _, duration := range append(tc.invalid, "bad", true, 1e30) {
				request["metadata"] = map[string]any{"parameters": map[string]any{"duration": duration}}
				_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", ctx)
				require.ErrorContains(t, err, "duration", "duration=%v", duration)
			}
		})
	}
	// Shadowed -1 values must not override explicit metadata duration, nor
	// mutate a request that will be decoded again after channel selection.
	request := map[string]any{"model": "wan3.0-video", "prompt": "a cat", "duration": -1, "metadata": map[string]any{"parameters": map[string]any{"duration": 8}}}
	value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "wan3.0-video", "body": map[string]any{"kind": "json", "value": request}})
	require.NoError(t, err)
	decoded := alibabaObject(t, value)["requestBody"].(map[string]any)
	assert.NotContains(t, decoded, "auto_duration")
	body, facts, _ := submitAlibabaRequest(t, plugin, "wan3.0-video", decoded)
	assert.Equal(t, float64(8), body["parameters"].(map[string]any)["duration"])
	assert.Equal(t, float64(8), facts["seconds"])

	request["duration"] = 8
	request["metadata"] = map[string]any{"parameters": map[string]any{"duration": -1}}
	value, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "wan3.0-video", "body": map[string]any{"kind": "json", "value": request}})
	require.NoError(t, err)
	decoded = alibabaObject(t, value)["requestBody"].(map[string]any)
	body, facts, _ = submitAlibabaRequest(t, plugin, "wan3.0-video", decoded)
	assert.Equal(t, float64(-1), body["parameters"].(map[string]any)["duration"])
	assert.Equal(t, float64(30), facts["seconds"])
}

func TestAlibabaWanMediaInputs(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	first := "https://cdn.example/first.png"
	last := "https://cdn.example/last.png"
	audio := "https://cdn.example/audio.mp3"
	clip := "https://cdn.example/clip.mp4"
	for _, tc := range []struct {
		name    string
		model   string
		request map[string]any
		input   map[string]any
		seconds float64
	}{
		{"2.7 frame and driving audio", "wan2.7-i2v", map[string]any{"image": first, "audio_url": audio}, map[string]any{"media": []any{map[string]any{"type": "first_frame", "url": first}, map[string]any{"type": "driving_audio", "url": audio}}}, 5},
		{"2.7 frame pair", "wan2.7-i2v", map[string]any{"images": []string{first, last}}, map[string]any{"media": []any{map[string]any{"type": "first_frame", "url": first}, map[string]any{"type": "last_frame", "url": last}}}, 5},
		{"legacy frame pair", "wan2.2-kf2v-flash", map[string]any{"images": []string{first, last}}, map[string]any{"first_frame_url": first, "last_frame_url": last}, 5},
		{"digital human", "wan2.2-s2v", map[string]any{"image": first, "audio_url": audio}, map[string]any{"image_url": first, "audio_url": audio}, 20},
		{"2.7 continuation counts total duration", "wan2.7-i2v", map[string]any{"duration": 15, "metadata": map[string]any{"input": map[string]any{"media": []any{map[string]any{"type": "first_clip", "url": clip}, map[string]any{"type": "last_frame", "url": last}}}}}, map[string]any{"media": []any{map[string]any{"type": "first_clip", "url": clip}, map[string]any{"type": "last_frame", "url": last}}}, 15},
		{"Wan3 reference video reserves input and output", "wan3.0-video", map[string]any{"duration": 5, "media": []any{map[string]any{"type": "reference_video", "url": clip}, map[string]any{"type": "reference_audio", "url": audio}}}, map[string]any{"media": []any{map[string]any{"type": "reference_video", "url": clip}, map[string]any{"type": "reference_audio", "url": audio}}}, 30},
		{"Wan3 frame pair", "wan3.0-video-prime", map[string]any{"images": []string{first, last}, "audio": false}, map[string]any{"media": []any{map[string]any{"type": "first_frame", "url": first}, map[string]any{"type": "last_frame", "url": last}}}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := maps.Clone(tc.request)
			request["model"] = tc.model
			body, facts, _ := submitAlibabaRequest(t, plugin, tc.model, request)
			assert.Equal(t, tc.input, body["input"])
			assert.Equal(t, tc.seconds, facts["seconds"])
			if request["audio"] == false {
				assert.Equal(t, false, body["parameters"].(map[string]any)["audio"])
			}
		})
	}
	for _, tc := range []struct {
		name  string
		model string
		media []any
	}{
		{"missing visual input", "wan2.7-i2v", []any{map[string]any{"type": "driving_audio", "url": audio}}},
		{"duplicate first frame", "wan2.7-i2v", []any{map[string]any{"type": "first_frame", "url": first}, map[string]any{"type": "first_frame", "url": last}}},
		{"continuation with driving audio", "wan2.7-i2v", []any{map[string]any{"type": "first_clip", "url": clip}, map[string]any{"type": "driving_audio", "url": audio}}},
		{"mixed frames and references", "wan3.0-video", []any{map[string]any{"type": "first_frame", "url": first}, map[string]any{"type": "reference_video", "url": clip}}},
		{"unknown media type", "wan3.0-video", []any{map[string]any{"type": "first_clip", "url": clip}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"requestBody": map[string]any{
				"model": tc.model, "metadata": map[string]any{"input": map[string]any{"media": tc.media}},
			}})
			require.Error(t, err)
		})
	}
}

func TestAlibabaWanCompletionFactsAndArtifacts(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	for _, tc := range []struct {
		name  string
		model string
		usage map[string]any
		facts map[string]any
	}{
		{"digital human fractional duration", "wan2.2-s2v", map[string]any{"duration": 18.13, "SR": 480}, map[string]any{"seconds": 18.13, "resolution": "480P"}},
		{"Wan3 input plus output", "wan3.0-video", map[string]any{"input_video_duration": 4.25, "output_video_duration": 6.5, "duration": 6.5, "SR": 720}, map[string]any{"seconds": 10.75, "resolution": "720P"}},
		{"Wan3 without reference video", "wan3.0-video-prime", map[string]any{"input_video_duration": 0, "output_video_duration": 8, "SR": 1080}, map[string]any{"seconds": float64(8), "resolution": "1080P"}},
		{"Wan3 missing input duration preserves reserved seconds", "wan3.0-video", map[string]any{"output_video_duration": 6, "SR": 720}, map[string]any{"resolution": "720P"}},
		{"2.7 continuation uses total duration", "wan2.7-i2v", map[string]any{"duration": 15, "output_video_duration": 12, "SR": 1080}, map[string]any{"seconds": float64(15), "resolution": "1080P"}},
		{"flash silent output settles as silent", "wan2.6-i2v-flash", map[string]any{"duration": 5, "SR": 720, "audio": false}, map[string]any{"seconds": float64(5), "resolution": "720P", "audio": false}},
		{"flash without audio flag keeps the estimate", "wan2.6-i2v-flash", map[string]any{"duration": 5, "SR": 1080}, map[string]any{"seconds": float64(5), "resolution": "1080P"}},
		{"audio flag is ignored for models priced without it", "wan2.6-i2v", map[string]any{"duration": 5, "SR": 1080, "audio": false}, map[string]any{"seconds": float64(5), "resolution": "1080P"}},
		{"missing usage keeps reservation", "wan2.7-t2v", map[string]any{}, nil},
		{"oversized duration rejected by host", "wan2.2-s2v", map[string]any{"duration": 1e30, "SR": 480}, nil},
		{"negative input cannot reduce Wan3 charge", "wan3.0-video", map[string]any{"input_video_duration": -5, "output_video_duration": 10}, nil},
		{"negative output cannot reduce Wan3 charge", "wan3.0-video", map[string]any{"input_video_duration": 10, "output_video_duration": -5}, nil},
		{"non-numeric duration rejected by host", "wan2.2-s2v", map[string]any{"duration": "invalid", "SR": 480}, nil},
		{"unknown resolution rejected by host", "wan2.7-t2v", map[string]any{"duration": 5, "SR": 4320}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := taskplugin.New(plugin)
			task := &model.Task{Properties: model.Properties{OriginModelName: "my-alias", UpstreamModelName: tc.model}}
			encoded, err := common.Marshal(map[string]any{
				"output": map[string]any{"task_status": "SUCCEEDED", "results": map[string]any{"video_url": "https://cdn.example/result.mp4"}},
				"usage":  tc.usage,
			})
			require.NoError(t, err)
			result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, encoded)
			require.NoError(t, err)
			assert.Equal(t, "SUCCESS", result.Status)
			assert.Equal(t, "https://cdn.example/result.mp4", result.Url)
			if tc.facts == nil {
				assert.Nil(t, result.UsageFacts)
			} else {
				assert.Equal(t, tc.facts, alibabaObject(t, result.UsageFacts))
			}
		})
	}

	adaptor := taskplugin.New(plugin)
	task := &model.Task{TaskID: "task_public", Status: model.TaskStatusSuccess, Properties: model.Properties{OriginModelName: "wan2.2-s2v"}}
	task.SetData(map[string]any{"output": map[string]any{"task_status": "SUCCEEDED", "results": map[string]any{"video_url": "https://cdn.example/result.mp4"}}})
	artifacts, err := adaptor.ListArtifacts(task)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	assert.Equal(t, "video", artifacts[0].Key)
	value, err := plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{"artifactKey": "video", "data": map[string]any{"output": map[string]any{"results": map[string]any{"video_url": "https://cdn.example/result.mp4"}}}, "clientRequest": map[string]any{"method": "GET"}})
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example/result.mp4", alibabaObject(t, value)["url"])
}

func TestAlibabaImageSubmission(t *testing.T) {
	source, err := builtinplugins.Source("alibaba")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "alibaba"})
	require.NoError(t, err)
	const base = "/api/v1/services/aigc/"
	for _, tc := range []struct {
		name       string
		model      string
		input      map[string]any
		parameters map[string]any
		path       string
		count      float64
	}{
		{"legacy text", "wan2.2-t2i-flash", map[string]any{"prompt": "a cat"}, map[string]any{"n": 2}, "text2image/image-synthesis", 2},
		{"legacy default count", "wanx2.1-t2i-plus", map[string]any{"prompt": "a cat"}, nil, "text2image/image-synthesis", 4},
		{"2.6 text", "wan2.6-t2i", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "a cat"}}}}}, map[string]any{"n": 2, "seed": 0, "watermark": false, "size": "960*1696"}, "image-generation/generation", 2},
		{"2.6 synchronous", "wan2.6-t2i", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "a cat"}}}}}, nil, "multimodal-generation/generation", 1},
		{"2.7 group default count", "wan2.7-image", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "seasons"}}}}}, map[string]any{"enable_sequential": true}, "image-generation/generation", 12},
		{"2.6 interleaved default count", "wan2.6-image", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "recipe"}}}}}, map[string]any{"enable_interleave": true}, "image-generation/generation", 5},
		{"2.6 edit", "wan2.6-image", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"image": "https://cdn.example/input.png"}, map[string]any{"text": "watercolor"}}}}}, map[string]any{"size": "2K", "n": 1}, "multimodal-generation/generation", 1},
		{"2.7 4K", "wan2.7-image-pro", map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "a cat"}}}}}, map[string]any{"size": "4K"}, "multimodal-generation/generation", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/ali" + base + tc.path
			binding, found := registry.Generation().LookupDeclaredRoute(http.MethodPost, path)
			require.True(t, found)
			assert.Contains(t, binding.Route.Models, tc.model)
			value, err := plugin.Engine.CallPath(t.Context(), "native", []string{binding.Route.Decode}, map[string]any{
				"path": path, "body": map[string]any{"kind": "json", "value": map[string]any{"model": tc.model, "input": tc.input, "parameters": tc.parameters}},
			})
			require.NoError(t, err)
			request := alibabaObject(t, value)["requestBody"].(map[string]any)
			body, facts, url := submitAlibabaRequest(t, plugin, tc.model, request)
			assert.Equal(t, "https://dashscope.aliyuncs.com"+base+tc.path, url)
			assert.Equal(t, alibabaObject(t, tc.input), body["input"])
			assert.Equal(t, map[string]any{"image_count": tc.count}, facts)
			parameters := body["parameters"].(map[string]any)
			for key, expected := range alibabaObject(t, tc.parameters) {
				assert.Equal(t, expected, parameters[key], key)
			}
			value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"upstreamModel": tc.model, "requestBody": request})
			require.NoError(t, err)
			headers := alibabaObject(t, value)["headers"].(map[string]any)
			if tc.path == "multimodal-generation/generation" {
				assert.NotContains(t, headers, "X-DashScope-Async")
			} else {
				assert.Equal(t, "enable", headers["X-DashScope-Async"])
			}
		})
	}

	t.Run("Responses images use task artifacts and do not claim video creation", func(t *testing.T) {
		for _, name := range []string{"wan2.6-t2i", "wan2.7-image", "wanx2.1-t2i-plus"} {
			_, found := registry.Generation().LookupEndpoint(http.MethodPost, "/v1/responses", name)
			require.True(t, found)
			_, found = registry.Generation().LookupEndpoint(http.MethodPost, "/v1/videos", name)
			assert.False(t, found)
		}
		value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": "my-image-alias", "upstreamModel": "wan2.7-image", "body": map[string]any{"kind": "json", "value": map[string]any{
				"input": []any{map[string]any{"type": "input_text", "text": "watercolor"}, map[string]any{"type": "input_image", "image_url": "https://cdn.example/input.png"}}, "n": 2,
			}},
		})
		require.NoError(t, err)
		resolved := alibabaObject(t, value)
		assert.Equal(t, "image_to_image", resolved["action"])
		body, facts, url := submitAlibabaRequest(t, plugin, "wan2.7-image", resolved["requestBody"].(map[string]any))
		assert.Equal(t, "wan2.7-image", body["model"])
		assert.Equal(t, map[string]any{"image_count": float64(2)}, facts)
		assert.Equal(t, "https://dashscope.aliyuncs.com"+base+"image-generation/generation", url)
	})
}

func TestAlibabaImageValidation(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	for _, tc := range []struct {
		name    string
		model   string
		request map[string]any
		message string
	}{
		{"zero count", "wan2.6-t2i", map[string]any{"n": 0}, "n must be"},
		{"fractional count", "wan2.6-t2i", map[string]any{"n": 1.5}, "n must be"},
		{"string count", "wan2.6-t2i", map[string]any{"n": "2"}, "n must be"},
		{"oversized nested count", "wan2.6-t2i", map[string]any{"metadata": map[string]any{"parameters": map[string]any{"n": 1e30}}}, "n must be"},
		{"negative nested count", "wan2.6-t2i", map[string]any{"metadata": map[string]any{"parameters": map[string]any{"n": -1}}}, "n must be"},
		{"group limit", "wan2.7-image", map[string]any{"enable_sequential": true, "n": 13}, "n must be"},
		{"interleaved limit", "wan2.6-image", map[string]any{"enable_interleave": true, "max_images": 6}, "max_images must be"},
		{"nested interleaved limit", "wan2.6-image", map[string]any{"metadata": map[string]any{"parameters": map[string]any{"enable_interleave": true, "max_images": 1e30}}}, "max_images must be"},
		{"interleaved n", "wan2.6-image", map[string]any{"enable_interleave": true, "n": 2}, "n must be"},
		{"string mode flag", "wan2.7-image", map[string]any{"enable_sequential": "true"}, "must be a boolean"},
		{"upstream SSE", "wan2.6-image", map[string]any{"metadata": map[string]any{"parameters": map[string]any{"stream": true}}}, "upstream streaming requires"},
		{"legacy synchronous", "wan2.2-t2i-flash", map[string]any{"metadata": map[string]any{"upstream_mode": "sync"}}, "only supports asynchronous"},
		{"edit without reference", "wan2.6-image", nil, "requires a reference image"},
		{"t2i with reference", "wan2.6-t2i", map[string]any{"image": "https://cdn.example/input.png"}, "only supports text-to-image input"},
		{"invalid 4K", "wan2.7-image", map[string]any{"size": "4K"}, "4K is only supported"},
		{"oversized pixel dimensions", "wan2.7-image", map[string]any{"size": "999999999999*999999999999"}, "pixel and aspect-ratio limits"},
		{"4K group dimensions", "wan2.7-image-pro", map[string]any{"size": "4096*4096", "enable_sequential": true}, "pixel and aspect-ratio limits"},
		{"model override", "wan2.7-image", map[string]any{"metadata": map[string]any{"model": "wan2.7-image-pro"}}, "can't change model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"model": tc.model, "prompt": "a cat"}
			maps.Copy(request, tc.request)
			_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"requestBody": request, "upstreamModel": tc.model})
			require.ErrorContains(t, err, tc.message)
			// The production validator must reject the same input before HTTP/billing.
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.model}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor := taskplugin.New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set("task_request", request)
			taskErr := adaptor.ValidateRequestAndSetAction(c, info)
			require.NotNil(t, taskErr)
			assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
		})
	}
}

func TestAlibabaImageResults(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	const first = "https://cdn.example/first.png"
	const second = "https://cdn.example/second.png"
	for _, tc := range []struct {
		name   string
		model  string
		output map[string]any
	}{
		{"legacy partial success", "wan2.2-t2i-flash", map[string]any{"results": []any{map[string]any{"url": first}, map[string]any{"code": "DataInspectionFailed"}, map[string]any{"url": second}}}},
		{"modern interleaved output", "wan2.6-image", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"text": "Step 1"}, map[string]any{"image": first}, map[string]any{"text": "Step 2"}, map[string]any{"image": second}}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := maps.Clone(tc.output)
			output["task_status"] = "SUCCEEDED"
			body := map[string]any{"output": output, "usage": map[string]any{"image_count": 2}}
			encoded, err := common.Marshal(body)
			require.NoError(t, err)
			task := &model.Task{TaskID: "task_public", Status: model.TaskStatusSuccess, Properties: model.Properties{OriginModelName: "alias", UpstreamModelName: tc.model}}
			task.SetData(body)
			adaptor := taskplugin.New(plugin)
			adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
			result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK}, encoded)
			require.NoError(t, err)
			assert.Equal(t, "SUCCESS", result.Status)
			assert.Equal(t, first, result.Url)
			require.Equal(t, map[string]any{"image_count": float64(2)}, result.UsageFacts)
			// A reservation for four images can settle to the two actual successes.
			cost, _, err := billingexpr.RunExprWithRequest(`tier("image", u("image_count") * 0.1)`, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: result.UsageFacts})
			require.NoError(t, err)
			assert.InDelta(t, 0.2, cost, 1e-9)
			artifacts, err := adaptor.ListArtifacts(task)
			require.NoError(t, err)
			require.Len(t, artifacts, 2)
			assert.Equal(t, "image-2", artifacts[1].Key)
			assert.Equal(t, "image", artifacts[1].Type)
			content, err := adaptor.BuildContentRequest(task, "image-2", relaychannel.TaskArtifactClientRequest{Method: http.MethodGet})
			require.NoError(t, err)
			assert.Equal(t, second, content.URL)
			assert.True(t, content.Credentialless)
			assert.Empty(t, content.Headers)
			_, err = adaptor.BuildContentRequest(task, "image-3", relaychannel.TaskArtifactClientRequest{Method: http.MethodGet})
			require.Error(t, err)
			value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"}, map[string]any{
				"artifacts": map[string]any{"image-1": map[string]any{"url": "/v1/tasks/task_public/artifacts/image-1/content"}, "image-2": map[string]any{"url": "/v1/tasks/task_public/artifacts/image-2/content"}},
			}, map[string]any{"status": "SUCCESS", "data": body})
			require.NoError(t, err)
			final := alibabaObject(t, value)
			message := final["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
			assert.Contains(t, message, "![Image 1](</v1/tasks/task_public/artifacts/image-1/content>)")
			assert.Contains(t, message, "![Image 2](</v1/tasks/task_public/artifacts/image-2/content>)")
			assert.NotContains(t, message, "cdn.example")
			if tc.model == "wan2.6-image" {
				assert.Contains(t, message, "Step 1\n\n![Image 1]")
			}
		})
	}

	t.Run("synchronous completion uses the host ID and retains result JSON", func(t *testing.T) {
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "wan2.6-t2i"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"}}
		adaptor := taskplugin.New(plugin)
		adaptor.Init(info)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set("task_request", map[string]any{"model": "wan2.6-t2i", "prompt": "a cat", "metadata": map[string]any{"upstream_mode": "sync"}})
		body := map[string]any{"request_id": "request_upstream", "output": map[string]any{"finished": true, "choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"image": first}}}}}}, "usage": map[string]any{"image_count": 1}}
		encoded, err := common.Marshal(body)
		require.NoError(t, err)
		parsed, taskErr := adaptor.ParseResponse(c, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(encoded))}, info)
		require.Nil(t, taskErr)
		require.NotNil(t, parsed.Immediate)
		assert.Equal(t, "task_public", parsed.UpstreamTaskID)
		assert.Equal(t, "SUCCESS", parsed.Immediate.Status)
		assert.Equal(t, "100%", parsed.Immediate.Progress)
		assert.Equal(t, first, parsed.Immediate.Url)
		assert.JSONEq(t, string(encoded), string(parsed.TaskData))
		assert.Equal(t, map[string]any{"image_count": float64(1)}, parsed.Immediate.UsageFacts)
		value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"imageCreated"}, map[string]any{}, map[string]any{"data": body})
		require.NoError(t, err)
		assert.Equal(t, alibabaObject(t, body), alibabaObject(t, value))
	})

	t.Run("asynchronous submission remains pending", func(t *testing.T) {
		value, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"model": "wan2.6-t2i", "requestBody": map[string]any{"model": "wan2.6-t2i", "prompt": "a cat"}}, map[string]any{"body": map[string]any{"output": map[string]any{"task_id": "upstream_task", "task_status": "PENDING"}}})
		require.NoError(t, err)
		result := alibabaObject(t, value)
		assert.Equal(t, "upstream_task", result["taskId"])
		assert.NotContains(t, result, "immediate")
	})

	t.Run("failed and malformed synchronous results do not complete a task", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"code": "DataInspectionFailed", "message": "rejected"},
			{"output": map[string]any{"finished": true, "choices": []any{}}},
			{"output": map[string]any{"finished": false, "choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"image": first}}}}}}},
		} {
			_, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"upstreamModel": "wan2.6-t2i", "publicTaskId": "task_public", "requestBody": map[string]any{"model": "wan2.6-t2i", "prompt": "a cat", "metadata": map[string]any{"upstream_mode": "sync"}}}, map[string]any{"body": body})
			require.Error(t, err)
		}
	})

	t.Run("invalid completion counts retain the reservation", func(t *testing.T) {
		adaptor := taskplugin.New(plugin)
		task := &model.Task{Properties: model.Properties{UpstreamModelName: "wan2.6-t2i"}}
		for _, count := range []any{-1, 0, 1.5, 1e30, "2"} {
			encoded, err := common.Marshal(map[string]any{"output": map[string]any{"task_status": "SUCCEEDED", "choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"image": first}}}}}}, "usage": map[string]any{"image_count": count}})
			require.NoError(t, err)
			result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK}, encoded)
			require.NoError(t, err)
			assert.Equal(t, "SUCCESS", result.Status)
			assert.Nil(t, result.UsageFacts, "invalid count %v must not replace the reservation", count)
		}
	})

	t.Run("interleaved text without images has zero image usage", func(t *testing.T) {
		body := map[string]any{"output": map[string]any{"task_status": "SUCCEEDED", "choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"text": "Text-only answer"}}}}}}}
		encoded, err := common.Marshal(body)
		require.NoError(t, err)
		adaptor := taskplugin.New(plugin)
		result, err := adaptor.ParseTaskResult(&model.Task{Properties: model.Properties{UpstreamModelName: "wan2.6-image"}}, &http.Response{StatusCode: http.StatusOK}, encoded)
		require.NoError(t, err)
		assert.Equal(t, "SUCCESS", result.Status)
		assert.Equal(t, map[string]any{"image_count": float64(0)}, result.UsageFacts)
		value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderEvents"}, map[string]any{}, map[string]any{"status": "SUCCESS", "data": body}, nil)
		require.NoError(t, err)
		events := alibabaObject(t, value)
		assert.Equal(t, true, events["done"])
		assert.Equal(t, []any{map[string]any{"type": "output", "data": "Text-only answer"}}, events["events"])
	})
}

func TestAlibabaSynchronousMultiImageAndStream(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("SSE=%t", streaming), func(t *testing.T) {
			name := "wan2.7-image"
			parameters := map[string]any{"n": 4}
			if streaming {
				name = "wan2.6-image"
				parameters = map[string]any{"enable_interleave": true, "max_images": 3}
			}
			info := &relaycommon.RelayInfo{OriginModelName: name, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: name, ChannelBaseUrl: "https://dashscope.aliyuncs.com"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_sync"}}
			adaptor := taskplugin.New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			c.Set("task_request", map[string]any{"model": name, "prompt": "recipe", "metadata": map[string]any{"upstream_mode": "sync", "parameters": parameters}})
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			facts, err := adaptor.ExtractUsageFactsValidated(c, info)
			require.NoError(t, err)
			wantEstimate := float64(4)
			if streaming {
				wantEstimate = 3
			}
			assert.Equal(t, wantEstimate, facts["image_count"])
			var content []any
			if streaming {
				content = append(content, map[string]any{"type": "text", "text": "第一步"})
			}
			content = append(content, map[string]any{"type": "image", "image": "https://cdn.example/1.png"})
			if streaming {
				content = append(content, map[string]any{"type": "text", "text": "第二步"})
			}
			content = append(content, map[string]any{"type": "image", "image": "https://cdn.example/2.png"})
			body := map[string]any{"request_id": "vendor-request", "output": map[string]any{"finished": true, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}}, "usage": map[string]any{"image_count": 2}}
			encoded, err := common.Marshal(body)
			require.NoError(t, err)
			responseType := "application/json"
			if streaming {
				var stream strings.Builder
				parts := []map[string]any{{"type": "text", "text": "第"}, {"type": "text", "text": "一步"}, {"type": "image", "image": "https://cdn.example/1.png"}, {"type": "text", "text": "第二步"}, {"type": "image", "image": "https://cdn.example/2.png"}}
				count := 0
				for index, part := range parts {
					if part["image"] != nil {
						count++
					}
					finish := "null"
					if index == len(parts)-1 {
						finish = "stop"
					}
					chunk, err := common.Marshal(map[string]any{"request_id": "vendor-request", "output": map[string]any{"finished": true, "choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"role": "assistant", "content": []any{part}}}}}, "usage": map[string]any{"image_count": count}})
					require.NoError(t, err)
					stream.WriteString("data: ")
					stream.Write(chunk)
					stream.WriteString("\n\n")
				}
				encoded = []byte(stream.String())
				responseType = "text/event-stream"
				request := httptest.NewRequest("POST", "https://dashscope.aliyuncs.com", nil)
				require.NoError(t, adaptor.BuildRequestHeader(c, request, info))
				assert.Equal(t, "enable", request.Header.Get("X-DashScope-Sse"))
			}
			parsed, taskErr := adaptor.ParseResponse(c, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {responseType}}, Body: io.NopCloser(bytes.NewReader(encoded))}, info)
			require.Nil(t, taskErr)
			require.NotNil(t, parsed.Immediate)
			assert.Equal(t, "SUCCESS", parsed.Immediate.Status)
			assert.Equal(t, map[string]any{"image_count": float64(2)}, parsed.Immediate.UsageFacts)
			want, err := common.Marshal(body)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(parsed.TaskData))
			assert.False(t, c.Writer.Written())
		})
	}
}

// The OpenAI Images protocol claims every declared image model on both image
// endpoints; chat and video models keep the built-in relay.
func TestAlibabaOpenAIImageProtocolBindings(t *testing.T) {
	source, err := builtinplugins.Source("alibaba")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "alibaba"})
	require.NoError(t, err)
	for _, name := range []string{"qwen-image-3.0-pro", "qwen-image-plus", "qwen-image-edit-plus", "z-image-turbo", "wan2.6-t2i", "wan2.2-t2i-flash", "wan2.5-i2i-preview", "wanx2.1-imageedit", "qwen-image-edit-max-2026-01-16"} {
		for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
			binding, found := registry.Generation().LookupEndpoint(http.MethodPost, path, name)
			require.True(t, found, name+" "+path)
			assert.Same(t, plugin, binding.Plugin)
			assert.Equal(t, "openai_image", binding.Protocol)
		}
		_, found := registry.Generation().LookupEndpoint(http.MethodPost, "/v1/videos", name)
		assert.False(t, found, name)
	}
	for _, name := range []string{"wan2.6-t2v", "qwen-turbo"} {
		_, found := registry.Generation().LookupEndpoint(http.MethodPost, "/v1/images/generations", name)
		assert.False(t, found, name)
	}
}

func decodeAlibabaImage(t *testing.T, plugin *jsplugin.LoadedPlugin, operation string, body map[string]any) (map[string]any, error) {
	t.Helper()
	path := "/v1/images/generations"
	if operation == "edit" {
		path = "/v1/images/edits"
	}
	value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "decodeRequest"}, map[string]any{
		"protocol": "openai_image", "operation": operation, "model": body["model"], "path": path, "method": http.MethodPost,
		"body": map[string]any{"kind": "json", "value": body},
	})
	if err != nil {
		return nil, err
	}
	return alibabaObject(t, value), nil
}

// OpenAI image requests map onto the DashScope services the models support,
// reserve the requested count, and reject counts the vendor would refuse
// before any quota is reserved.
func TestAlibabaOpenAIImageDecodeAndSubmission(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	const base = "https://dashscope.aliyuncs.com/api/v1/services/aigc/"
	for _, tc := range []struct {
		name, model, operation string
		body                   map[string]any
		wantService, wantAsync string
		wantAction             string
		wantN                  float64
		wantFacts              map[string]any
		wantRatios             map[string]any
	}{
		{"qwen-image-3.0-pro two images", "qwen-image-3.0-pro", "generate", map[string]any{"prompt": "a white siamese cat", "n": 2, "size": "1024x1024"},
			"multimodal-generation/generation", "", "text_to_image", 2,
			map[string]any{"image_count": float64(2), "output_image_type": "qima_output_1k", "input_image_count": float64(0)}, map[string]any{"image_count": float64(2)}},
		{"qwen-image-3.0-pro without size reserves the 2K tier", "qwen-image-3.0", "generate", map[string]any{"prompt": "poster"},
			"multimodal-generation/generation", "", "text_to_image", 1,
			map[string]any{"image_count": float64(1), "output_image_type": "qima_output_2k", "input_image_count": float64(0)}, map[string]any{"image_count": float64(1)}},
		{"fixed count model", "qwen-image-plus", "generate", map[string]any{"prompt": "a cat", "size": "1328x1328"},
			"multimodal-generation/generation", "", "text_to_image", 1, map[string]any{"image_count": float64(1)}, map[string]any{"image_count": float64(1)}},
		{"z-image with prompt rewriting", "z-image-turbo", "generate", map[string]any{"prompt": "a cat", "prompt_extend": true},
			"multimodal-generation/generation", "", "text_to_image", 1,
			map[string]any{"image_count": float64(1), "prompt_extend": true}, map[string]any{"image_count": float64(1), "prompt_extend_ratio": float64(2)}},
		{"legacy text-to-image is asynchronous", "wan2.2-t2i-flash", "generate", map[string]any{"prompt": "a cat", "n": 2, "size": "1024x1024"},
			"text2image/image-synthesis", "enable", "text_to_image", 2, map[string]any{"image_count": float64(2)}, map[string]any{"image_count": float64(2)}},
		{"wan2.6 text-to-image is asynchronous by default", "wan2.6-t2i", "generate", map[string]any{"prompt": "a cat", "n": 2},
			"image-generation/generation", "enable", "text_to_image", 2, map[string]any{"image_count": float64(2)}, map[string]any{"image_count": float64(2)}},
		{"qwen edit with an image URL", "qwen-image-edit-plus", "edit", map[string]any{"prompt": "watercolor", "image": "https://cdn.example/input.png"},
			"multimodal-generation/generation", "", "image_to_image", 1, map[string]any{"image_count": float64(1)}, map[string]any{"image_count": float64(1)}},
		{"qwen-image-3.0 edit counts input images", "qwen-image-3.0-pro", "edit", map[string]any{"prompt": "watercolor", "image": []any{"https://cdn.example/1.png", "https://cdn.example/2.png"}, "size": "2048x2048"},
			"multimodal-generation/generation", "", "image_to_image", 1,
			map[string]any{"image_count": float64(1), "output_image_type": "qima_output_2k", "input_image_count": float64(2)}, map[string]any{"image_count": float64(1)}},
		{"wan2.5 legacy edit is asynchronous", "wan2.5-i2i-preview", "edit", map[string]any{"prompt": "watercolor", "image": "https://cdn.example/input.png", "n": 2},
			"image2image/image-synthesis", "enable", "image_to_image", 2, map[string]any{"image_count": float64(2)}, map[string]any{"image_count": float64(2)}},
		{"wanx2.1 function edit is asynchronous", "wanx2.1-imageedit", "edit", map[string]any{"prompt": "make the hair red", "image": "https://cdn.example/input.png", "n": 2},
			"image2image/image-synthesis", "enable", "image_to_image", 2, map[string]any{"image_count": float64(2)}, map[string]any{"image_count": float64(2)}},
		{"provider parameters pass through", "qwen-image-2.0-pro", "generate", map[string]any{"prompt": "a cat", "n": 3, "parameters": map[string]any{"negative_prompt": "blurry", "watermark": false}},
			"multimodal-generation/generation", "", "text_to_image", 3, map[string]any{"image_count": float64(3)}, map[string]any{"image_count": float64(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"model": tc.model}
			maps.Copy(body, tc.body)
			resolved, err := decodeAlibabaImage(t, plugin, tc.operation, body)
			require.NoError(t, err)
			assert.Equal(t, "submit", resolved["kind"])
			assert.Equal(t, tc.model, resolved["model"])
			assert.Equal(t, tc.wantAction, resolved["action"])
			request := resolved["requestBody"].(map[string]any)
			upstreamBody, facts, url := submitAlibabaRequest(t, plugin, tc.model, request)
			assert.Equal(t, base+tc.wantService, url)
			assert.Equal(t, tc.model, upstreamBody["model"])
			parameters := upstreamBody["parameters"].(map[string]any)
			assert.Equal(t, tc.wantN, parameters["n"], "the reserved count is sent explicitly")
			if size, ok := tc.body["size"].(string); ok {
				assert.Equal(t, strings.Replace(size, "x", "*", 1), parameters["size"])
			}
			if extra, ok := tc.body["parameters"].(map[string]any); ok {
				for key, expected := range alibabaObject(t, extra) {
					assert.Equal(t, expected, parameters[key], key)
				}
			}
			assert.Equal(t, tc.wantFacts, facts)
			value, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{"upstreamModel": tc.model, "model": tc.model, "usagePurpose": "billing_ratios", "requestBody": request})
			require.NoError(t, err)
			assert.Equal(t, tc.wantRatios, alibabaObject(t, value))
			value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"upstreamModel": tc.model, "requestBody": request, "baseUrl": "https://dashscope.aliyuncs.com", "apiKey": "k"})
			require.NoError(t, err)
			headers := alibabaObject(t, value)["headers"].(map[string]any)
			if tc.wantAsync == "" {
				assert.NotContains(t, headers, "X-DashScope-Async")
			} else {
				assert.Equal(t, tc.wantAsync, headers["X-DashScope-Async"])
			}
		})
	}

	t.Run("multipart edits address every uploaded file", func(t *testing.T) {
		value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "decodeRequest"}, map[string]any{
			"protocol": "openai_image", "operation": "edit", "model": "qwen-image-edit-plus", "path": "/v1/images/edits", "method": http.MethodPost,
			"body": map[string]any{"kind": "multipart",
				"fields": map[string][]string{"model": {"qwen-image-edit-plus"}, "prompt": {"watercolor"}, "n": {"2"}, "response_format": {"b64_json"}, "watermark": {"false"}},
				"files": []any{
					map[string]any{"ref": "request_file:image[]", "field": "image[]", "filename": "a.png", "mimeType": "image/png", "size": 10},
					map[string]any{"ref": "request_file:image[]#1", "field": "image[]", "filename": "b.jpg", "mimeType": "image/jpeg", "size": 10},
					map[string]any{"ref": "request_file:mask", "field": "mask", "filename": "m.png", "mimeType": "image/png", "size": 10},
				},
			},
		})
		require.NoError(t, err)
		resolved := alibabaObject(t, value)
		assert.Equal(t, "image_to_image", resolved["action"])
		request := resolved["requestBody"].(map[string]any)
		assert.Equal(t, float64(2), request["n"])
		assert.Equal(t, false, request["watermark"])
		images := request["images"].([]any)
		require.Len(t, images, 2, "mask uploads are not reference images")
		assert.Equal(t, map[string]any{"__fileRef": "request_file:image[]", "encoding": "dataUrl", "mimeType": "image/png", "maxBytes": float64(10485760)}, images[0])
		assert.Equal(t, "request_file:image[]#1", images[1].(map[string]any)["__fileRef"])
		driver := map[string]any{"upstreamModel": "qwen-image-edit-plus", "model": "qwen-image-edit-plus", "requestBody": request, "baseUrl": "https://dashscope.aliyuncs.com", "apiKey": "k", "usagePurpose": "facts"}
		value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", driver)
		require.NoError(t, err)
		body := alibabaObject(t, value)["body"].(map[string]any)
		content := body["input"].(map[string]any)["messages"].([]any)[0].(map[string]any)["content"].([]any)
		require.Len(t, content, 3)
		assert.Equal(t, "request_file:image[]#1", content[1].(map[string]any)["image"].(map[string]any)["__fileRef"], "placeholders reach the upstream body for the host to inline")
		assert.Equal(t, "watercolor", content[2].(map[string]any)["text"])
		value, err = plugin.Engine.Call(t.Context(), "extractUsage", driver)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"image_count": float64(2)}, alibabaObject(t, value))
	})

	// wanx2.1-imageedit selects its operation with input.function. OpenAI edit
	// fields map onto the documented body: the image becomes base_image_url, a
	// mask becomes mask_image_url and selects local repainting, and the native
	// route forwards the DashScope body unchanged.
	t.Run("wanx2.1-imageedit maps OpenAI edit fields onto the function API", func(t *testing.T) {
		const model = "wanx2.1-imageedit"
		const image, mask = "https://cdn.example/base.png", "https://cdn.example/mask.png"
		for _, tc := range []struct {
			name           string
			body           map[string]any
			wantInput      map[string]any
			wantParameters map[string]any
		}{
			{"defaults to instruction editing", map[string]any{"prompt": "make the hair red", "image": image},
				map[string]any{"prompt": "make the hair red", "function": "description_edit", "base_image_url": image}, map[string]any{"n": float64(1)}},
			{"a mask selects local repainting", map[string]any{"prompt": "a ceramic rabbit", "image": image, "mask": mask, "n": 2},
				map[string]any{"prompt": "a ceramic rabbit", "function": "description_edit_with_mask", "base_image_url": image, "mask_image_url": mask}, map[string]any{"n": float64(2)}},
			{"explicit function with its parameters", map[string]any{"prompt": "a green fairy", "image": image, "function": "expand", "top_scale": 1.5, "watermark": true},
				map[string]any{"prompt": "a green fairy", "function": "expand", "base_image_url": image}, map[string]any{"n": float64(1), "top_scale": 1.5, "watermark": true}},
		} {
			body := map[string]any{"model": model}
			maps.Copy(body, tc.body)
			resolved, err := decodeAlibabaImage(t, plugin, "edit", body)
			require.NoError(t, err, tc.name)
			upstreamBody, facts, url := submitAlibabaRequest(t, plugin, model, resolved["requestBody"].(map[string]any))
			assert.Equal(t, base+"image2image/image-synthesis", url, tc.name)
			assert.Equal(t, tc.wantInput, upstreamBody["input"], tc.name)
			assert.Equal(t, tc.wantParameters, upstreamBody["parameters"], tc.name)
			assert.Equal(t, map[string]any{"image_count": tc.wantParameters["n"]}, facts, tc.name)
		}

		native, err := plugin.Engine.CallPath(t.Context(), "native", []string{"createImageTask"}, map[string]any{
			"path": "/ali/api/v1/services/aigc/image2image/image-synthesis",
			"body": map[string]any{"kind": "json", "value": map[string]any{"model": model, "input": map[string]any{"function": "super_resolution", "prompt": "sharpen", "base_image_url": image}, "parameters": map[string]any{"upscale_factor": 2, "n": 1}}},
		})
		require.NoError(t, err)
		upstreamBody, facts, url := submitAlibabaRequest(t, plugin, model, alibabaObject(t, native)["requestBody"].(map[string]any))
		assert.Equal(t, base+"image2image/image-synthesis", url)
		assert.Equal(t, map[string]any{"function": "super_resolution", "prompt": "sharpen", "base_image_url": image}, upstreamBody["input"])
		assert.Equal(t, map[string]any{"upscale_factor": float64(2), "n": float64(1)}, upstreamBody["parameters"])
		assert.Equal(t, map[string]any{"image_count": float64(1)}, facts)

		value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "decodeRequest"}, map[string]any{
			"protocol": "openai_image", "operation": "edit", "model": model, "path": "/v1/images/edits", "method": http.MethodPost,
			"body": map[string]any{"kind": "multipart",
				"fields": map[string][]string{"model": {model}, "prompt": {"a ceramic rabbit"}},
				"files": []any{
					map[string]any{"ref": "request_file:image", "field": "image", "filename": "a.png", "mimeType": "image/png", "size": 10},
					map[string]any{"ref": "request_file:mask", "field": "mask", "filename": "m.png", "mimeType": "image/png", "size": 10},
				},
			},
		})
		require.NoError(t, err)
		value, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"upstreamModel": model, "requestBody": alibabaObject(t, value)["requestBody"], "baseUrl": "https://dashscope.aliyuncs.com", "apiKey": "k"})
		require.NoError(t, err)
		input := alibabaObject(t, value)["body"].(map[string]any)["input"].(map[string]any)
		assert.Equal(t, "description_edit_with_mask", input["function"], "a mask upload selects local repainting")
		assert.Equal(t, "request_file:image", input["base_image_url"].(map[string]any)["__fileRef"])
		assert.Equal(t, "request_file:mask", input["mask_image_url"].(map[string]any)["__fileRef"], "the mask placeholder reaches the upstream body for the host to inline")
	})

	for _, tc := range []struct {
		name, model, operation string
		body                   map[string]any
		decodeErr, submitErr   string
	}{
		{"wanx2.1-imageedit takes exactly one image", "wanx2.1-imageedit", "edit", map[string]any{"prompt": "x", "image": []any{"https://cdn.example/1.png", "https://cdn.example/2.png"}}, "", "exactly one input image"},
		{"wanx2.1-imageedit rejects unknown functions", "wanx2.1-imageedit", "edit", map[string]any{"prompt": "x", "image": "https://cdn.example/1.png", "function": "paint"}, "", "function must be one of"},
		{"wanx2.1-imageedit local repainting needs a mask", "wanx2.1-imageedit", "edit", map[string]any{"prompt": "x", "image": "https://cdn.example/1.png", "function": "description_edit_with_mask"}, "", "requires a mask image"},
		{"wanx2.1-imageedit has no size parameter", "wanx2.1-imageedit", "edit", map[string]any{"prompt": "x", "image": "https://cdn.example/1.png", "size": "1024x1024"}, "", "does not accept size"},
		{"wanx2.1-imageedit bounds n", "wanx2.1-imageedit", "edit", map[string]any{"prompt": "x", "image": "https://cdn.example/1.png", "n": 5}, "", "n must be an integer between 1 and 4"},
		{"fixed count model rejects n=2 before reservation", "qwen-image-plus", "generate", map[string]any{"prompt": "a cat", "n": 2}, "", "n must be 1 for this model"},
		{"z-image rejects n=2", "z-image-turbo", "generate", map[string]any{"prompt": "a cat", "n": 2}, "", "n must be 1 for this model"},
		{"qwen-image-3.0 bounds n", "qwen-image-3.0-pro", "generate", map[string]any{"prompt": "a cat", "n": 7}, "", "n must be an integer between 1 and 6"},
		{"legacy bounds n", "wan2.2-t2i-flash", "generate", map[string]any{"prompt": "a cat", "n": 5}, "", "n must be an integer between 1 and 4"},
		{"edit requires an image", "qwen-image-edit-plus", "edit", map[string]any{"prompt": "watercolor"}, "image is required", ""},
		{"edit-only model requires an image on generations", "qwen-image-edit", "generate", map[string]any{"prompt": "watercolor"}, "", "requires at least one input image"},
		{"text-to-image model rejects images", "qwen-image-plus", "edit", map[string]any{"prompt": "a cat", "image": "https://cdn.example/1.png"}, "", "only supports text-to-image input"},
		{"streaming is rejected", "qwen-image-plus", "generate", map[string]any{"prompt": "a cat", "stream": true}, "stream is not supported", ""},
		{"zero count is rejected", "qwen-image-3.0-pro", "generate", map[string]any{"prompt": "a cat", "n": 0}, "n must be a positive integer", ""},
		{"response_format is validated", "qwen-image-plus", "generate", map[string]any{"prompt": "a cat", "response_format": "png"}, "response_format must be url or b64_json", ""},
		{"prompt is required", "qwen-image-plus", "generate", map[string]any{}, "prompt is required", ""},
		{"size bounds follow the model", "z-image-turbo", "generate", map[string]any{"prompt": "a cat", "size": "256x256"}, "", "pixel and aspect-ratio limits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"model": tc.model}
			maps.Copy(body, tc.body)
			resolved, err := decodeAlibabaImage(t, plugin, tc.operation, body)
			if tc.decodeErr != "" {
				require.ErrorContains(t, err, tc.decodeErr)
				return
			}
			require.NoError(t, err)
			request := resolved["requestBody"].(map[string]any)
			_, err = plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"requestBody": request, "upstreamModel": tc.model})
			require.ErrorContains(t, err, tc.submitErr)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.model}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor := taskplugin.New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			c.Set("task_request", request)
			taskErr := adaptor.ValidateRequestAndSetAction(c, info)
			require.NotNil(t, taskErr, "the production validator must reject the request before billing")
			assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
		})
	}
}

// Count derivation for synchronous image results: usage.image_count first,
// then the Qwen-Image-3.0 usage.output_image_count, then image payloads
// flattened across every choice; payload-less parts never count and an
// inconsistent count retains the reservation. The response renderer emits one
// OpenAI data entry per image, so the client sees as many images as are billed.
func TestAlibabaOpenAIImageCountDerivationAndRender(t *testing.T) {
	plugin := newAlibabaPlugin(t)
	const first, second = "https://cdn.example/first.png", "https://cdn.example/second.png"
	twoImagesOneChoice := []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"image": first}, map[string]any{"image": second}}}}}
	for _, tc := range []struct {
		name, model string
		output      map[string]any
		usage       map[string]any
		wantFacts   map[string]any
		wantData    int
		wantErr     bool
	}{
		{"qwen-image-3.0 reports output_image_count", "qwen-image-3.0-pro", map[string]any{"choices": twoImagesOneChoice},
			map[string]any{"output_width": 1024, "output_height": 1024, "input_image_count": 0, "input_image_type": "qima_input_1k", "output_image_count": 2, "output_image_type": "qima_output_1k"},
			map[string]any{"image_count": float64(2), "output_image_type": "qima_output_1k", "input_image_count": float64(0)}, 2, false},
		{"one choice with two images and no usage counts payloads", "qwen-image-2.0-pro", map[string]any{"choices": twoImagesOneChoice}, nil,
			map[string]any{"image_count": float64(2)}, 2, false},
		{"usage.image_count is preferred", "wan2.6-t2i", map[string]any{"choices": twoImagesOneChoice}, map[string]any{"image_count": 2},
			map[string]any{"image_count": float64(2)}, 2, false},
		{"payload-less parts are not counted", "qwen-image-plus", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"text": "revised"}, map[string]any{"image": first}, map[string]any{"image": ""}}}}}}, nil,
			map[string]any{"image_count": float64(1)}, 1, false},
		{"z-image keeps its count", "z-image-turbo", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"image": first}}}}}}, map[string]any{"image_count": 1, "width": 1024, "height": 1536},
			map[string]any{"image_count": float64(1)}, 1, false},
		{"inconsistent zero count retains the reservation", "qwen-image-3.0-pro", map[string]any{"choices": twoImagesOneChoice}, map[string]any{"output_image_count": 0},
			nil, 2, true},
		{"fractional count retains the reservation", "wan2.6-t2i", map[string]any{"choices": twoImagesOneChoice}, map[string]any{"image_count": 1.5},
			nil, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"request_id": "r", "output": tc.output}
			if tc.usage != nil {
				body["usage"] = tc.usage
			}
			request := map[string]any{"model": tc.model, "prompt": "a cat", "n": 2}
			if tc.model == "qwen-image-plus" || tc.model == "z-image-turbo" {
				request["n"] = 1
			}
			if strings.HasPrefix(tc.model, "wan") {
				// Wan models default to the asynchronous service; the synchronous response shape needs the sync mode.
				request["metadata"] = map[string]any{"upstream_mode": "sync"}
			}
			info := &relaycommon.RelayInfo{OriginModelName: tc.model, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.model, ChannelBaseUrl: "https://dashscope.aliyuncs.com"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"}}
			adaptor := taskplugin.New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			c.Set("task_request", request)
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			encoded, err := common.Marshal(body)
			require.NoError(t, err)
			parsed, taskErr := adaptor.ParseResponse(c, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded))}, info)
			require.Nil(t, taskErr)
			require.NotNil(t, parsed.Immediate)
			assert.Equal(t, "SUCCESS", parsed.Immediate.Status)
			assert.Equal(t, "task_public", parsed.UpstreamTaskID, "synchronous results keep the host task id")
			if tc.wantErr {
				assert.Nil(t, parsed.Immediate.UsageFacts, "an unrecognized count must not replace the reservation")
			} else {
				assert.Equal(t, tc.wantFacts, parsed.Immediate.UsageFacts)
				ratios := adaptor.AdjustBillingOnSubmit(info, parsed.TaskData)
				assert.Equal(t, tc.wantFacts["image_count"], ratios["image_count"], "legacy per-call pricing settles to the same count")
			}
			value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "render"}, map[string]any{"model": tc.model}, map[string]any{"task_id": "task_public", "status": "SUCCESS", "created_at": 1700000000, "data": body})
			require.NoError(t, err)
			rendered := alibabaObject(t, value)
			assert.Equal(t, float64(1700000000), rendered["created"])
			data := rendered["data"].([]any)
			require.Len(t, data, tc.wantData)
			assert.Equal(t, first, data[0].(map[string]any)["url"])
			assert.NotContains(t, data[0], "b64_json")
		})
	}

	t.Run("legacy results and Base64 payloads render as OpenAI entries", func(t *testing.T) {
		body := map[string]any{"output": map[string]any{"task_status": "SUCCEEDED", "results": []any{map[string]any{"url": first}, map[string]any{"code": "DataInspectionFailed"}, map[string]any{"url": "data:image/png;base64,QUFB"}}}, "usage": map[string]any{"image_count": 2}}
		value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_image", "render"}, map[string]any{}, map[string]any{"status": "SUCCESS", "created_at": 1, "data": body})
		require.NoError(t, err)
		rendered := alibabaObject(t, value)
		data := rendered["data"].([]any)
		require.Len(t, data, 2)
		assert.Equal(t, map[string]any{"url": first}, data[0])
		assert.Equal(t, map[string]any{"b64_json": "QUFB"}, data[1])
		// metadata keeps the upstream body so clients still see DashScope usage.
		assert.Equal(t, float64(2), rendered["metadata"].(map[string]any)["usage"].(map[string]any)["image_count"])
	})
}
