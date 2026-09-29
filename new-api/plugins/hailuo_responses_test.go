package plugins_test

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay/channel"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHailuoResponsesProtocol(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey: "hailuo",
		model:     "MiniMax-Hailuo-2.3",
		requestBody: map[string]any{
			"model": "MiniMax-Hailuo-2.3",
			"input": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "ocean at sunset"},
				map[string]any{"type": "input_image", "image_url": "https://cdn.example/frame.png"},
			}}},
			"seconds": 10,
			"size":    "1920x1080",
		},
		wantAction: "image_to_video",
		wantRequest: map[string]any{
			"model":    "MiniMax-Hailuo-2.3",
			"prompt":   "ocean at sunset",
			"images":   []any{"https://cdn.example/frame.png"},
			"duration": float64(10),
			"size":     "1920x1080",
			"metadata": map[string]any{"first_frame_image": "https://cdn.example/frame.png"},
		},
		wantUsageKeys:  []string{"input_images", "input_video_seconds", "resolution", "seconds"},
		wantVendorName: "hailuo",
	})
}

// Every model selects only the dimensions it can actually bill, while
// submission still reports the union so prices saved against the previous
// schema keep evaluating.
func TestHailuoUsageProfilesPerModel(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	families := []struct {
		models      []string
		fields      []string
		resolutions []string
	}{
		{
			models:      []string{"MiniMax-H3"},
			fields:      []string{"input_images", "input_video_seconds", "resolution", "seconds"},
			resolutions: []string{"768P", "2K"},
		},
		{
			models:      []string{"MiniMax-Hailuo-2.3", "MiniMax-Hailuo-2.3-Fast"},
			fields:      []string{"resolution", "seconds"},
			resolutions: []string{"768P", "1080P"},
		},
		{
			models:      []string{"MiniMax-Hailuo-02"},
			fields:      []string{"resolution", "seconds"},
			resolutions: []string{"512P", "768P", "1080P"},
		},
		{
			models: []string{"T2V-01-Director", "T2V-01", "I2V-01-Director", "I2V-01-live", "I2V-01", "S2V-01"},
			fields: []string{"seconds"},
		},
	}
	profiled := make([]string, 0, len(plugin.Meta.Models))
	for _, family := range families {
		for _, name := range family.models {
			profiled = append(profiled, name)
			t.Run(name, func(t *testing.T) {
				schema, examples := plugin.Meta.UsageForModel(name)
				assert.Equal(t, family.fields, slices.Sorted(maps.Keys(schema)))
				assert.Equal(t, family.resolutions, schema["resolution"].Enum)
				assert.NotEmpty(t, examples)
			})
		}
	}
	assert.ElementsMatch(t, plugin.Meta.Models, profiled, "every declared model must select a usage profile")

	t.Run("submission keeps facts outside the selected profile billable", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			ChannelMeta:     &relaycommon.ChannelMeta{ApiKey: "test-ak", ChannelBaseUrl: "https://api.minimax.example", UpstreamModelName: "MiniMax-Hailuo-2.3"},
			OriginModelName: "MiniMax-Hailuo-2.3",
			TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"},
		}
		adaptor := taskplugin.New(plugin)
		adaptor.Init(info)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
		c.Set("task_request", map[string]any{"model": "MiniMax-Hailuo-2.3", "prompt": "p", "duration": 10, "resolution": "768P"})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

		facts, err := adaptor.ExtractUsageFactsValidated(c, info)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"seconds": float64(10), "resolution": "768P", "input_images": float64(0), "input_video_seconds": float64(0),
		}, facts)
	})
}

