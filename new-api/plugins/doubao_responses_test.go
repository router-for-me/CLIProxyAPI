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
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	doubaoImageRoute = "/doubao/api/v3/images/generations"
	doubaoBaseURL    = "https://ark.cn-beijing.volces.com"
)

func TestDoubaoResponsesProtocol(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey: "doubao",
		model:     "doubao-seedance-2-0-260128",
		requestBody: map[string]any{
			"model": "doubao-seedance-2-0-260128",
			"input": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "a running fox"},
				map[string]any{"type": "input_image", "image_url": "https://cdn.example/frame.png"},
			}}},
			"seconds": 6,
			"size":    "1920x1080",
		},
		wantAction: "image_to_video",
		wantRequest: map[string]any{
			"model":   "doubao-seedance-2-0-260128",
			"prompt":  "a running fox",
			"images":  []any{"https://cdn.example/frame.png"},
			"seconds": float64(6),
			"metadata": map[string]any{
				"resolution": "1080p",
			},
		},
		wantUsageKeys:  []string{"resolution", "tokens", "video_input"},
		wantVendorName: "doubao",
	})
}

func newDoubaoPlugin(t *testing.T) (*jsplugin.Registry, *jsplugin.LoadedPlugin) {
	t.Helper()
	source, err := builtinplugins.Source("doubao")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "doubao"})
	require.NoError(t, err)
	return registry, plugin
}

func decodeDoubaoImage(t *testing.T, registry *jsplugin.Registry, plugin *jsplugin.LoadedPlugin, body map[string]any) (map[string]any, error) {
	t.Helper()
	binding, found := registry.Generation().LookupDeclaredRoute(http.MethodPost, doubaoImageRoute)
	require.True(t, found)
	require.Equal(t, jsplugin.RouteTypeSubmit, binding.Route.Type)
	value, err := plugin.Engine.CallPath(t.Context(), "native", []string{binding.Route.Decode}, map[string]any{
		"path": doubaoImageRoute, "body": map[string]any{"kind": "json", "value": body},
	})
	if err != nil {
		return nil, err
	}
	return alibabaObject(t, value), nil
}

// Runs the production host validation, body conversion and usage extraction
// for one normalized image request without contacting Volcengine.
func submitDoubaoImage(t *testing.T, plugin *jsplugin.LoadedPlugin, action string, request map[string]any) (map[string]any, map[string]any, string) {
	t.Helper()
	modelName := request["model"].(string)
	// RelayTask copies the decode action onto the relay info before the adaptor runs.
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelBaseUrl: doubaoBaseURL, UpstreamModelName: modelName},
		OriginModelName: modelName,
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public", Action: action},
	}
	adaptor := taskplugin.New(plugin)
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, doubaoImageRoute, nil)
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

