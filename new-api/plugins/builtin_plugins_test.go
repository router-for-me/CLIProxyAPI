package plugins

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var expectedKeys = []string{"alibaba", "doubao", "google", "hailuo", "jimeng", "kling", "sora", "sunoapi", "vertex-ai", "vidu"}

func TestBuiltInVendorPluginsDeclareNativeRoutesAndLegacyChannelTypes(t *testing.T) {
	generation := jsplugin.DefaultRegistry.Generation()
	require.NotNil(t, generation)

	routes := []struct {
		method    string
		path      string
		key       string
		routeType jsplugin.RouteType
		action    string
		renderer  string
	}{
		{"POST", "/kling/v1/videos/text2video", "kling", jsplugin.RouteTypeSubmit, "text_to_video", "taskCreated"},
		{"POST", "/kling/v1/videos/image2video", "kling", jsplugin.RouteTypeSubmit, "image_to_video", "taskCreated"},
		{"GET", "/kling/v1/videos/text2video/:task_id", "kling", jsplugin.RouteTypeQuery, "", "taskStatus"},
		{"GET", "/kling/v1/videos/image2video/:task_id", "kling", jsplugin.RouteTypeQuery, "", "taskStatus"},
		{"POST", "/jimeng/", "jimeng", jsplugin.RouteTypeDynamic, "", "renderTask"},
		{"POST", "/suno/submit/:action", "sunoapi", jsplugin.RouteTypeSubmit, "", "renderSubmit"},
		{"POST", "/suno/fetch", "sunoapi", jsplugin.RouteTypeDynamic, "", "renderTasks"},
		{"GET", "/suno/fetch/:task_id", "sunoapi", jsplugin.RouteTypeQuery, "", "renderTask"},
		{"POST", "/doubao/api/v3/contents/generations/tasks", "doubao", jsplugin.RouteTypeSubmit, "", "taskCreated"},
		{"GET", "/doubao/api/v3/contents/generations/tasks/:task_id", "doubao", jsplugin.RouteTypeQuery, "", "taskStatus"},
		{"POST", "/doubao/api/v3/images/generations", "doubao", jsplugin.RouteTypeSubmit, "", "imageCreated"},
	}
	for _, expected := range routes {
		t.Run(expected.method+" "+expected.path, func(t *testing.T) {
			binding, found := generation.LookupDeclaredRoute(expected.method, expected.path)
			require.True(t, found)
			require.Equal(t, expected.key, binding.Plugin.Meta.Key)
			require.Equal(t, expected.routeType, binding.Route.Type)
			require.Equal(t, expected.action, binding.Route.Action)
			require.Equal(t, expected.renderer, binding.Route.Render)
		})
	}

	// The legacy Wan edit route serves both the wan2.5 image-list body and the
	// wanx2.1-imageedit function API.
	imageEdit, found := generation.LookupDeclaredRoute("POST", "/ali/api/v1/services/aigc/image2image/image-synthesis")
	require.True(t, found)
	assert.Equal(t, "alibaba", imageEdit.Plugin.Meta.Key)
	assert.ElementsMatch(t, []string{"wan2.5-i2i-preview", "wanx2.1-imageedit"}, imageEdit.Route.Models)

	channelTypes := []struct {
		value int
		key   string
	}{
		{1, "sora"},
		{36, "sunoapi"},
		{45, "doubao"},
		{50, "kling"},
		{51, "jimeng"},
		{54, "doubao"},
		{55, "sora"},
	}
	for _, channelType := range channelTypes {
		plugin, found := generation.GetByChannelType(channelType.value)
		require.True(t, found)
		require.Equal(t, channelType.key, plugin.Meta.Key)
	}
}