func TestHailuoArtifactContentProxy(t *testing.T) {
	source, err := builtinplugins.Source("hailuo")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "hailuo"})
	require.NoError(t, err)
	adaptor := taskplugin.New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:         "test-ak",
			ChannelBaseUrl: "https://api.minimax.example",
		},
	})
	data, err := common.Marshal(map[string]any{"file_id": "file/with space"})
	require.NoError(t, err)
	task := &model.Task{TaskID: "task-public", Status: model.TaskStatusSuccess, Data: data}

	artifacts, err := adaptor.ListArtifacts(task)
	require.NoError(t, err)
	assert.Equal(t, []channel.TaskArtifact{{Key: "video", Type: "video", MimeType: "video/mp4"}}, artifacts)

	descriptor, err := adaptor.BuildContentRequest(task, "video", channel.TaskArtifactClientRequest{Method: http.MethodHead})
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	assert.Equal(t, "https://api.minimax.example/v1/files/download?file_id=file%2Fwith%20space", descriptor.URL)
	assert.Equal(t, http.MethodHead, descriptor.Method)
	assert.Equal(t, map[string]string{"Accept": "video/*", "Authorization": "Bearer test-ak"}, descriptor.Headers)
	assert.False(t, descriptor.Credentialless)
}

func loadHailuoPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("hailuo")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "hailuo"})
	require.NoError(t, err)
	return plugin
}

func callHailuoHook(t *testing.T, plugin *jsplugin.LoadedPlugin, hook string, args ...any) map[string]any {
	t.Helper()
	value, err := plugin.Engine.Call(t.Context(), hook, args...)
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	return decoded
}

func hailuoH3SubmitContext(requestBody map[string]any) map[string]any {
	return map[string]any{
		"requestBody":   requestBody,
		"model":         "MiniMax-H3",
		"upstreamModel": "MiniMax-H3",
		"baseUrl":       "https://api.minimax.example",
		"apiKey":        "test-ak",
	}
}

// MiniMax-H3 submits to /v2/video_generation with a multimodal content array
// instead of the flat /v1 frame fields.
func TestHailuoH3BuildSubmitRequest(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	testCases := []struct {
		name       string
		request    map[string]any
		wantBody   string
		wantAction string
	}{
		{
			name:       "text to video defaults duration ratio and resolution",
			request:    map[string]any{"prompt": "a boy playing basketball"},
			wantBody:   `{"model":"MiniMax-H3","content":[{"type":"text","text":"a boy playing basketball"}],"resolution":"768P","duration":5,"ratio":"16:9"}`,
			wantAction: "text_to_video",
		},
		{
			name:       "2K resolution from size",
			request:    map[string]any{"prompt": "p", "duration": 15, "size": "2K"},
			wantBody:   `{"model":"MiniMax-H3","content":[{"type":"text","text":"p"}],"resolution":"2K","duration":15,"ratio":"16:9"}`,
			wantAction: "text_to_video",
		},
		{
			name:    "first and last frame from metadata",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{"first_frame_image": "first.png", "last_frame_image": "last.png"}},
			wantBody: `{"model":"MiniMax-H3","content":[
				{"type":"text","text":"p"},
				{"type":"image_url","role":"first_frame","image_url":{"url":"first.png"}},
				{"type":"image_url","role":"last_frame","image_url":{"url":"last.png"}}],
				"resolution":"768P","duration":5,"ratio":"adaptive"}`,
			wantAction: "image_to_video",
		},
		{
			name:    "frames from the images array",
			request: map[string]any{"prompt": "p", "images": []any{"first.png", "last.png"}},
			wantBody: `{"model":"MiniMax-H3","content":[
				{"type":"text","text":"p"},
				{"type":"image_url","role":"first_frame","image_url":{"url":"first.png"}},
				{"type":"image_url","role":"last_frame","image_url":{"url":"last.png"}}],
				"resolution":"768P","duration":5,"ratio":"adaptive"}`,
			wantAction: "image_to_video",
		},
		{
			name: "reference video and audio",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{
				"reference_video": "ref.mp4",
				"reference_audio": []any{"a.mp3", "b.mp3"},
			}},
			wantBody: `{"model":"MiniMax-H3","content":[
				{"type":"text","text":"p"},
				{"type":"video_url","role":"reference_video","video_url":{"url":"ref.mp4"}},
				{"type":"audio_url","role":"reference_audio","audio_url":{"url":"a.mp3"}},
				{"type":"audio_url","role":"reference_audio","audio_url":{"url":"b.mp3"}}],
				"resolution":"768P","duration":5,"ratio":"adaptive"}`,
			wantAction: "image_to_video",
		},
		{
			name: "content passthrough prepends the prompt when no text item exists",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{"content": []any{
				map[string]any{"type": "image_url", "role": "reference_image", "image_url": map[string]any{"url": "img.png"}},
			}}},
			wantBody: `{"model":"MiniMax-H3","content":[
				{"type":"text","text":"p"},
				{"type":"image_url","role":"reference_image","image_url":{"url":"img.png"}}],
				"resolution":"768P","duration":5,"ratio":"adaptive"}`,
			wantAction: "image_to_video",
		},
		{
			name: "content passthrough keeps an existing text item",
			request: map[string]any{"prompt": "ignored", "metadata": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "kept"},
			}}},
			wantBody:   `{"model":"MiniMax-H3","content":[{"type":"text","text":"kept"}],"resolution":"768P","duration":5,"ratio":"16:9"}`,
			wantAction: "text_to_video",
		},
		{
			name: "explicit ratio callback and watermark",
			request: map[string]any{"prompt": "p", "duration": 4, "metadata": map[string]any{
				"ratio":          "9:16",
				"callback_url":   "https://example.com/cb",
				"aigc_watermark": true,
			}},
			wantBody:   `{"model":"MiniMax-H3","content":[{"type":"text","text":"p"}],"resolution":"768P","duration":4,"ratio":"9:16","callback_url":"https://example.com/cb","aigc_watermark":true}`,
			wantAction: "text_to_video",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			descriptor := callHailuoHook(t, plugin, "buildSubmitRequest", hailuoH3SubmitContext(testCase.request))
			assert.Equal(t, "https://api.minimax.example/v2/video_generation", descriptor["url"])
			assert.Equal(t, "POST", descriptor["method"])
			assert.Equal(t, testCase.wantAction, descriptor["action"])
			body, err := common.Marshal(descriptor["body"])
			require.NoError(t, err)
			assert.JSONEq(t, testCase.wantBody, string(body))
		})
	}
}