func TestDoubaoImageSubmission(t *testing.T) {
	registry, plugin := newDoubaoPlugin(t)
	reference := "https://cdn.example/reference.png"
	noImages := map[string]any{"images_up_to_1_5k": float64(0), "images_above_1_5k": float64(0), "input_images": float64(0), "layer_decomposition": false}
	facts := func(overrides map[string]any) map[string]any {
		merged := map[string]any{}
		maps.Copy(merged, noImages)
		maps.Copy(merged, overrides)
		return merged
	}
	for _, tc := range []struct {
		name       string
		model      string
		body       map[string]any
		wantAction string
		wantBody   map[string]any // upstream fields whose value differs from the request
		wantFacts  map[string]any
	}{
		{"text to image with a preset size", "doubao-seedream-4-0-250828",
			map[string]any{"prompt": "a cat", "size": "2K", "watermark": false, "response_format": "url", "stream": false, "seed": 42, "guidance_scale": 2.5},
			"text_to_image", nil, facts(map[string]any{"images_above_1_5k": float64(1)})},
		{"1K reserves the lower pricing tier", "doubao-seedream-4-0-250828",
			map[string]any{"prompt": "a cat", "size": "1K", "optimize_prompt_options": nil},
			"text_to_image", nil, facts(map[string]any{"images_up_to_1_5k": float64(1)})},
		{"lowercase presets are normalized", "doubao-seedream-4-0-250828",
			map[string]any{"prompt": "a cat", "size": "2k"},
			"text_to_image", map[string]any{"size": "2K"}, facts(map[string]any{"images_above_1_5k": float64(1)})},
		{"pixel sizes are normalized to WxH", "doubao-seedream-4-5-251128",
			map[string]any{"prompt": "a banner", "size": "3750 * 1250"},
			"text_to_image", map[string]any{"size": "3750x1250"}, facts(map[string]any{"images_above_1_5k": float64(1)})},
		{"the 2.61 megapixel boundary belongs to the lower tier", "doubao-seedream-5-0-pro-260628",
			map[string]any{"prompt": "a poster", "size": "1500X1740"},
			"text_to_image", map[string]any{"size": "1500x1740"}, facts(map[string]any{"images_up_to_1_5k": float64(1)})},
		{"5.0 pro 1.5K with fast prompt optimization", "doubao-seedream-5-0-pro-260628",
			map[string]any{"prompt": "a poster", "size": "1.5K", "optimize_prompt_options": map[string]any{"mode": "fast"}, "output_format": "png"},
			"text_to_image", nil, facts(map[string]any{"images_up_to_1_5k": float64(1)})},
		{"group generation estimates max_images and reference images", "doubao-seedream-5-0-lite-260128",
			map[string]any{"prompt": "a brand kit", "image": reference, "size": "2K", "sequential_image_generation": "auto", "sequential_image_generation_options": map[string]any{"max_images": 4}, "output_format": "png", "tools": []any{map[string]any{"type": "web_search"}}},
			"image_to_image", nil, facts(map[string]any{"images_above_1_5k": float64(4), "input_images": float64(1)})},
		{"group generation is capped by reference images", "doubao-seedream-4-5-251128",
			map[string]any{"prompt": "four seasons", "image": []any{reference, "https://cdn.example/second.png"}, "sequential_image_generation": "auto"},
			"image_to_image", nil, facts(map[string]any{"images_above_1_5k": float64(13), "input_images": float64(2)})},
		{"base64 references count as input images", "doubao-seedream-5-0-pro-260628",
			map[string]any{"prompt": "a cat", "image": []any{reference, "data:image/png;base64,iVBORw0KGgo="}, "size": "1K"},
			"image_to_image", nil, facts(map[string]any{"images_up_to_1_5k": float64(1), "input_images": float64(2)})},
		{"transparent background on 5.0 pro", "doubao-seedream-5-0-pro-260628",
			map[string]any{"prompt": "cut out the cat", "image": reference, "background": "transparent", "output_format": "png", "size": "1K"},
			"image_to_image", nil, facts(map[string]any{"images_up_to_1_5k": float64(1), "input_images": float64(1)})},
		{"background is forwarded to other models for upstream to decide", "doubao-seedream-5-0-lite-260128",
			map[string]any{"prompt": "cut out the cat", "background": "transparent", "size": "2K"},
			"text_to_image", nil, facts(map[string]any{"images_above_1_5k": float64(1)})},
		{"layer decomposition with auto size reserves every output at the higher tier", "doubao-seedream-5-0-pro-260628",
			map[string]any{"image": reference, "layer_decomposition": true, "size": "auto"},
			"image_to_image", nil, facts(map[string]any{"images_above_1_5k": float64(17), "input_images": float64(1), "layer_decomposition": true})},
		{"layer decomposition at 1K reserves the lower tier", "doubao-seedream-5-0-pro-260628",
			map[string]any{"image": reference, "layer_decomposition": true, "size": "1k"},
			"image_to_image", map[string]any{"size": "1K"}, facts(map[string]any{"images_up_to_1_5k": float64(17), "input_images": float64(1), "layer_decomposition": true})},
		{"endpoint ids use the permissive profile", "ep-20260918-seedream",
			map[string]any{"prompt": "a cat", "size": "4K", "sequential_image_generation": "auto"},
			"text_to_image", nil, facts(map[string]any{"images_above_1_5k": float64(15)})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"model": tc.model}
			maps.Copy(request, tc.body)
			resolved, err := decodeDoubaoImage(t, registry, plugin, request)
			require.NoError(t, err)
			assert.Equal(t, tc.model, resolved["model"])
			assert.Equal(t, tc.wantAction, resolved["action"])
			body, facts, url := submitDoubaoImage(t, plugin, tc.wantAction, resolved["requestBody"].(map[string]any))
			assert.Equal(t, doubaoBaseURL+"/api/v3/images/generations", url)
			assert.Equal(t, tc.model, body["model"])
			assert.NotContains(t, body, "stream")
			for key, expected := range alibabaObject(t, tc.body) {
				if key == "stream" {
					continue
				}
				if override, ok := tc.wantBody[key]; ok {
					expected = override
				}
				assert.Contains(t, body, key)
				assert.Equal(t, expected, body[key], key)
			}
			assert.Equal(t, tc.wantFacts, facts)
		})
	}

	// Legacy per-call pricing multiplies the price by every ratio, so the
	// reservation ratio is only the total requested output count: one 1K image
	// with two reference images reserves price × 1, not price × 1 × 2.
	t.Run("legacy per-call pricing reserves only the total output count", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			model string
			body  map[string]any
			want  float64
		}{
			{"1K image with two reference images", "doubao-seedream-5-0-pro-260628", map[string]any{"prompt": "a cat", "image": []any{reference, "https://cdn.example/second.png"}, "size": "1K"}, 1},
			{"group generation", "doubao-seedream-5-0-lite-260128", map[string]any{"prompt": "a brand kit", "image": reference, "size": "2K", "sequential_image_generation": "auto", "sequential_image_generation_options": map[string]any{"max_images": 4}}, 4},
			{"layer decomposition", "doubao-seedream-5-0-pro-260628", map[string]any{"image": reference, "layer_decomposition": true, "size": "auto"}, 17},
		} {
			request := map[string]any{"model": tc.model}
			maps.Copy(request, tc.body)
			resolved, err := decodeDoubaoImage(t, registry, plugin, request)
			require.NoError(t, err, tc.name)
			ctx := map[string]any{"upstreamModel": tc.model, "model": tc.model, "action": resolved["action"], "requestBody": resolved["requestBody"]}
			ctx["usagePurpose"] = "billing_ratios"
			value, err := plugin.Engine.Call(t.Context(), "extractUsage", ctx)
			require.NoError(t, err, tc.name)
			assert.Equal(t, map[string]any{"image_count": tc.want}, alibabaObject(t, value), tc.name)
			ctx["usagePurpose"] = "facts"
			value, err = plugin.Engine.Call(t.Context(), "extractUsage", ctx)
			require.NoError(t, err, tc.name)
			facts := alibabaObject(t, value)
			assert.NotContains(t, facts, "image_count", tc.name)
			assert.Equal(t, tc.want, facts["images_up_to_1_5k"].(float64)+facts["images_above_1_5k"].(float64), tc.name)
		}
	})

	t.Run("image models are Responses-only host protocol models", func(t *testing.T) {
		for _, name := range []string{"doubao-seedream-5-0-pro-260628", "doubao-seedream-5-0-lite-260128", "doubao-seedream-4-5-251128", "doubao-seedream-4-0-250828"} {
			_, found := registry.Generation().LookupEndpoint(http.MethodPost, "/v1/responses", name)
			assert.True(t, found, name)
			_, found = registry.Generation().LookupEndpoint(http.MethodPost, "/v1/videos", name)
			assert.False(t, found, name)
			schema, _ := plugin.Meta.UsageForModel(name)
			assert.ElementsMatch(t, []string{"images_up_to_1_5k", "images_above_1_5k", "input_images", "layer_decomposition"}, keysOf(schema), name)
		}
		_, found := registry.Generation().LookupEndpoint(http.MethodPost, "/v1/videos", "doubao-seedance-2-0-260128")
		assert.True(t, found)
		schema, _ := plugin.Meta.UsageForModel("doubao-seedance-2-0-260128")
		assert.ElementsMatch(t, []string{"tokens", "resolution", "video_input"}, keysOf(schema))
		schema, examples := plugin.Meta.UsageForModel("doubao-seedance-1-5-pro-251215")
		assert.ElementsMatch(t, []string{"tokens", "resolution", "generate_audio"}, keysOf(schema))
		assert.Equal(t, "boolean", schema["generate_audio"].Type)
		assert.NotEmpty(t, examples)
	})
}