func TestBuiltInTaskPluginResponsesAndUsageContracts(t *testing.T) {
	generation := jsplugin.DefaultRegistry.Generation()
	require.NotNil(t, generation)

	entries, err := fs.ReadDir(taskPlugins, "tasks")
	require.NoError(t, err)
	actualKeys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			actualKeys = append(actualKeys, entry.Name())
		}
	}
	assert.Equal(t, expectedKeys, actualKeys)

	for _, key := range expectedKeys {
		t.Run(key, func(t *testing.T) {
			_, found := generation.Get(key)
			require.True(t, found, "factory plugin was excluded from the active generation")

			source, sourceErr := Source(key)
			require.NoError(t, sourceErr)
			registry := jsplugin.NewRegistry()
			plugin, registerErr := registry.RegisterFactory(source, jsplugin.Options{Key: key})
			require.NoError(t, registerErr)

			var responsesClaim jsplugin.ProtocolClaim
			foundResponses := false
			for _, claim := range plugin.Meta.Protocols {
				if claim.Name == "openai_responses" {
					responsesClaim = claim
					foundResponses = true
					break
				}
			}
			require.True(t, foundResponses, "openai_responses claim must be present")
			assert.Equal(t, []string{"stream", "sync", "background"}, responsesClaim.Supports)
			for _, model := range plugin.Meta.Models {
				binding, claimed := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
				require.True(t, claimed, model)
				assert.Same(t, plugin, binding.Plugin)
			}
			for _, hook := range []string{"decodeRequest", "renderEvents", "renderFinal"} {
				callable, callableErr := plugin.Engine.HasCallablePath(t.Context(), "protocols", "openai_responses", hook)
				require.NoError(t, callableErr)
				assert.True(t, callable, hook)
			}
			for _, hook := range []string{"extractUsage", "extractUsageOnComplete"} {
				callable, callableErr := plugin.Engine.HasExport(t.Context(), hook)
				require.NoError(t, callableErr)
				assert.True(t, callable, hook)
			}
			require.NotEmpty(t, plugin.Meta.UsageSchema)
			for usageKey, schema := range plugin.Meta.UsageSchema {
				assert.NotEmpty(t, schema.Description, usageKey)
			}
		})
	}
}

func TestBuiltInResponsesDecodersEchoChannelMappedAlias(t *testing.T) {
	bodyOverrides := map[string]map[string]any{}
	for _, key := range expectedKeys {
		t.Run(key, func(t *testing.T) {
			source, sourceErr := Source(key)
			require.NoError(t, sourceErr)
			registry := jsplugin.NewRegistry()
			plugin, registerErr := registry.RegisterFactory(source, jsplugin.Options{Key: key})
			require.NoError(t, registerErr)
			require.NotEmpty(t, plugin.Meta.Models)

			alias := "alias-under-test"
			upstreamModel := plugin.Meta.Models[0]
			body := map[string]any{"model": alias, "input": "a cat walking on the beach"}
			if override, ok := bodyOverrides[key]; ok {
				body = override
			}
			value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
				"model": alias, "upstreamModel": upstreamModel, "stream": false,
				"body": map[string]any{"kind": "json", "value": body},
			})
			require.NoError(t, callErr)
			encoded, marshalErr := common.Marshal(value)
			require.NoError(t, marshalErr)
			var decoded map[string]any
			require.NoError(t, common.Unmarshal(encoded, &decoded))
			assert.Equal(t, "submit", decoded["kind"])
			assert.Equal(t, alias, decoded["model"])
		})
	}
}