// Every MiniMax-H3 request bound is rejected before the upstream call, so an
// out-of-range duration can never reach quota calculation as a billing fact.
func TestHailuoH3RejectsOutOfContractRequests(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	tenReferenceImages := make([]any, 0, 10)
	for range 10 {
		tenReferenceImages = append(tenReferenceImages, map[string]any{
			"type": "image_url", "role": "reference_image", "image_url": map[string]any{"url": "u"},
		})
	}
	testCases := []struct {
		name    string
		request map[string]any
		wantErr string
	}{
		{"duration below the minimum", map[string]any{"prompt": "p", "duration": 3}, "duration must be an integer between 4 and 15"},
		{"duration above the maximum", map[string]any{"prompt": "p", "duration": 16}, "duration must be an integer between 4 and 15"},
		{"fractional duration", map[string]any{"prompt": "p", "duration": 5.5}, "duration must be an integer between 4 and 15"},
		{"unsupported resolution", map[string]any{"prompt": "p", "size": "1080P"}, "resolution must be 768P or 2K"},
		{"unknown ratio", map[string]any{"prompt": "p", "metadata": map[string]any{"ratio": "16:10"}}, "ratio must be one of"},
		{"adaptive ratio without a visual input", map[string]any{"prompt": "p", "metadata": map[string]any{"ratio": "adaptive"}}, "ratio adaptive requires an image or video input"},
		{"too many frame images", map[string]any{"prompt": "p", "images": []any{"a.png", "b.png", "c.png"}}, "at most 2 frame images"},
		{"media without text", map[string]any{"images": []any{"a.png"}}, "requires a non-empty text item"},
		{"content is not an array", map[string]any{"prompt": "p", "metadata": map[string]any{"content": "nope"}}, "metadata.content must be an array"},
		{
			name: "multiple first frame roles",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{"content": []any{
				map[string]any{"type": "image_url", "role": "first_frame", "image_url": map[string]any{"url": "u1"}},
				map[string]any{"type": "image_url", "role": "first_frame", "image_url": map[string]any{"url": "u2"}},
			}}},
			wantErr: "at most one first_frame image",
		},
		{
			name: "passthrough mixes frame and reference media",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{"content": []any{
				map[string]any{"type": "image_url", "role": "first_frame", "image_url": map[string]any{"url": "frame"}},
				map[string]any{"type": "image_url", "role": "reference_image", "image_url": map[string]any{"url": "reference"}},
			}}},
			wantErr: "cannot mix frame images with reference media",
		},
		{
			name: "assembled content mixes frame and reference media",
			request: map[string]any{"prompt": "p", "images": []any{"frame"}, "metadata": map[string]any{
				"reference_video": "reference.mp4",
			}},
			wantErr: "cannot mix frame images with reference media",
		},
		{
			name: "too many reference videos",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{"content": []any{
				map[string]any{"type": "video_url", "video_url": map[string]any{"url": "u1"}},
				map[string]any{"type": "video_url", "video_url": map[string]any{"url": "u2"}},
				map[string]any{"type": "video_url", "video_url": map[string]any{"url": "u3"}},
				map[string]any{"type": "video_url", "video_url": map[string]any{"url": "u4"}},
			}}},
			wantErr: "at most 3 reference videos",
		},
		{
			name:    "too many reference images",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{"content": tenReferenceImages}},
			wantErr: "at most 9 reference images",
		},
		{
			name:    "too many reference audios",
			request: map[string]any{"prompt": "p", "metadata": map[string]any{"reference_audio": []any{"a.mp3", "b.mp3", "c.mp3", "d.mp3"}}},
			wantErr: "at most 3 reference audios",
		},
		{"empty input", map[string]any{"prompt": "  "}, "requires a prompt or a media input"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", hailuoH3SubmitContext(testCase.request))
			require.ErrorContains(t, err, testCase.wantErr)
		})
	}
}