// Capability profiles follow the Ark model list: pricing lists only the
// resolutions each Seedance model offers, reference video input only on
// Seedance 2.x, and audio output only on Seedance 1.5 pro.
func TestDoubaoSeedanceUsageFacts(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	const (
		pro10  = "doubao-seedance-1-0-pro-250528"
		pro15  = "doubao-seedance-1-5-pro-251215"
		v20    = "doubao-seedance-2-0-260128"
		fast20 = "doubao-seedance-2-0-fast-260128"
		mini20 = "doubao-seedance-2-0-mini-260615"
		v25    = "doubao-seedance-2-5-260628"
	)
	families := []struct {
		models      []string
		resolutions []string
		fields      []string
	}{
		{[]string{pro10, "doubao-seedance-1-0-lite-t2v", "doubao-seedance-1-0-lite-i2v"}, []string{"480p", "720p", "1080p"}, []string{"resolution", "tokens"}},
		{[]string{pro15}, []string{"480p", "720p", "1080p"}, []string{"generate_audio", "resolution", "tokens"}},
		{[]string{v20}, []string{"480p", "720p", "1080p", "4k"}, []string{"resolution", "tokens", "video_input"}},
		{[]string{fast20, mini20}, []string{"480p", "720p"}, []string{"resolution", "tokens", "video_input"}},
		{[]string{v25}, []string{"480p", "720p", "1080p"}, []string{"resolution", "tokens", "video_input"}},
	}
	profiled := make([]string, 0, len(plugin.Meta.Models))
	for _, family := range families {
		for _, name := range family.models {
			profiled = append(profiled, name)
			t.Run(name, func(t *testing.T) {
				schema, examples := plugin.Meta.UsageForModel(name)
				assert.Equal(t, family.fields, slices.Sorted(maps.Keys(schema)))
				assert.Equal(t, family.resolutions, schema["resolution"].Enum)
				require.NotEmpty(t, examples)
				for _, example := range examples {
					assert.Equal(t, family.fields, slices.Sorted(maps.Keys(example.Facts)), example.Label)
					assert.Contains(t, family.resolutions, example.Facts["resolution"], example.Label)
				}
			})
		}
	}
	videoModels := make([]string, 0, len(profiled))
	for _, name := range plugin.Meta.Models {
		if strings.Contains(name, "seedance") {
			videoModels = append(videoModels, name)
		}
	}
	assert.ElementsMatch(t, videoModels, profiled, "every Seedance model selects a capability profile")

	for _, tc := range []struct {
		name    string
		model   string
		request map[string]any
		want    map[string]any
		wantErr string
	}{
		{"1.5 pro defaults to audio", pro15, map[string]any{"metadata": map[string]any{"resolution": "720p"}},
			map[string]any{"tokens": float64(108000), "resolution": "720p", "generate_audio": true}, ""},
		{"1.5 pro silent output", pro15, map[string]any{"metadata": map[string]any{"resolution": "720p", "generate_audio": false}},
			map[string]any{"tokens": float64(108000), "resolution": "720p", "generate_audio": false}, ""},
		{"2.0 has no audio fact", v20, map[string]any{"metadata": map[string]any{"resolution": "720p", "generate_audio": false}},
			map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"}, ""},
		{"2.0 offers 4k", v20, map[string]any{"metadata": map[string]any{"resolution": "4k"}},
			map[string]any{"tokens": float64(972000), "resolution": "4k", "video_input": "none"}, ""},
		{"1.0 pro has no reference video fact", pro10, map[string]any{"metadata": map[string]any{"resolution": "1080p"}},
			map[string]any{"tokens": float64(243000), "resolution": "1080p"}, ""},
		{"mini reserves its highest tier when the resolution is left to Ark", mini20, map[string]any{},
			map[string]any{"tokens": float64(108000), "resolution": "720p", "video_input": "none"}, ""},
		{"mini rejects 1080p", mini20, map[string]any{"metadata": map[string]any{"resolution": "1080p"}}, nil, mini20 + " resolution must be one of 480p, 720p"},
		{"mini rejects a 1080p size", mini20, map[string]any{"size": "1920x1080"}, nil, mini20 + " resolution must be one of 480p, 720p"},
		{"fast rejects 4k", fast20, map[string]any{"metadata": map[string]any{"resolution": "4k"}}, nil, fast20 + " resolution must be one of 480p, 720p"},
		{"2.5 rejects 4k", v25, map[string]any{"metadata": map[string]any{"resolution": "4K"}}, nil, v25 + " resolution must be one of 480p, 720p, 1080p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"model": tc.model, "prompt": "a cat", "seconds": float64(5)}
			maps.Copy(request, tc.request)
			info := &relaycommon.RelayInfo{
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelBaseUrl: doubaoBaseURL, UpstreamModelName: tc.model},
				OriginModelName: tc.model,
				TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public", Action: "text_to_video"},
			}
			adaptor := taskplugin.New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			c.Set("task_request", request)
			taskErr := adaptor.ValidateRequestAndSetAction(c, info)
			if tc.wantErr != "" {
				require.NotNil(t, taskErr)
				assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
				assert.Contains(t, taskErr.Message, tc.wantErr)
				return
			}
			require.Nil(t, taskErr)
			reader, err := adaptor.BuildRequestBody(c, info)
			require.NoError(t, err)
			encoded, err := io.ReadAll(reader)
			require.NoError(t, err)
			var body map[string]any
			require.NoError(t, common.Unmarshal(encoded, &body))
			metadata, _ := tc.request["metadata"].(map[string]any)
			assert.Equal(t, metadata["generate_audio"], body["generate_audio"])
			facts, err := adaptor.ExtractUsageFactsValidated(c, info)
			require.NoError(t, err)
			assert.Equal(t, tc.want, alibabaObject(t, facts))
		})
	}

	t.Run("completion overlays only resolutions the model offers", func(t *testing.T) {
		for _, tc := range []struct {
			model      string
			resolution string
			want       map[string]any
		}{
			{mini20, "1080p", map[string]any{"tokens": float64(90000)}},
			{v25, "4k", map[string]any{"tokens": float64(90000)}},
			{v20, "4k", map[string]any{"tokens": float64(90000), "resolution": "4k"}},
		} {
			queryContext := map[string]any{"model": tc.model, "upstreamModel": tc.model, "action": "text_to_video"}
			body := map[string]any{
				"status":  "succeeded",
				"usage":   map[string]any{"completion_tokens": 90000},
				"content": map[string]any{"video_url": "https://cdn.example/video.mp4", "resolution": tc.resolution},
			}
			value, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", queryContext, map[string]any{"status": "SUCCESS"}, body)
			require.NoError(t, err, tc.model)
			assert.Equal(t, tc.want, alibabaObject(t, value), tc.model)
		}
	})
}