func TestBuiltInPluginsAddressNewAPIUpstreamOnNativeRoutes(t *testing.T) {
	generation := jsplugin.DefaultRegistry.Generation()
	require.NotNil(t, generation)
	for _, key := range []string{"hailuo", "google", "vidu", "vertex-ai"} {
		plugin, found := generation.Get(key)
		require.True(t, found, key)
		assert.False(t, plugin.Meta.SupportsUpstream(jsplugin.UpstreamKindNewAPI), "%s has no native routes and must not be bindable to a New API channel", key)
	}

	const base = "https://upstream.example"
	cases := []struct {
		key                string
		driver             map[string]any
		vendorKey          string
		vendorAuthPrefix   string
		vendorSubmit       string
		gatewaySubmit      string
		vendorQuery        string
		gatewayQuery       string
		content            string
		legacyKeyHeuristic bool
	}{
		{
			key:              "doubao",
			driver:           map[string]any{"action": "text_to_video", "model": "doubao-seedance-1-0-pro-250528", "upstreamModel": "doubao-seedance-1-0-pro-250528", "requestBody": map[string]any{"model": "doubao-seedance-1-0-pro-250528", "prompt": "a cat"}},
			vendorKey:        "vendor-key",
			vendorAuthPrefix: "Bearer vendor-key",
			vendorSubmit:     "/api/v3/contents/generations/tasks",
			gatewaySubmit:    "/doubao/api/v3/contents/generations/tasks",
			vendorQuery:      "/api/v3/contents/generations/tasks/tid",
			gatewayQuery:     "/doubao/api/v3/contents/generations/tasks/tid",
		},
		{
			key:              "alibaba",
			driver:           map[string]any{"action": "text_to_video", "model": "wan2.2-t2v-plus", "upstreamModel": "wan2.2-t2v-plus", "requestBody": map[string]any{"model": "wan2.2-t2v-plus", "prompt": "a cat"}},
			vendorKey:        "vendor-key",
			vendorAuthPrefix: "Bearer vendor-key",
			vendorSubmit:     "/api/v1/services/aigc/video-generation/video-synthesis",
			gatewaySubmit:    "/ali/api/v1/services/aigc/video-generation/video-synthesis",
			vendorQuery:      "/api/v1/tasks/tid",
			gatewayQuery:     "/ali/api/v1/tasks/tid",
		},
		{
			key:                "kling",
			driver:             map[string]any{"action": "text_to_video", "model": "kling-v1", "upstreamModel": "kling-v1", "requestBody": map[string]any{"prompt": "a cat"}},
			vendorKey:          "ak|sk",
			vendorAuthPrefix:   "Bearer ey",
			vendorSubmit:       "/v1/videos/text2video",
			gatewaySubmit:      "/kling/v1/videos/text2video",
			vendorQuery:        "/v1/videos/text2video/tid",
			gatewayQuery:       "/kling/v1/videos/text2video/tid",
			legacyKeyHeuristic: true,
		},
		{
			key:                "jimeng",
			driver:             map[string]any{"action": "text_to_video", "model": "jimeng_vgfm_t2v_l20", "upstreamModel": "jimeng_vgfm_t2v_l20", "requestBody": map[string]any{"prompt": "a cat"}},
			vendorKey:          "ak|sk",
			vendorAuthPrefix:   "HMAC-SHA256",
			vendorSubmit:       "/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31",
			gatewaySubmit:      "/jimeng/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31",
			vendorQuery:        "/?Action=CVSync2AsyncGetResult&Version=2022-08-31",
			gatewayQuery:       "/jimeng/?Action=CVSync2AsyncGetResult&Version=2022-08-31",
			legacyKeyHeuristic: true,
		},
		{
			key:              "sunoapi",
			driver:           map[string]any{"action": "MUSIC", "model": "suno_music", "upstreamModel": "suno_music", "requestBody": map[string]any{"prompt": "a song"}},
			vendorKey:        "vendor-key",
			vendorAuthPrefix: "Bearer vendor-key",
			vendorSubmit:     "/suno/submit/MUSIC",
			gatewaySubmit:    "/suno/submit/MUSIC",
			vendorQuery:      "/suno/fetch",
			gatewayQuery:     "/suno/fetch",
		},
		{
			key:              "sora",
			driver:           map[string]any{"action": "text_to_video", "model": "sora-2", "upstreamModel": "sora-2", "requestBody": map[string]any{"prompt": "a cat"}},
			vendorKey:        "vendor-key",
			vendorAuthPrefix: "Bearer vendor-key",
			vendorSubmit:     "/v1/videos",
			gatewaySubmit:    "/v1/videos",
			vendorQuery:      "/v1/videos/tid",
			gatewayQuery:     "/v1/videos/tid",
			content:          "/v1/videos/tid/content",
		},
	}
	variants := []struct {
		name      string
		upstream  map[string]any
		gateway   bool
		legacyKey bool
	}{
		{"vendor signal", map[string]any{"kind": "vendor"}, false, false},
		{"no signal from an older host", nil, false, false},
		{"new_api signal", map[string]any{"kind": "new_api"}, true, false},
		{"legacy sk- key without a signal", nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			plugin, found := generation.Get(tc.key)
			require.True(t, found)
			require.True(t, plugin.Meta.SupportsUpstream(jsplugin.UpstreamKindNewAPI), "%s must declare new_api to be bindable to a New API channel", tc.key)
			call := func(hook string, args ...any) map[string]any {
				value, err := plugin.Engine.Call(t.Context(), hook, args...)
				require.NoError(t, err, hook)
				encoded, err := common.Marshal(value)
				require.NoError(t, err)
				var descriptor map[string]any
				require.NoError(t, common.Unmarshal(encoded, &descriptor))
				return descriptor
			}
			for _, variant := range variants {
				if variant.legacyKey && !tc.legacyKeyHeuristic {
					continue
				}
				t.Run(variant.name, func(t *testing.T) {
					apiKey := tc.vendorKey
					if variant.legacyKey {
						apiKey = "sk-legacy"
					} else if variant.gateway {
						apiKey = "sk-gateway"
					}
					authHeader := apiKey
					if variant.gateway && !variant.legacyKey {
						authHeader = "Bearer " + apiKey
					}
					ctx := map[string]any{"baseUrl": base, "apiKey": apiKey, "authHeader": authHeader, "publicTaskId": "task_pub", "requestHeaders": map[string]any{}, "files": []any{}}
					for key, value := range tc.driver {
						ctx[key] = value
					}
					if variant.upstream != nil {
						ctx["upstream"] = variant.upstream
					}
					wantSubmit, wantQuery := tc.vendorSubmit, tc.vendorQuery
					if variant.gateway {
						wantSubmit, wantQuery = tc.gatewaySubmit, tc.gatewayQuery
					}

					submit := call("buildSubmitRequest", ctx)
					assert.Equal(t, base+wantSubmit, submit["url"])
					headers, _ := submit["headers"].(map[string]any)
					authorization, _ := headers["Authorization"].(string)
					if variant.gateway {
						assert.Equal(t, "Bearer "+apiKey, authorization, "a gateway receives its own token as a Bearer header")
					} else {
						assert.True(t, strings.HasPrefix(authorization, tc.vendorAuthPrefix), "vendor Authorization %q", authorization)
					}

					queryCtx := map[string]any{"taskId": "tid", "publicTaskId": "task_pub", "action": tc.driver["action"], "model": tc.driver["model"], "upstreamModel": tc.driver["upstreamModel"], "baseUrl": base, "apiKey": apiKey, "authHeader": authHeader, "data": nil, "state": nil}
					if variant.upstream != nil {
						queryCtx["upstream"] = variant.upstream
					}
					var query map[string]any
					if plugin.Meta.FetchMode == "batch" {
						query = call("buildBatchQueryRequest", queryCtx, []any{queryCtx})
					} else {
						query = call("buildQueryRequest", queryCtx)
					}
					assert.Equal(t, base+wantQuery, query["url"])

					if tc.content == "" {
						return
					}
					contentCtx := map[string]any{"baseUrl": base, "apiKey": apiKey, "authHeader": authHeader, "artifactKey": "video", "upstreamTaskId": "tid", "data": map[string]any{}, "clientRequest": map[string]any{"method": "GET", "headers": map[string]any{}}}
					if variant.upstream != nil {
						contentCtx["upstream"] = variant.upstream
					}
					assert.Equal(t, base+tc.content, call("buildContentRequest", contentCtx)["url"])
				})
			}
		})
	}

	t.Run("alibaba asks only DashScope for an interleaved image stream", func(t *testing.T) {
		plugin, found := generation.Get("alibaba")
		require.True(t, found)
		for _, variant := range []struct {
			name             string
			upstream         map[string]any
			wantResponseType string
			wantSSEHeader    bool
		}{
			{"vendor", map[string]any{"kind": "vendor"}, "sse", true},
			{"new_api", map[string]any{"kind": "new_api"}, "json", false},
		} {
			t.Run(variant.name, func(t *testing.T) {
				value, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
					"baseUrl": base, "apiKey": "key", "authHeader": "key", "publicTaskId": "task_pub", "upstream": variant.upstream,
					"model": "wan2.6-image", "upstreamModel": "wan2.6-image",
					"requestBody": map[string]any{"model": "wan2.6-image", "prompt": "a cat", "metadata": map[string]any{"upstream_mode": "sync", "parameters": map[string]any{"enable_interleave": true}}},
				})
				require.NoError(t, err)
				encoded, err := common.Marshal(value)
				require.NoError(t, err)
				var descriptor struct {
					URL          string            `json:"url"`
					ResponseType string            `json:"responseType"`
					Headers      map[string]string `json:"headers"`
				}
				require.NoError(t, common.Unmarshal(encoded, &descriptor))
				assert.Equal(t, variant.wantResponseType, descriptor.ResponseType, "a gateway native route aggregates the vendor stream and answers with JSON")
				_, hasSSEHeader := descriptor.Headers["X-DashScope-Sse"]
				assert.Equal(t, variant.wantSSEHeader, hasSSEHeader)
			})
		}
	})
}