// The /v1 models keep the flat request shape and endpoint.
func TestHailuoLegacySubmitRequestUnchanged(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	ctx := hailuoH3SubmitContext(map[string]any{"prompt": "p", "duration": 10, "size": "768P"})
	ctx["model"] = "MiniMax-Hailuo-2.3"
	ctx["upstreamModel"] = "MiniMax-Hailuo-2.3"
	descriptor := callHailuoHook(t, plugin, "buildSubmitRequest", ctx)
	assert.Equal(t, "https://api.minimax.example/v1/video_generation", descriptor["url"])
	body, err := common.Marshal(descriptor["body"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"MiniMax-Hailuo-2.3","prompt":"p","duration":10,"resolution":"768P"}`, string(body))
}

func TestHailuoQueryRequestEndpointByModel(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	testCases := []struct {
		name string
		ctx  map[string]any
		want string
	}{
		{
			name: "H3 uses the v2 path parameter",
			ctx:  map[string]any{"taskId": "task/1", "upstreamModel": "MiniMax-H3"},
			want: "https://api.minimax.example/v2/query/video_generation/task%2F1",
		},
		{
			name: "an unmapped task falls back to the origin model",
			ctx:  map[string]any{"taskId": "t1", "model": "MiniMax-H3"},
			want: "https://api.minimax.example/v2/query/video_generation/t1",
		},
		{
			name: "legacy models keep the v1 query parameter",
			ctx:  map[string]any{"taskId": "t1", "upstreamModel": "MiniMax-Hailuo-2.3"},
			want: "https://api.minimax.example/v1/query/video_generation?task_id=t1",
		},
		{
			name: "a channel-mapped alias resolves through the upstream model",
			ctx:  map[string]any{"taskId": "t1", "model": "h3", "upstreamModel": "MiniMax-H3"},
			want: "https://api.minimax.example/v2/query/video_generation/t1",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.ctx["baseUrl"] = "https://api.minimax.example"
			testCase.ctx["apiKey"] = "test-ak"
			descriptor := callHailuoHook(t, plugin, "buildQueryRequest", testCase.ctx)
			assert.Equal(t, testCase.want, descriptor["url"])
			assert.Equal(t, "GET", descriptor["method"])
		})
	}
}

func TestHailuoParseTaskResult(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	testCases := []struct {
		name       string
		body       string
		wantStatus string
		wantURL    string
		wantReason string
	}{
		{"H3 queued", `{"task":{"id":"1","status":"queued"}}`, "QUEUED", "", ""},
		{"H3 running", `{"task":{"id":"1","status":"running"}}`, "IN_PROGRESS", "", ""},
		{"H3 succeeded", `{"task":{"id":"1","status":"succeeded","content":{"url":"https://cdn.example/h3.mp4"}}}`, "SUCCESS", "https://cdn.example/h3.mp4", ""},
		{"H3 failed", `{"task":{"id":"1","status":"failed","error":{"code":"1026","message":"sensitive content"}}}`, "FAILURE", "", "sensitive content"},
		{"H3 cancelled", `{"task":{"id":"1","status":"cancelled"}}`, "FAILURE", "", "task cancelled"},
		{"H3 permanent query error", `{"type":"error","error":{"type":"authorized_error","message":"login failed","http_code":"401"}}`, "FAILURE", "", "login failed"},
		{"legacy success", `{"task_id":"1","status":"Success","file_id":"f1","base_resp":{"status_code":0}}`, "SUCCESS", "", ""},
		{"legacy processing", `{"task_id":"1","status":"Processing","base_resp":{"status_code":0}}`, "IN_PROGRESS", "", ""},
		{"H3 unrecognized", `{"task":{"id":"1","status":"weird"}}`, "UNKNOWN", "", "unrecognized status: weird"},
		{"legacy unrecognized", `{"task_id":"1","status":"Weird","base_resp":{"status_code":0}}`, "UNKNOWN", "", "unrecognized status: Weird"},
		{"legacy base_resp failure", `{"task_id":"1","status":"Success","base_resp":{"status_code":1001,"status_msg":"upstream down"}}`, "FAILURE", "", "upstream down"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var body any
			require.NoError(t, common.UnmarshalJsonStr(testCase.body, &body))
			result := callHailuoHook(t, plugin, "parseTaskResult", map[string]any{}, body)
			assert.Equal(t, testCase.wantStatus, result["status"])
			assert.Equal(t, testCase.wantURL, common.Interface2String(result["url"]))
			assert.Equal(t, testCase.wantReason, common.Interface2String(result["reason"]))
		})
	}
	t.Run("H3 retryable query error", func(t *testing.T) {
		var body any
		require.NoError(t, common.UnmarshalJsonStr(
			`{"type":"error","error":{"type":"rate_limit_error","message":"retry later","http_code":"429"}}`, &body))
		_, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{}, body)
		require.ErrorContains(t, err, "retry later")
	})
}

func TestHailuoExtractUsageFacts(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	nineReferenceImages := make([]any, 0, 9)
	for range 9 {
		nineReferenceImages = append(nineReferenceImages, map[string]any{
			"type": "image_url", "role": "reference_image", "image_url": map[string]any{"url": "image"},
		})
	}
	testCases := []struct {
		name    string
		model   string
		request map[string]any
		want    map[string]any
	}{
		{"H3 defaults", "MiniMax-H3", map[string]any{"prompt": "p"}, map[string]any{
			"seconds": float64(5), "resolution": "768P", "input_images": float64(0), "input_video_seconds": float64(0),
		}},
		{"H3 2K", "MiniMax-H3", map[string]any{"prompt": "p", "duration": 12, "size": "2K"}, map[string]any{
			"seconds": float64(12), "resolution": "2K", "input_images": float64(0), "input_video_seconds": float64(0),
		}},
		{"H3 reference images", "MiniMax-H3", map[string]any{"prompt": "p", "metadata": map[string]any{"content": nineReferenceImages}}, map[string]any{
			"seconds": float64(5), "resolution": "768P", "input_images": float64(9), "input_video_seconds": float64(0),
		}},
		{"H3 reference video reserves total duration limit", "MiniMax-H3", map[string]any{"prompt": "p", "metadata": map[string]any{
			"reference_video": []any{"one.mp4", "two.mp4", "three.mp4"},
		}}, map[string]any{
			"seconds": float64(5), "resolution": "768P", "input_images": float64(0), "input_video_seconds": float64(15),
		}},
		{"legacy model", "MiniMax-Hailuo-2.3", map[string]any{"prompt": "p", "duration": 10}, map[string]any{
			"seconds": float64(10), "resolution": "768P", "input_images": float64(0), "input_video_seconds": float64(0),
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := hailuoH3SubmitContext(testCase.request)
			ctx["model"] = testCase.model
			ctx["upstreamModel"] = testCase.model
			assert.Equal(t, testCase.want, callHailuoHook(t, plugin, "extractUsage", ctx))
		})
	}
}

func TestHailuoH3CompletionUsageFacts(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	testCases := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "actual usage replaces submission estimates",
			body: `{"task":{"id":"1","status":"succeeded","resolution":"2K","usage":{"output_seconds":5,"input_seconds":7.5,"input_image_count":6}}}`,
			want: map[string]any{"seconds": float64(5), "resolution": "2K", "input_images": float64(6), "input_video_seconds": float64(7.5)},
		},
		{
			name: "zero actual usage is retained for settlement",
			body: `{"task":{"id":"1","status":"succeeded","resolution":"768P","usage":{"output_seconds":4,"input_seconds":0,"input_image_count":0}}}`,
			want: map[string]any{"seconds": float64(4), "resolution": "768P", "input_images": float64(0), "input_video_seconds": float64(0)},
		},
		{
			name: "zero output cannot erase the submission reservation",
			body: `{"task":{"id":"1","status":"succeeded","resolution":"768P","usage":{"output_seconds":0,"input_seconds":0,"input_image_count":0}}}`,
			want: map[string]any{"resolution": "768P", "input_images": float64(0), "input_video_seconds": float64(0)},
		},
		{
			name: "missing usage leaves submission estimates untouched",
			body: `{"task":{"id":"1","status":"succeeded","resolution":"768P"}}`,
			want: map[string]any{"resolution": "768P"},
		},
		{
			name: "out of contract usage cannot become a billing multiplier",
			body: `{"task":{"id":"1","status":"succeeded","resolution":"2K","usage":{"output_seconds":16,"input_seconds":16,"input_image_count":10}}}`,
			want: map[string]any{"resolution": "2K"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var body any
			require.NoError(t, common.UnmarshalJsonStr(testCase.body, &body))
			assert.Equal(t, testCase.want, callHailuoHook(t, plugin, "extractUsageOnComplete", nil, nil, body))
		})
	}

	t.Run("polling adaptor carries actual facts into task settlement", func(t *testing.T) {
		adaptor := taskplugin.New(plugin)
		result, err := adaptor.ParseTaskResult(&model.Task{}, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, []byte(
			`{"task":{"id":"1","status":"succeeded","resolution":"2K","usage":{"output_seconds":5,"input_seconds":7.5,"input_image_count":6}}}`,
		))
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"seconds": float64(5), "resolution": "2K", "input_images": float64(6), "input_video_seconds": float64(7.5),
		}, result.UsageFacts)
	})
}

// The /v2 result is a public CDN URL, so its artifact is proxied without
// channel credentials instead of through the /v1 file download endpoint.
func TestHailuoH3ArtifactContentProxy(t *testing.T) {
	source, err := builtinplugins.Source("hailuo")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "hailuo"})
	require.NoError(t, err)
	adaptor := taskplugin.New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "test-ak", ChannelBaseUrl: "https://api.minimax.example"},
	})
	task := &model.Task{
		TaskID: "task-public",
		Status: model.TaskStatusSuccess,
		Data:   []byte(`{"task":{"id":"1","status":"succeeded","content":{"url":"https://cdn.example/h3.mp4"}}}`),
	}

	artifacts, err := adaptor.ListArtifacts(task)
	require.NoError(t, err)
	assert.Equal(t, []channel.TaskArtifact{{Key: "video", Type: "video", MimeType: "video/mp4"}}, artifacts)

	descriptor, err := adaptor.BuildContentRequest(task, "video", channel.TaskArtifactClientRequest{Method: http.MethodGet})
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	assert.Equal(t, "https://cdn.example/h3.mp4", descriptor.URL)
	assert.True(t, descriptor.Credentialless)

	pending := &model.Task{TaskID: "task-public", Status: model.TaskStatusInProgress, Data: task.Data}
	pendingArtifacts, err := adaptor.ListArtifacts(pending)
	require.NoError(t, err)
	assert.Empty(t, pendingArtifacts)
}

// The /v2 create response carries only task_id, without the /v1 base_resp envelope.
func TestHailuoParseSubmitResponse(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	t.Run("H3 create has no envelope", func(t *testing.T) {
		parsed := callHailuoHook(t, plugin, "parseSubmitResponse", map[string]any{"upstreamModel": "MiniMax-H3"},
			map[string]any{"body": map[string]any{"task_id": "h3-1"}})
		assert.Equal(t, "h3-1", parsed["taskId"])
	})
	t.Run("H3 rejection reports the envelope message", func(t *testing.T) {
		_, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"upstreamModel": "MiniMax-H3"},
			map[string]any{"body": map[string]any{"base_resp": map[string]any{"status_code": 2013, "status_msg": "invalid params"}}})
		require.ErrorContains(t, err, "invalid params")
	})
	t.Run("H3 rejection reports the v2 error message", func(t *testing.T) {
		_, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"upstreamModel": "MiniMax-H3"},
			map[string]any{"body": map[string]any{"type": "error", "error": map[string]any{
				"type": "bad_request_error", "message": "content requires text", "http_code": "400",
			}}})
		require.ErrorContains(t, err, "content requires text")
	})
	t.Run("legacy create", func(t *testing.T) {
		parsed := callHailuoHook(t, plugin, "parseSubmitResponse", map[string]any{"upstreamModel": "MiniMax-Hailuo-2.3"},
			map[string]any{"body": map[string]any{"task_id": "v1-1", "base_resp": map[string]any{"status_code": 0}}})
		assert.Equal(t, "v1-1", parsed["taskId"])
	})
	t.Run("legacy response without an envelope is rejected", func(t *testing.T) {
		ctx := map[string]any{"upstreamModel": "MiniMax-Hailuo-2.3"}
		_, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", ctx,
			map[string]any{"body": map[string]any{"task_id": "v1-1"}})
		require.ErrorContains(t, err, "hailuo submit failed")
	})
	t.Run("legacy error", func(t *testing.T) {
		_, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", map[string]any{"upstreamModel": "MiniMax-Hailuo-2.3"},
			map[string]any{"body": map[string]any{"base_resp": map[string]any{"status_code": 1026, "status_msg": "sensitive"}}})
		require.ErrorContains(t, err, "sensitive")
	})
}

// Without an H3 exemption the shared /v1 combo table would reject every H3
// duration on the OpenAI Video path before the request is ever built.
func TestHailuoH3PassesOpenAIVideoDecode(t *testing.T) {
	plugin := loadHailuoPlugin(t)
	value, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"},
		map[string]any{
			"body":          map[string]any{"kind": "json", "value": map[string]any{"model": "MiniMax-H3", "prompt": "p", "seconds": 12, "size": "2K"}},
			"model":         "MiniMax-H3",
			"upstreamModel": "MiniMax-H3",
		})
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var intent map[string]any
	require.NoError(t, common.Unmarshal(encoded, &intent))
	assert.Equal(t, "submit", intent["kind"])
	requestBody, ok := intent["requestBody"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(12), requestBody["duration"])
}