func keysOf(schema map[string]jsplugin.UsageFieldSchema) []string {
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	return keys
}

// Rejections mirror the Ark API reference; the model-capability cases were
// confirmed against live 400 responses (2026-09).
func TestDoubaoImageValidation(t *testing.T) {
	registry, plugin := newDoubaoPlugin(t)
	const pro, lite, v45, v40 = "doubao-seedream-5-0-pro-260628", "doubao-seedream-5-0-lite-260128", "doubao-seedream-4-5-251128", "doubao-seedream-4-0-250828"
	reference := "https://cdn.example/1.png"
	manyReferences := make([]any, 0, 11)
	for index := range 11 {
		manyReferences = append(manyReferences, fmt.Sprintf("https://cdn.example/%d.png", index))
	}
	for _, tc := range []struct {
		name    string
		model   string
		body    map[string]any
		wantErr string
	}{
		{"prompt is required", v40, map[string]any{}, "prompt is required"},
		{"client streaming is rejected", v40, map[string]any{"prompt": "a cat", "stream": true}, "stream is not supported"},
		{"base64 responses are not deliverable", v40, map[string]any{"prompt": "a cat", "response_format": "b64_json"}, "response_format must be url"},
		{"5.0 pro rejects group generation", pro, map[string]any{"prompt": "a cat", "sequential_image_generation": "auto"}, "sequential_image_generation is not supported"},
		{"5.0 pro rejects sequential_image_generation even when disabled", pro, map[string]any{"prompt": "a cat", "sequential_image_generation": "disabled"}, "sequential_image_generation is not supported"},
		{"sequential_image_generation is an enum", lite, map[string]any{"prompt": "a cat", "sequential_image_generation": "on"}, "must be auto or disabled"},
		{"sequential options must be an object", lite, map[string]any{"prompt": "a cat", "sequential_image_generation": "auto", "sequential_image_generation_options": 3}, "sequential_image_generation_options must be an object"},
		{"max_images is bounded", lite, map[string]any{"prompt": "a cat", "sequential_image_generation": "auto", "sequential_image_generation_options": map[string]any{"max_images": 16}}, "max_images must be an integer between 1 and 15"},
		{"reference images are bounded", pro, map[string]any{"prompt": "a cat", "image": manyReferences}, "at most 10 reference images"},
		{"fast prompt optimization is unsupported on 5.0 lite", lite, map[string]any{"prompt": "a cat", "optimize_prompt_options": map[string]any{"mode": "fast"}}, "mode fast is not supported"},
		{"fast prompt optimization is unsupported on 4.5", v45, map[string]any{"prompt": "a cat", "optimize_prompt_options": map[string]any{"mode": "fast"}}, "mode fast is not supported"},
		{"prompt optimization mode is an enum", v40, map[string]any{"prompt": "a cat", "optimize_prompt_options": map[string]any{"mode": "turbo"}}, "must be standard or fast"},
		{"prompt optimization options must be an object", v40, map[string]any{"prompt": "a cat", "optimize_prompt_options": 123}, "optimize_prompt_options must be an object"},
		{"output_format is an enum", pro, map[string]any{"prompt": "a cat", "output_format": "webp"}, "output_format must be png or jpeg"},
		{"output_format follows the model", v40, map[string]any{"prompt": "a cat", "output_format": "png"}, "output_format is not supported"},
		{"background is a string enum", pro, map[string]any{"prompt": "a cat", "image": reference, "background": true}, "background must be opaque or transparent"},
		{"transparent background needs exactly one input image", pro, map[string]any{"prompt": "a cat", "background": "transparent"}, "requires exactly one input image"},
		{"transparent background rejects jpeg output", pro, map[string]any{"prompt": "a cat", "image": reference, "background": "transparent", "output_format": "jpeg"}, "cannot be combined with output_format jpeg"},
		{"tools must be objects with a type", lite, map[string]any{"prompt": "a cat", "tools": []any{map[string]any{}}}, "tools must be an array of objects with a string type"},
		{"tools must be an array", lite, map[string]any{"prompt": "a cat", "tools": map[string]any{"type": "web_search"}}, "tools must be an array of objects with a string type"},
		{"tools follow the model", pro, map[string]any{"prompt": "a cat", "tools": []any{map[string]any{"type": "web_search"}}}, "tools are not supported"},
		{"seed must be an integer", v40, map[string]any{"prompt": "a cat", "seed": "abc"}, "seed must be an integer"},
		{"seed rejects fractions", v40, map[string]any{"prompt": "a cat", "seed": 1.5}, "seed must be an integer"},
		{"guidance_scale must be a number", v40, map[string]any{"prompt": "a cat", "guidance_scale": "x"}, "guidance_scale must be a number"},
		{"size must be a string", v40, map[string]any{"prompt": "a cat", "size": 2048}, "size must be a string"},
		{"presets follow the model", v45, map[string]any{"prompt": "a cat", "size": "1K"}, "size must be one of 2K, 4K"},
		{"pixel sizes follow the model", v45, map[string]any{"prompt": "a cat", "size": "1500x1500"}, "outside the model's pixel"},
		{"aspect ratios are bounded", v40, map[string]any{"prompt": "a cat", "size": "8192x480"}, "outside the model's pixel"},
		{"layer decomposition needs one image", pro, map[string]any{"layer_decomposition": true}, "requires exactly one input image"},
		{"layer decomposition follows the model", v40, map[string]any{"prompt": "a cat", "image": reference, "layer_decomposition": true}, "layer_decomposition is not supported"},
		{"layer decomposition sizes are presets", pro, map[string]any{"image": reference, "layer_decomposition": true, "size": "2048x2048"}, "layer decomposition sizes must be one of"},
		{"auto size needs layer decomposition", v40, map[string]any{"prompt": "a cat", "size": "auto"}, "size auto is only supported"},
		{"image references must be URLs", v40, map[string]any{"prompt": "a cat", "image": "ftp://cdn.example/1.png"}, "image must be an HTTP URL"},
		{"base64 references need the full data URI", v40, map[string]any{"prompt": "a cat", "image": "data:image/png,iVBORw0KGgo="}, "image must be an HTTP URL"},
		{"base64 formats are lowercase", v40, map[string]any{"prompt": "a cat", "image": "data:image/PNG;base64,iVBORw0KGgo="}, "image must be an HTTP URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"model": tc.model}
			maps.Copy(request, tc.body)
			_, err := decodeDoubaoImage(t, registry, plugin, request)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// Fixtures mirror real Ark responses captured on 2026-09-18 (signed URLs replaced).
func TestDoubaoImageResults(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	const lite, pro = "doubao-seedream-5-0-lite-260128", "doubao-seedream-5-0-pro-260628"
	first, second := "https://ark-content.example/1.jpeg?sig=secret", "https://ark-content.example/2.jpeg?sig=secret"
	// 5.0 lite group of two plus one failed member; Ark echoes the canonical model name.
	groupBody := map[string]any{
		"model":   "doubao-seedream-5-0-260128",
		"created": 1789733151,
		"data": []any{
			map[string]any{"url": first, "size": "2848x1600"},
			map[string]any{"url": second, "size": "2848x1600"},
			map[string]any{"error": map[string]any{"code": "OutputImageSensitiveContentDetected", "message": "blocked"}},
		},
		"usage": map[string]any{"generated_images": 2, "output_tokens": 35600, "total_tokens": 35600},
	}
	// 5.0 pro layer decomposition with size auto: base and first layer above 2.61 MP, second layer below.
	layerBody := map[string]any{
		"model":   pro,
		"created": 1789733460,
		"data": []any{
			map[string]any{"url": "https://ark-content.example/base.jpeg?sig=secret", "size": "2848x1600", "output_format": "jpeg", "z_index": 0},
			map[string]any{"url": "https://ark-content.example/layer1.png?sig=secret", "size": "2848x1600", "output_format": "png", "z_index": 1, "name": "背景场景", "description": "室内木桌背景",
				"bounding_box": map[string]any{"absolute": []any{0, 0, 2848, 1600}, "normalized": []any{0, 0, 1000, 999}}},
			map[string]any{"url": "https://ark-content.example/layer2.png?sig=secret", "size": "1125x956", "output_format": "png", "z_index": 2, "name": "主体橘猫", "description": "坐姿橘猫",
				"bounding_box": map[string]any{"absolute": []any{955, 622, 2080, 1578}, "normalized": []any{335, 389, 730, 986}}},
		},
		"usage": map[string]any{"input_images": 1, "generated_images": 3, "output_tokens": 42316, "total_tokens": 42316},
	}
	// 5.0 pro single 1K image from two reference images.
	referenceBody := map[string]any{
		"model":   pro,
		"created": 1789733390,
		"data":    []any{map[string]any{"url": first, "size": "1424x800", "output_format": "jpeg"}},
		"usage":   map[string]any{"input_images": 2, "generated_images": 1, "output_tokens": 4450, "total_tokens": 4450},
	}

	parseResponse := func(t *testing.T, model string, request map[string]any, payload map[string]any) *relaycommon.TaskInfo {
		t.Helper()
		info := &relaycommon.RelayInfo{OriginModelName: model, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model, ChannelBaseUrl: doubaoBaseURL}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"}}
		adaptor := taskplugin.New(plugin)
		adaptor.Init(info)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, doubaoImageRoute, nil)
		c.Set("task_request", request)
		encoded, err := common.Marshal(payload)
		require.NoError(t, err)
		parsed, taskErr := adaptor.ParseResponse(c, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(encoded))}, info)
		require.Nil(t, taskErr)
		require.NotNil(t, parsed.Immediate)
		assert.Equal(t, "task_public", parsed.UpstreamTaskID)
		assert.JSONEq(t, string(encoded), string(parsed.TaskData))
		return parsed.Immediate
	}
	groupRequest := map[string]any{"model": lite, "prompt": "a cat", "sequential_image_generation": "auto"}
	queryContext := map[string]any{"upstreamModel": lite, "model": lite, "action": "text_to_image"}

	t.Run("uniform group settles every image at its own tier", func(t *testing.T) {
		immediate := parseResponse(t, lite, groupRequest, groupBody)
		assert.Equal(t, "SUCCESS", immediate.Status)
		assert.Equal(t, "100%", immediate.Progress)
		assert.Equal(t, first, immediate.Url)
		assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(0), "images_above_1_5k": float64(2)}, immediate.UsageFacts)

		value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"imageCreated"}, map[string]any{}, map[string]any{"task_id": "task_public", "status": "SUCCESS", "data": groupBody})
		require.NoError(t, err)
		assert.Equal(t, alibabaObject(t, groupBody), alibabaObject(t, value))

		value, err = plugin.Engine.Call(t.Context(), "extractUsageOnSubmit", queryContext, groupBody)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"image_count": float64(2)}, alibabaObject(t, value), "legacy per-call pricing settles on the delivered count")
	})

	// Legacy per-call pricing multiplies the price by every ratio the plugin
	// returns, so the submit-time settlement carries only the total delivered
	// output count; a mixed-tier result with a reference image must settle 5
	// images, not price × 4 × 1 × 1.
	t.Run("legacy per-call pricing settles the total output count", func(t *testing.T) {
		tieredBody := map[string]any{
			"model": pro,
			"data": []any{
				map[string]any{"url": first, "size": "1024x1024"},
				map[string]any{"url": second, "size": "1024x1024"},
				map[string]any{"url": first, "size": "1024x1024"},
				map[string]any{"url": second, "size": "1024x1024"},
				map[string]any{"url": first, "size": "2848x1600"},
			},
			"usage": map[string]any{"input_images": 1, "generated_images": 5},
		}
		value, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", queryContext, map[string]any{"status": "SUCCESS"}, tieredBody)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(4), "images_above_1_5k": float64(1), "input_images": float64(1)}, alibabaObject(t, value), "task expressions keep the tiered facts")
		for _, tc := range []struct {
			name string
			body map[string]any
			want float64
		}{
			{"mixed tiers with a reference image", tieredBody, 5},
			{"layer decomposition", layerBody, 3},
			{"single image from two reference images", referenceBody, 1},
		} {
			value, err := plugin.Engine.Call(t.Context(), "extractUsageOnSubmit", queryContext, tc.body)
			require.NoError(t, err, tc.name)
			assert.Equal(t, map[string]any{"image_count": tc.want}, alibabaObject(t, value), tc.name)
		}
		value, err = plugin.Engine.Call(t.Context(), "extractUsageOnSubmit", queryContext, map[string]any{"data": []any{map[string]any{"error": map[string]any{"code": "x"}}}})
		require.NoError(t, err)
		assert.Empty(t, alibabaObject(t, value), "no delivered image leaves the reservation in place")
	})

	t.Run("mixed-tier layers settle per layer with the input image count", func(t *testing.T) {
		request := map[string]any{"model": pro, "image": "https://cdn.example/photo.png", "layer_decomposition": true, "size": "auto"}
		immediate := parseResponse(t, pro, request, layerBody)
		assert.Equal(t, "SUCCESS", immediate.Status)
		assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(1), "images_above_1_5k": float64(2), "input_images": float64(1)}, immediate.UsageFacts)
	})

	t.Run("reference images settle from usage.input_images", func(t *testing.T) {
		request := map[string]any{"model": pro, "prompt": "a cat", "image": []any{"https://cdn.example/a.png", "https://cdn.example/b.png", "https://cdn.example/c.png"}, "size": "1K"}
		immediate := parseResponse(t, pro, request, referenceBody)
		assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(1), "images_above_1_5k": float64(0), "input_images": float64(2)}, immediate.UsageFacts)
	})

	t.Run("invalid completion counts retain the reservation", func(t *testing.T) {
		for _, count := range []any{-1, 0, 1.5, 99, "2", 3} {
			payload := map[string]any{"data": []any{map[string]any{"url": first, "size": "2048x2048"}}, "usage": map[string]any{"generated_images": count}}
			immediate := parseResponse(t, lite, groupRequest, payload)
			assert.Equal(t, "SUCCESS", immediate.Status)
			assert.Nil(t, immediate.UsageFacts, "count %v must not replace the reservation", count)
		}
	})

	t.Run("invalid input image counts keep the estimate while tiers settle", func(t *testing.T) {
		for _, count := range []any{-1, 1.5, 15, "2"} {
			payload := map[string]any{"data": []any{map[string]any{"url": first, "size": "1424x800"}}, "usage": map[string]any{"generated_images": 1, "input_images": count}}
			value, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", queryContext, map[string]any{"status": "SUCCESS"}, payload)
			require.NoError(t, err)
			assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(1), "images_above_1_5k": float64(0)}, alibabaObject(t, value), "input_images %v", count)
		}
	})

	t.Run("missing sizes keep the estimated tiers", func(t *testing.T) {
		payload := map[string]any{"data": []any{map[string]any{"url": first, "size": "2048x2048"}, map[string]any{"url": second}}, "usage": map[string]any{"generated_images": 2, "input_images": 1}}
		value, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", queryContext, map[string]any{"status": "SUCCESS"}, payload)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"input_images": float64(1)}, alibabaObject(t, value))
	})

	t.Run("missing usage settles from image payload sizes", func(t *testing.T) {
		payload := map[string]any{"data": []any{map[string]any{"url": first, "size": "4096x4096"}, map[string]any{"url": second, "size": "1024x1024"}, map[string]any{"error": map[string]any{"code": "x"}}}}
		value, err := plugin.Engine.Call(t.Context(), "extractUsageOnComplete", queryContext, map[string]any{"status": "SUCCESS"}, payload)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(1), "images_above_1_5k": float64(1)}, alibabaObject(t, value))
	})

	t.Run("failed and empty results do not complete a task", func(t *testing.T) {
		for _, payload := range []map[string]any{
			{"error": map[string]any{"code": "InvalidParameter", "message": "The parameter `seed` specified in the request is not valid. Request id: 0217", "param": "", "type": ""}},
			{"data": []any{map[string]any{"error": map[string]any{"code": "OutputImageSensitiveContentDetected", "message": "blocked"}}}, "usage": map[string]any{"generated_images": 0}},
			{"data": []any{}},
		} {
			_, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"upstreamModel": lite, "publicTaskId": "task_public", "requestBody": map[string]any{"model": lite, "prompt": "a cat"}}, map[string]any{"statusCode": 200, "body": payload})
			require.Error(t, err)
		}
	})

	t.Run("artifacts and Responses output use gateway URLs", func(t *testing.T) {
		value, err := plugin.Engine.Call(t.Context(), "listArtifacts", map[string]any{"taskId": "task_public", "status": "SUCCESS", "action": "text_to_image", "data": groupBody})
		require.NoError(t, err)
		encoded, err := common.Marshal(value)
		require.NoError(t, err)
		var artifacts []map[string]any
		require.NoError(t, common.Unmarshal(encoded, &artifacts))
		require.Len(t, artifacts, 2)
		assert.Equal(t, "image-1", artifacts[0]["key"])
		assert.Equal(t, "image", artifacts[0]["type"])
		assert.Equal(t, "image-2", artifacts[1]["key"])

		value, err = plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{"artifactKey": "image-2", "action": "text_to_image", "data": groupBody, "clientRequest": map[string]any{"method": "GET"}})
		require.NoError(t, err)
		content := alibabaObject(t, value)
		assert.Equal(t, second, content["url"])
		assert.Equal(t, true, content["credentialless"])

		gateway := map[string]any{
			"model": lite,
			"artifacts": map[string]any{
				"image-1": map[string]any{"key": "image-1", "type": "image", "url": "https://gateway.example/v1/tasks/task_public/artifacts/image-1/content?access=a"},
				"image-2": map[string]any{"key": "image-2", "type": "image", "url": "https://gateway.example/v1/tasks/task_public/artifacts/image-2/content?access=b"},
			},
		}
		task := map[string]any{"task_id": "task_public", "status": "SUCCESS", "progress": "100%", "created_at": 10, "updated_at": 20, "data": groupBody}
		value, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"}, gateway, task)
		require.NoError(t, err)
		machine := relay.NewPluginResponsesMachine("task_public", lite, 10, relay.DefaultPluginProtocolLimits())
		response, err := machine.FinalResponse(value, "SUCCESS")
		require.NoError(t, err)
		text := response["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		assert.Contains(t, text, "artifacts/image-1/content")
		assert.Contains(t, text, "artifacts/image-2/content")
		assert.NotContains(t, text, "ark-content.example")

		_, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "renderFinal"}, map[string]any{"model": lite}, task)
		require.ErrorContains(t, err, "image artifact is unavailable")
	})
}

func TestDoubaoImageResponsesDecode(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	const model = "doubao-seedream-5-0-lite-260128"
	decode := func(t *testing.T, body map[string]any) (map[string]any, error) {
		t.Helper()
		value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
			"model": body["model"], "stream": false, "body": map[string]any{"kind": "json", "value": body},
		})
		if err != nil {
			return nil, err
		}
		return alibabaObject(t, value), nil
	}

	resolved, err := decode(t, map[string]any{
		"model": model,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "a poster"},
			map[string]any{"type": "input_image", "image_url": "https://cdn.example/ref.png"},
		}}},
		"size":                                "2K",
		"sequential_image_generation":         "auto",
		"sequential_image_generation_options": map[string]any{"max_images": 3},
		"watermark":                           false,
		// Host-owned background execution flag: must never become Ark's transparency option.
		"background": true,
		// Only Ark's web_search tool is forwarded, stripped to its type; function tools are dropped.
		"tools": []any{
			map[string]any{"type": "web_search", "search_context_size": "low"},
			map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{}},
		},
		"metadata": map[string]any{"ignored": true},
	})
	require.NoError(t, err)
	assert.Equal(t, model, resolved["model"])
	assert.Equal(t, "image_to_image", resolved["action"])
	request := resolved["requestBody"].(map[string]any)
	assert.Equal(t, map[string]any{
		"model": model, "prompt": "a poster", "image": []any{"https://cdn.example/ref.png"},
		"size": "2K", "sequential_image_generation": "auto", "sequential_image_generation_options": map[string]any{"max_images": float64(3)}, "watermark": false,
		"tools": []any{map[string]any{"type": "web_search"}},
	}, request)
	body, facts, url := submitDoubaoImage(t, plugin, "image_to_image", request)
	assert.Equal(t, doubaoBaseURL+"/api/v3/images/generations", url)
	assert.Equal(t, []any{"https://cdn.example/ref.png"}, body["image"])
	assert.NotContains(t, body, "background")
	assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(0), "images_above_1_5k": float64(3), "input_images": float64(1), "layer_decomposition": false}, facts)

	t.Run("function-only tools and background never reach a model without tool support", func(t *testing.T) {
		resolved, err := decode(t, map[string]any{"model": "doubao-seedream-4-0-250828", "input": "a cat", "background": true, "tools": []any{map[string]any{"type": "function", "name": "lookup"}}})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"model": "doubao-seedream-4-0-250828", "prompt": "a cat"}, resolved["requestBody"])
	})

	_, err = decode(t, map[string]any{"model": model, "input": "a cat", "response_format": "b64_json"})
	require.ErrorContains(t, err, "response_format must be url")
}

// Channel model mapping may send a declared Seedream model to an Ark endpoint ID.
// The declared model still drives capability checks and the image usage profile.
func TestDoubaoImageEndpointMappingKeepsDeclaredModel(t *testing.T) {
	_, plugin := newDoubaoPlugin(t)
	const declared, endpoint = "doubao-seedream-5-0-pro-260628", "ep-20260918-seedream"
	for _, tc := range []struct {
		name    string
		body    map[string]any
		wantErr string
	}{
		{"declared capabilities still apply", map[string]any{"prompt": "a cat", "sequential_image_generation": "auto"}, "sequential_image_generation is not supported"},
		{"the endpoint receives the request", map[string]any{"prompt": "a cat", "size": "2K"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"model": declared}
			maps.Copy(request, tc.body)
			info := &relaycommon.RelayInfo{
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelBaseUrl: doubaoBaseURL, UpstreamModelName: endpoint},
				OriginModelName: declared,
				TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public", Action: "text_to_image"},
			}
			adaptor := taskplugin.New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, doubaoImageRoute, nil)
			c.Set("task_request", request)
			taskErr := adaptor.ValidateRequestAndSetAction(c, info)
			if tc.wantErr != "" {
				require.NotNil(t, taskErr)
				assert.Equal(t, "plugin_request_invalid", taskErr.Code)
				assert.Contains(t, taskErr.Message, tc.wantErr)
				return
			}
			require.Nil(t, taskErr)
			reader, err := adaptor.BuildRequestBody(c, info)
			require.NoError(t, err)
			encoded, err := io.ReadAll(reader)
			require.NoError(t, err)
			var body map[string]any
			require.NoError(t, common.Unmarshal(encoded, &body))
			assert.Equal(t, endpoint, body["model"])
			facts, err := adaptor.ExtractUsageFactsValidated(c, info)
			require.NoError(t, err)
			assert.Equal(t, map[string]any{"images_up_to_1_5k": float64(0), "images_above_1_5k": float64(1), "input_images": float64(0), "layer_decomposition": false}, alibabaObject(t, facts))
		})
	}
}
