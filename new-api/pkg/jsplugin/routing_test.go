package jsplugin

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryIndexesNativeRoutesAndProtocolsInOneGeneration(t *testing.T) {
	registry := NewRegistry()
	plugin, err := CompilePlugin(routingTestPluginSource(
		"routing-alpha",
		50,
		`["video-alpha"]`,
		`routes: [
			{method: "POST", path: "/vendor/videos", type: "submit", action: "generate", decode: "decodeVideo", render: "videoCreated"},
			{method: "GET", path: "/vendor/videos/:task_id", type: "query", render: "videoStatus"}
		],
		protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		`export const native = {
			decodeVideo: function(ctx) { return {kind: "submit", model: "video-alpha", requestBody: ctx.body.value}; },
			videoCreated: function(ctx, task) { return task; },
			videoStatus: function(ctx, task) { return task; }
		};
		export const protocols = {openai_responses: {
			decodeRequest: function(ctx) { return {kind: "submit", model: ctx.model, requestBody: ctx.body.value}; },
			renderEvents: function() { return {events: [], state: null, done: false}; },
			renderFinal: function(ctx, task) { return task; }
		}};`,
	), Options{})
	require.NoError(t, err)

	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{plugin}))
	generation := registry.Generation()
	assert.Equal(t, uint64(1), generation.Number)

	byKey, ok := generation.Get("routing-alpha")
	require.True(t, ok)
	assert.Same(t, plugin, byKey)
	byType, ok := generation.GetByChannelType(50)
	require.True(t, ok)
	assert.Same(t, plugin, byType)
	byModel, ok := generation.GetByModel("video-alpha")
	require.True(t, ok)
	assert.Same(t, plugin, byModel)

	route, ok := generation.LookupDeclaredRoute("get", "/vendor/videos/:id")
	require.True(t, ok)
	assert.Equal(t, "routing-alpha", route.Plugin.Meta.Key)
	assert.Equal(t, "task_id", route.Route.TaskIDParam)

	endpoint, ok := generation.LookupEndpoint("POST", "/v1/responses", "video-alpha")
	require.True(t, ok)
	assert.Equal(t, "openai_responses", endpoint.Protocol)
	assert.Equal(t, []ProtocolBinding{endpoint}, generation.LookupEndpointCandidates("POST", "/v1/responses", "video-alpha"))
}

func TestRegistryIndexesSharedEndpointCandidatesForDistinctLegacyProviders(t *testing.T) {
	registry := NewRegistry()
	gemini := mustCompileRoutingPlugin(t, "gemini-provider", 24, `["shared-video"]`,
		`protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		routingProtocolExport("openai_responses"))
	vertex := mustCompileRoutingPlugin(t, "vertex-provider", 41, `["shared-video"]`,
		`protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		routingProtocolExport("openai_responses"))

	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{vertex, gemini}))
	candidates := registry.Generation().LookupEndpointCandidates("POST", "/v1/responses", "shared-video")
	require.Len(t, candidates, 2)
	assert.Equal(t, "gemini-provider", candidates[0].Plugin.Meta.Key)
	assert.Equal(t, "vertex-provider", candidates[1].Plugin.Meta.Key)
	assert.Empty(t, registry.RoutingErrors())
}

func TestSupportsRegisteredHostProtocols(t *testing.T) {
	assert.True(t, SupportsHostProtocol("openai_responses"))
	assert.True(t, SupportsHostProtocol("openai_video"))
	assert.True(t, SupportsHostProtocol(ProtocolOpenAIImage))
	assert.False(t, SupportsHostProtocol("plugin_owned_wire"))
}

func TestLookupHostProtocolOperationExcludesRetrieveWithoutModelField(t *testing.T) {
	_, _, ok := LookupHostProtocolOperation(http.MethodGet, "/v1/responses/:response_id")
	assert.False(t, ok)
	_, _, ok = LookupHostProtocolOperation(http.MethodPost, "/v1/responses")
	assert.True(t, ok)
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		protocol, operation, found := LookupHostProtocolOperation(http.MethodPost, path)
		require.True(t, found, path)
		assert.Equal(t, ProtocolOpenAIImage, protocol)
		assert.Equal(t, "model", operation.ModelField)
		assert.Empty(t, operation.Modes, "the synchronous image protocol has no request modes")
	}
}

// The OpenAI Images protocol is claimed in bare-string form, binds both image
// endpoints for every declared model, and requires the render hook that shapes
// the synchronous response.
func TestOpenAIImageProtocolClaimBindsBothEndpointsAndRequiresRender(t *testing.T) {
	exports := `export const protocols = {openai_image: {
		decodeRequest: function(ctx) { return {kind: "submit", model: ctx.model, requestBody: ctx.body.value}; },
		render: function(ctx, task) { return {data: []}; }
	}};`
	registry := NewRegistry()
	plugin := mustCompileRoutingPlugin(t, "image-plugin", 17, `["image-a", "image-b"]`, `protocols: ["openai_image"],`, exports)
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{plugin}))
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		for _, model := range []string{"image-a", "image-b"} {
			binding, ok := registry.Generation().LookupEndpoint(http.MethodPost, path, model)
			require.True(t, ok, path+" "+model)
			assert.Equal(t, ProtocolOpenAIImage, binding.Protocol)
			assert.Same(t, plugin, binding.Plugin)
		}
	}
	_, ok := registry.Generation().LookupEndpoint(http.MethodPost, "/v1/videos", "image-a")
	assert.False(t, ok, "an image claim must not bind the video endpoint")

	scoped := mustCompileRoutingPlugin(t, "image-scoped", 17, `["image-a", "image-b"]`, `protocols: [{name: "openai_image", models: ["image-a"]}],`, exports)
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{scoped}))
	_, ok = registry.Generation().LookupEndpoint(http.MethodPost, "/v1/images/generations", "image-b")
	assert.False(t, ok, "models narrows the image endpoint bindings")

	_, err := CompilePlugin(routingTestPluginSource("image-no-render", 17, `["image-a"]`, `protocols: ["openai_image"],`,
		`export const protocols = {openai_image: {decodeRequest: function(ctx) { return {kind: "submit", model: ctx.model}; }}};`), Options{})
	require.ErrorContains(t, err, `missing hook "render"`)
	_, err = CompilePlugin(routingTestPluginSource("image-modes", 17, `["image-a"]`, `protocols: [{name: "openai_image", supports: ["sync"]}],`, exports), Options{})
	require.ErrorContains(t, err, "does not define modes")
}

func TestFileReferenceRoundTripAddressesRepeatedFieldFiles(t *testing.T) {
	assert.Equal(t, "request_file:image[]", FileReference("image[]", 0))
	assert.Equal(t, "request_file:image[]#2", FileReference("image[]", 2))
	for _, tc := range []struct {
		ref   string
		field string
		index int
		ok    bool
	}{
		{"request_file:input_reference", "input_reference", 0, true},
		{"request_file:image[]#1", "image[]", 1, true},
		{"request_file:image[]#01", "", 0, false},
		{"request_file:image[]#-1", "", 0, false},
		{"request_file:#1", "", 0, false},
		{"request_file:", "", 0, false},
		{"image", "", 0, false},
	} {
		field, index, ok := ParseFileReference(tc.ref)
		assert.Equal(t, tc.ok, ok, tc.ref)
		assert.Equal(t, tc.field, field, tc.ref)
		assert.Equal(t, tc.index, index, tc.ref)
	}
}

func TestPerProtocolModelsNarrowEndpointBindings(t *testing.T) {
	registry := NewRegistry()
	plugin := mustCompileRoutingPlugin(t, "narrow-protocol", 50, `["gpt-5.5", "gpt-5.6"]`,
		`protocols: [{name: "openai_responses", models: ["gpt-5.5"], supports: ["stream", "sync", "background"]}],`,
		routingProtocolExport("openai_responses"))

	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{plugin}))
	generation := registry.Generation()

	bound, ok := generation.LookupEndpoint("POST", "/v1/responses", "gpt-5.5")
	require.True(t, ok)
	assert.Same(t, plugin, bound.Plugin)
	_, ok = generation.LookupEndpoint("POST", "/v1/responses", "gpt-5.6")
	assert.False(t, ok, "model outside the protocol claim must fall through to the Go relay")
	assert.Empty(t, generation.LookupEndpointCandidates("POST", "/v1/responses", "gpt-5.6"))
}

func TestPerProtocolModelsAllowDisjointPluginsOnSharedProtocol(t *testing.T) {
	registry := NewRegistry()
	left := mustCompileRoutingPlugin(t, "disjoint-left", 0, `["model-a"]`,
		`protocols: [{name: "openai_responses", models: ["model-a"], supports: ["stream", "sync", "background"]}],`,
		routingProtocolExport("openai_responses"))
	right := mustCompileRoutingPlugin(t, "disjoint-right", 0, `["model-b"]`,
		`protocols: [{name: "openai_responses", models: ["model-b"], supports: ["stream", "sync", "background"]}],`,
		routingProtocolExport("openai_responses"))

	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{left, right}))
	generation := registry.Generation()
	assert.Empty(t, registry.RoutingErrors())

	boundA, ok := generation.LookupEndpoint("POST", "/v1/responses", "model-a")
	require.True(t, ok)
	assert.Same(t, left, boundA.Plugin)
	boundB, ok := generation.LookupEndpoint("POST", "/v1/responses", "model-b")
	require.True(t, ok)
	assert.Same(t, right, boundB.Plugin)
}

func TestPreflightRoutingConflict(t *testing.T) {
	first := mustCompileRoutingPlugin(t, "preflight-first", 90, `["model-a"]`, "", "")
	second := mustCompileRoutingPlugin(t, "preflight-second", 90, `["model-b"]`, "", "")
	replacement := mustCompileRoutingPlugin(t, "preflight-first", 90, `["model-a-v2"]`, "", "")

	require.NoError(t, PreflightRoutingConflict(nil, first), "a lone candidate against a nil generation must be admitted")

	registry := NewRegistry()
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{first}))
	current := registry.Generation()

	err := PreflightRoutingConflict(current, second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preflight-first")
	assert.Contains(t, err.Error(), "channelType 90 conflicts")

	require.NoError(t, PreflightRoutingConflict(current, replacement), "same-key re-upload must replace rather than self-conflict")

	left := mustCompileRoutingPlugin(t, "preflight-left", 0, `["shared-model"]`,
		`protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		routingProtocolExport("openai_responses"))
	right := mustCompileRoutingPlugin(t, "preflight-right", 0, `["shared-model"]`,
		`protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
		routingProtocolExport("openai_responses"))
	protocolRegistry := NewRegistry()
	require.NoError(t, protocolRegistry.ReplaceOverrides([]*LoadedPlugin{left}))
	require.NoError(t, PreflightRoutingConflict(protocolRegistry.Generation(), right))
}

func TestPerProtocolModelsAcceptSharedPluginClaims(t *testing.T) {
	for _, channelType := range []int{0, 24} {
		t.Run(fmt.Sprintf("first_channel_type_%d", channelType), func(t *testing.T) {
			registry := NewRegistry()
			first := mustCompileRoutingPlugin(t, "overlap-first", channelType, `["shared-model"]`,
				`protocols: [{name: "openai_responses", models: ["shared-model"], supports: ["stream", "sync", "background"]}],`, routingProtocolExport("openai_responses"))
			second := mustCompileRoutingPlugin(t, "overlap-second", 0, `["shared-model"]`,
				`protocols: [{name: "openai_responses", models: ["shared-model"], supports: ["stream", "sync", "background"]}],`, routingProtocolExport("openai_responses"))
			require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{second, first}))
			assert.Empty(t, registry.RoutingErrors())
			generation := registry.Generation()
			candidates := generation.LookupEndpointCandidates("POST", "/v1/responses", "shared-model")
			require.Len(t, candidates, 2)
			assert.Same(t, first, candidates[0].Plugin)
			assert.Same(t, second, candidates[1].Plugin)
			assert.Equal(t, []*LoadedPlugin{first, second}, generation.PluginsByModel("shared-model"))
			metadata, ok := generation.GetByModel("shared-model")
			require.True(t, ok)
			assert.Same(t, first, metadata)
			copy := generation.PluginsByModel("shared-model")
			copy[0] = nil
			assert.Same(t, first, generation.PluginsByModel("shared-model")[0])
		})
	}
}

func TestProtocolClaimDecodeAndValidation(t *testing.T) {
	tests := []struct {
		name          string
		models        string
		metaFields    string
		exports       string
		errContains   string
		wantProtocols []ProtocolClaim
	}{
		{
			name:       "string and object entries mix",
			models:     `["gpt-5.5", "gpt-5.6"]`,
			metaFields: `protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}, {name: "openai_video", models: ["gpt-5.5"]}],`,
			exports: `export const protocols = {
				openai_responses: {
					decodeRequest: function(ctx) { return ctx; },
					renderEvents: function() { return {events: [], state: null, done: false}; },
					renderFinal: function(ctx, task) { return task; }
				},
				openai_video: {
					decodeRequest: function(ctx) { return ctx; },
					render: function(ctx, task) { return task; }
				}
			};
			export function listArtifacts() { return []; }
			export function buildContentRequest() { return {}; }`,
			wantProtocols: []ProtocolClaim{
				{Name: "openai_responses", Supports: []string{"stream", "sync", "background"}, objectForm: true},
				{Name: "openai_video", Models: []string{"gpt-5.5"}, objectForm: true},
			},
		},
		{
			name:          "absent protocols is fine",
			models:        `["gpt-5.5"]`,
			wantProtocols: []ProtocolClaim{},
		},
		{
			name:        "object with unknown field",
			models:      `["gpt-5.5"]`,
			metaFields:  `protocols: [{name: "openai_responses", models: ["gpt-5.5"], extra: true}],`,
			errContains: `unknown field "extra"`,
		},
		{
			name:        "model outside meta models",
			models:      `["gpt-5.5"]`,
			metaFields:  `protocols: [{name: "openai_responses", models: ["gpt-9.9"], supports: ["stream", "sync", "background"]}],`,
			errContains: "is not declared in plugin meta models",
		},
		{
			name:        "duplicate models in claim",
			models:      `["gpt-5.5"]`,
			metaFields:  `protocols: [{name: "openai_responses", models: ["gpt-5.5", "gpt-5.5"], supports: ["stream", "sync", "background"]}],`,
			errContains: "models must be unique",
		},
		{
			name:        "blank model in claim",
			models:      `["gpt-5.5"]`,
			metaFields:  `protocols: [{name: "openai_responses", models: [" "], supports: ["stream", "sync", "background"]}],`,
			errContains: "non-empty canonical names",
		},
		{
			name:        "empty models rejected",
			models:      `["gpt-5.5"]`,
			metaFields:  `protocols: [{name: "openai_responses", models: []}],`,
			errContains: "models must contain at least one model",
		},
		{
			name:        "explicit null protocols",
			models:      `["gpt-5.5"]`,
			metaFields:  `protocols: null,`,
			errContains: "must be an array",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			exports := testCase.exports
			if exports == "" && testCase.errContains != "" {
				exports = routingProtocolExport("openai_responses")
			}
			plugin, err := CompilePlugin(
				routingTestPluginSource("claim-decode", 0, testCase.models, testCase.metaFields, exports),
				Options{},
			)
			if testCase.errContains != "" {
				require.ErrorContains(t, err, testCase.errContains)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.wantProtocols, plugin.Meta.Protocols)
		})
	}
}

func TestRouteModelsDecodeAndValidation(t *testing.T) {
	routeExports := `export const native = {
		decodeJob: function(ctx) { return {kind: "submit", model: "gpt-5.5", requestBody: ctx.body.value}; },
		jobCreated: function(ctx, task) { return task; },
		jobStatus: function(ctx, task) { return task; }
	};`
	tests := []struct {
		name        string
		models      string
		metaFields  string
		errContains string
	}{
		{
			name:       "submit route with models",
			models:     `["gpt-5.5", "gpt-5.6"]`,
			metaFields: `routes: [{method: "POST", path: "/v1/batch", type: "submit", models: ["gpt-5.5"], decode: "decodeJob", render: "jobCreated"}],`,
		},
		{
			name:        "query route rejects models",
			models:      `["gpt-5.5"]`,
			metaFields:  `routes: [{method: "GET", path: "/v1/batch/:task_id", type: "query", models: ["gpt-5.5"], render: "jobStatus"}],`,
			errContains: "must not declare models",
		},
		{
			name:        "model outside meta models",
			models:      `["gpt-5.5"]`,
			metaFields:  `routes: [{method: "POST", path: "/v1/batch", type: "submit", models: ["gpt-9.9"], decode: "decodeJob", render: "jobCreated"}],`,
			errContains: "is not declared in plugin meta models",
		},
		{
			name:        "duplicate models",
			models:      `["gpt-5.5"]`,
			metaFields:  `routes: [{method: "POST", path: "/v1/batch", type: "submit", models: ["gpt-5.5", "gpt-5.5"], decode: "decodeJob", render: "jobCreated"}],`,
			errContains: "models must be unique",
		},
		{
			name:        "blank model entry",
			models:      `["gpt-5.5"]`,
			metaFields:  `routes: [{method: "POST", path: "/v1/batch", type: "submit", models: [" "], decode: "decodeJob", render: "jobCreated"}],`,
			errContains: "non-empty canonical names",
		},
		{
			name:        "empty models rejected",
			models:      `["gpt-5.5"]`,
			metaFields:  `routes: [{method: "POST", path: "/v1/batch", type: "submit", models: [], decode: "decodeJob", render: "jobCreated"}],`,
			errContains: "models must contain at least one model",
		},
		{
			name:        "query route empty models rejected",
			models:      `["gpt-5.5"]`,
			metaFields:  `routes: [{method: "GET", path: "/v1/batch/:task_id", type: "query", models: [], render: "jobStatus"}],`,
			errContains: "models must contain at least one model",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			plugin, err := CompilePlugin(
				routingTestPluginSource("route-models", 0, testCase.models, testCase.metaFields, routeExports),
				Options{},
			)
			if testCase.errContains != "" {
				require.ErrorContains(t, err, testCase.errContains)
				return
			}
			require.NoError(t, err)
			require.Len(t, plugin.Meta.Routes, 1)
			assert.Equal(t, []string{"gpt-5.5"}, plugin.Meta.Routes[0].Models)
		})
	}
}

func TestRouteRetainResultDecodeAndValidation(t *testing.T) {
	routeExports := `export const native = {
		decodeJob: function(ctx) { return {kind: "submit", model: "gpt-5.5", requestBody: ctx.body.value}; },
		jobCreated: function(ctx, task) { return task; },
		jobStatus: function(ctx, task) { return task; }
	};`
	falseValue := false
	tests := []struct {
		name        string
		metaFields  string
		want        *bool
		errContains string
	}{
		{
			name:       "undeclared route retains",
			metaFields: `routes: [{method: "POST", path: "/v1/batch", type: "submit", decode: "decodeJob", render: "jobCreated"}],`,
		},
		{
			name:       "submit route declines retention",
			metaFields: `routes: [{method: "POST", path: "/v1/batch", type: "submit", decode: "decodeJob", render: "jobCreated", retainResult: false}],`,
			want:       &falseValue,
		},
		{
			name:        "query route rejects retainResult",
			metaFields:  `routes: [{method: "GET", path: "/v1/batch/:task_id", type: "query", render: "jobStatus", retainResult: true}],`,
			errContains: "must not declare retainResult",
		},
		{
			name:        "non-boolean value",
			metaFields:  `routes: [{method: "POST", path: "/v1/batch", type: "submit", decode: "decodeJob", render: "jobCreated", retainResult: "no"}],`,
			errContains: "retainResult must be a boolean",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			plugin, err := CompilePlugin(
				routingTestPluginSource("route-retain", 0, `["gpt-5.5"]`, testCase.metaFields, routeExports),
				Options{},
			)
			if testCase.errContains != "" {
				require.ErrorContains(t, err, testCase.errContains)
				return
			}
			require.NoError(t, err)
			require.Len(t, plugin.Meta.Routes, 1)
			assert.Equal(t, testCase.want, plugin.Meta.Routes[0].RetainResult)
		})
	}
}

func TestRouteRequestContextClonesFormAndMultipartValuesPerDecoder(t *testing.T) {
	tests := []struct {
		name   string
		body   any
		mutate func(map[string]any)
	}{
		{
			name: "form fields",
			body: map[string]any{"kind": "form", "fields": map[string][]string{"prompt": {"original"}}},
			mutate: func(value map[string]any) {
				value["body"].(map[string]any)["fields"].(map[string][]string)["prompt"][0] = "mutated"
			},
		},
		{
			name: "multipart files",
			body: map[string]any{"kind": "multipart", "files": []map[string]any{{"ref": "request_file:image", "filename": "safe.png"}}},
			mutate: func(value map[string]any) {
				value["body"].(map[string]any)["files"].([]map[string]any)[0]["filename"] = "mutated.png"
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := RouteRequestContext{Body: testCase.body}
			first := request.JSValue()
			testCase.mutate(first)
			second := request.JSValue()
			assert.NotEqual(t, first, second)
		})
	}
}

func TestRegistryNoOpMutationsKeepCurrentGeneration(t *testing.T) {
	registry := NewRegistry()
	plugin := mustCompileRoutingPlugin(t, "stable-generation", 0, `["model"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{plugin}))
	current := registry.Generation()

	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{plugin}))
	assert.Same(t, current, registry.Generation())

	require.NoError(t, registry.Unregister("missing"))
	assert.Same(t, current, registry.Generation())
}

func TestAdjacentNodeGenerationsKeepPinnedPluginExecutable(t *testing.T) {
	compileVersion := func(version string) *LoadedPlugin {
		source := fmt.Sprintf(`
export const meta = {
	apiVersion: 1, key: "adjacent-node", name: "Adjacent", version: %q,
	author: {name: "Test"},
	models: ["model"], fetchMode: "per_task"
};
export function buildSubmitRequest() { return {version: %q}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`, version, version)
		plugin, err := CompilePlugin(source, Options{})
		require.NoError(t, err)
		return plugin
	}

	nodeA := NewRegistry()
	nodeB := NewRegistry()
	nodeAV1 := compileVersion("1.0.0")
	nodeBV1 := compileVersion("1.0.0")
	require.NoError(t, nodeA.ReplaceOverrides([]*LoadedPlugin{nodeAV1}))
	require.NoError(t, nodeB.ReplaceOverrides([]*LoadedPlugin{nodeBV1}))

	nodeAV2 := compileVersion("2.0.0")
	require.NoError(t, nodeA.ReplaceOverrides([]*LoadedPlugin{nodeAV2}))
	assert.Equal(t, nodeB.Generation().Number+1, nodeA.Generation().Number)

	pinned := PinnedPlugin{Generation: nodeB.Generation(), Plugin: nodeBV1}
	nodeBV2 := compileVersion("2.0.0")
	require.NoError(t, nodeB.ReplaceOverrides([]*LoadedPlugin{nodeBV2}))

	oldResult, err := pinned.Plugin.Engine.Call(context.Background(), "buildSubmitRequest")
	require.NoError(t, err)
	oldObject, ok := oldResult.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "1.0.0", oldObject["version"])
	current, ok := nodeB.Get("adjacent-node")
	require.True(t, ok)
	newResult, err := current.Engine.Call(context.Background(), "buildSubmitRequest")
	require.NoError(t, err)
	newObject, ok := newResult.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "2.0.0", newObject["version"])
	assert.Equal(t, uint64(1), pinned.Generation.Number)
	assert.Equal(t, uint64(2), nodeB.Generation().Number)
}

func TestRegistryReportsPartialAndFailedRebuildOutcomes(t *testing.T) {
	registry := NewRegistry()
	alpha := mustCompileRoutingPlugin(t, "outcome-alpha", 601, `["alpha"]`, "", "")
	beta := mustCompileRoutingPlugin(t, "outcome-beta", 601, `["beta"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{alpha, beta}))

	partial := registry.LastRebuildOutcome()
	assert.Equal(t, "partial", partial.Status)
	assert.Equal(t, registry.Generation().Number, partial.Generation)
	assert.Empty(t, partial.Error)

	err := registry.SetGenerationPreparer(func(_, _ *RoutingGeneration) (PreparedRoutingGeneration, error) {
		return PreparedRoutingGeneration{}, fmt.Errorf("runtime rebuild unavailable")
	})
	require.ErrorContains(t, err, "runtime rebuild unavailable")
	failed := registry.LastRebuildOutcome()
	assert.Equal(t, "failed", failed.Status)
	assert.Equal(t, registry.Generation().Number, failed.Generation)
	assert.Contains(t, failed.Error, "runtime rebuild unavailable")

	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{alpha}))
	success := registry.LastRebuildOutcome()
	assert.Equal(t, "success", success.Status)
	assert.Equal(t, registry.Generation().Number, success.Generation)
	assert.Empty(t, success.Error)
}

func TestRegistryExcludesConflictingPluginWithoutBlockingGeneration(t *testing.T) {
	tests := []struct {
		name          string
		first         *LoadedPlugin
		second        *LoadedPlugin
		expectedError string
	}{
		{
			name:          "legacy channel type",
			first:         mustCompileRoutingPlugin(t, "channel-alpha", 50, `["alpha"]`, "", ""),
			second:        mustCompileRoutingPlugin(t, "channel-beta", 50, `["beta"]`, "", ""),
			expectedError: "channelType 50 conflicts",
		},
		{
			name:          "overlapping channelTypes entry",
			first:         mustCompileRoutingPlugin(t, "channel-alpha", 0, `["alpha"]`, `channelTypes: [55, 1],`, ""),
			second:        mustCompileRoutingPlugin(t, "channel-beta", 0, `["beta"]`, `channelTypes: [1],`, ""),
			expectedError: "channelType 1 conflicts",
		},
		{
			name: "route shape",
			first: mustCompileRoutingPlugin(t, "route-alpha", 0, `["alpha"]`,
				`routes: [{method: "GET", path: "/vendor/jobs/:task_id", type: "query", render: "status"}],`,
				`export const native = {status: function(ctx, task) { return task; }};`),
			second: mustCompileRoutingPlugin(t, "route-beta", 0, `["beta"]`,
				`routes: [{method: "GET", path: "/vendor/jobs/:id", type: "query", render: "status", taskIdParam: "id"}],`,
				`export const native = {status: function(ctx, task) { return task; }};`),
			expectedError: "route GET /vendor/jobs/:id conflicts",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			registry := NewRegistry()
			require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{testCase.first}))
			before := registry.Generation()

			err := registry.ReplaceOverrides([]*LoadedPlugin{testCase.first, testCase.second})

			require.NoError(t, err)
			assert.Equal(t, before.Number+1, registry.Generation().Number)
			_, exists := registry.Get(testCase.second.Meta.Key)
			assert.False(t, exists)
			assert.Contains(t, registry.RoutingErrors()[testCase.second.Meta.Key], testCase.expectedError)
		})
	}
}

func TestRegistryRetainsIncumbentWhenUpdatedPluginConflicts(t *testing.T) {
	registry := NewRegistry()
	incumbent := mustCompileRoutingPlugin(t, "incumbent", 70, `["model-v1"]`, "", "")
	other := mustCompileRoutingPlugin(t, "other", 71, `["other-model"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{incumbent, other}))

	conflictingUpdate := mustCompileRoutingPlugin(t, "incumbent", 71, `["model-v2"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{conflictingUpdate, other}))

	active, ok := registry.Get("incumbent")
	require.True(t, ok)
	assert.Same(t, incumbent, active)
	assert.Same(t, conflictingUpdate, registry.OverridePlugins()["incumbent"])
	assert.Same(t, incumbent, registry.ActiveOverridePlugins()["incumbent"])
	require.Contains(t, registry.RoutingErrors(), "incumbent")

	fixedUpdate := mustCompileRoutingPlugin(t, "incumbent", 72, `["model-v2"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{fixedUpdate, other}))
	active, ok = registry.Get("incumbent")
	require.True(t, ok)
	assert.Same(t, fixedUpdate, active)
	assert.NotContains(t, registry.RoutingErrors(), "incumbent")
}

func TestRegistryAdmitsNewConflictsInDeterministicKeyOrder(t *testing.T) {
	registry := NewRegistry()
	alpha := mustCompileRoutingPlugin(t, "alpha", 80, `["alpha-model"]`, "", "")
	beta := mustCompileRoutingPlugin(t, "beta", 80, `["beta-model"]`, "", "")

	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{beta, alpha}))

	active, ok := registry.Get("alpha")
	require.True(t, ok)
	assert.Same(t, alpha, active)
	_, betaActive := registry.Get("beta")
	assert.False(t, betaActive)
	assert.Contains(t, registry.RoutingErrors()["beta"], "channelType 80 conflicts")
}

func TestRegistryPublishesHealthyUpdateAlongsideRejectedUpdate(t *testing.T) {
	registry := NewRegistry()
	healthyV1 := mustCompileRoutingPlugin(t, "healthy", 81, `["healthy-v1"]`, "", "")
	offenderV1 := mustCompileRoutingPlugin(t, "offender", 82, `["offender-v1"]`, "", "")
	owner := mustCompileRoutingPlugin(t, "owner", 83, `["owner"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{healthyV1, offenderV1, owner}))

	healthyV2 := mustCompileRoutingPlugin(t, "healthy", 84, `["healthy-v2"]`, "", "")
	offenderV2 := mustCompileRoutingPlugin(t, "offender", 83, `["offender-v2"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{healthyV2, offenderV2, owner}))

	activeHealthy, ok := registry.Get("healthy")
	require.True(t, ok)
	assert.Same(t, healthyV2, activeHealthy)
	activeOffender, ok := registry.Get("offender")
	require.True(t, ok)
	assert.Same(t, offenderV1, activeOffender)
	assert.Contains(t, registry.RoutingErrors()["offender"], "channelType 83 conflicts")
}

func TestRegistryAdmitsInterdependentUpdatesAsOneGeneration(t *testing.T) {
	tests := []struct {
		name       string
		firstType  int
		secondType int
	}{
		{name: "one update frees the type needed by another", firstType: 102, secondType: 103},
		{name: "two plugins swap types", firstType: 102, secondType: 101},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			registry := NewRegistry()
			firstV1 := mustCompileRoutingPlugin(t, "dependent-first", 101, `["first-v1"]`, "", "")
			secondV1 := mustCompileRoutingPlugin(t, "dependent-second", 102, `["second-v1"]`, "", "")
			require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{firstV1, secondV1}))

			firstV2 := mustCompileRoutingPlugin(t, "dependent-first", testCase.firstType, `["first-v2"]`, "", "")
			secondV2 := mustCompileRoutingPlugin(t, "dependent-second", testCase.secondType, `["second-v2"]`, "", "")
			require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{firstV2, secondV2}))

			activeFirst, ok := registry.Get("dependent-first")
			require.True(t, ok)
			assert.Same(t, firstV2, activeFirst)
			activeSecond, ok := registry.Get("dependent-second")
			require.True(t, ok)
			assert.Same(t, secondV2, activeSecond)
			assert.Empty(t, registry.RoutingErrors())
		})
	}
}

func TestRejectedUpdateDoesNotRestoreIncumbentAheadOfHealthyPeer(t *testing.T) {
	registry := NewRegistry()
	alphaV1 := mustCompileRoutingPlugin(t, "fallback-alpha", 111, `["alpha-v1"]`, "", "")
	betaV1 := mustCompileRoutingPlugin(t, "fallback-beta", 112, `["beta-v1"]`, "", "")
	owner := mustCompileRoutingPlugin(t, "fallback-owner", 113, `["owner"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{alphaV1, betaV1, owner}))

	alphaV2 := mustCompileRoutingPlugin(t, "fallback-alpha", 113, `["alpha-v2"]`, "", "")
	betaV2 := mustCompileRoutingPlugin(t, "fallback-beta", 111, `["beta-v2"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{alphaV2, betaV2, owner}))

	_, alphaActive := registry.Get("fallback-alpha")
	assert.False(t, alphaActive)
	activeBeta, ok := registry.Get("fallback-beta")
	require.True(t, ok)
	assert.Same(t, betaV2, activeBeta)
	activeOwner, ok := registry.Get("fallback-owner")
	require.True(t, ok)
	assert.Same(t, owner, activeOwner)
	assert.Contains(t, registry.RoutingErrors()["fallback-alpha"], "channelType 113 conflicts")
	assert.NotContains(t, registry.RoutingErrors(), "fallback-beta")
}

func TestRemovingOverrideNeverRetainsRemovedIncumbent(t *testing.T) {
	registry := NewRegistry()
	override := mustCompileRoutingPlugin(t, "fallback", 92, `["override"]`, "", "")
	owner := mustCompileRoutingPlugin(t, "owner", 91, `["owner"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{override, owner}))
	factory, err := registry.RegisterFactory(routingTestPluginSource("fallback", 91, `["factory"]`, "", ""), Options{})
	require.NoError(t, err)

	require.NoError(t, registry.Unregister("fallback"))

	_, fallbackActive := registry.Get("fallback")
	assert.False(t, fallbackActive)
	ownerActive, ok := registry.Get("owner")
	require.True(t, ok)
	assert.Same(t, owner, ownerActive)
	assert.NotContains(t, registry.OverridePlugins(), "fallback")
	assert.NotContains(t, registry.ActiveOverridePlugins(), "fallback")
	assert.Contains(t, registry.RoutingErrors()["fallback"], "channelType 91 conflicts")

	require.NoError(t, registry.Unregister("owner"))
	fallbackPlugin, ok := registry.Get("fallback")
	require.True(t, ok)
	assert.Same(t, factory, fallbackPlugin)
}

func TestRejectedNewOverrideRetainsFactoryIncumbent(t *testing.T) {
	registry := NewRegistry()
	factorySource := routingTestPluginSource("factory-fallback", 121, `["factory"]`, "", "")
	factory, err := registry.RegisterFactory(factorySource, Options{})
	require.NoError(t, err)
	owner := mustCompileRoutingPlugin(t, "factory-owner", 122, `["owner"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{owner}))

	conflictingOverride := mustCompileRoutingPlugin(t, "factory-fallback", 122, `["override"]`, "", "")
	require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{conflictingOverride, owner}))

	active, ok := registry.Get("factory-fallback")
	require.True(t, ok)
	assert.Same(t, factory, active)
	assert.Same(t, conflictingOverride, registry.OverridePlugins()["factory-fallback"])
	assert.NotContains(t, registry.ActiveOverridePlugins(), "factory-fallback")
	assert.Contains(t, registry.RoutingErrors()["factory-fallback"], "channelType 122 conflicts")
}

func TestGenericChannelTypesDoNotCreateLegacyIdentityConflicts(t *testing.T) {
	for _, channelType := range []int{0, constant.ChannelTypeTaskPlugin} {
		t.Run(fmt.Sprintf("channel_%d", channelType), func(t *testing.T) {
			registry := NewRegistry()
			first := mustCompileRoutingPlugin(t, "generic-alpha", channelType, `["alpha"]`, "", "")
			second := mustCompileRoutingPlugin(t, "generic-beta", channelType, `["beta"]`, "", "")

			require.NoError(t, registry.ReplaceOverrides([]*LoadedPlugin{first, second}))
			_, found := registry.GetByChannelType(channelType)
			assert.False(t, found)
		})
	}
}

func TestDeclarativeRouteValidationProtectsNamespacesAndCanonicalSyntax(t *testing.T) {
	tests := []struct {
		name          string
		route         Route
		expectedError string
	}{
		{
			name:          "core API",
			route:         Route{Method: "POST", Path: "/api/plugin", Type: RouteTypeSubmit},
			expectedError: "reserved namespace /api",
		},
		{
			name:          "generic task management",
			route:         Route{Method: "GET", Path: "/v1/tasks/:task_id", Type: RouteTypeQuery, Render: "native"},
			expectedError: "reserved namespace /v1/tasks",
		},
		{
			name:          "SPA root",
			route:         Route{Method: "POST", Path: "/console/jobs", Type: RouteTypeSubmit},
			expectedError: "reserved namespace /console",
		},
		{
			name:          "dynamic root can claim reserved subtree",
			route:         Route{Method: "POST", Path: "/:root/jobs", Type: RouteTypeSubmit},
			expectedError: "intersects reserved namespace",
		},
		{
			name:          "dynamic v1 namespace can claim task management",
			route:         Route{Method: "POST", Path: "/v1/:namespace", Type: RouteTypeSubmit},
			expectedError: "reserved namespace /v1/tasks",
		},
		{
			name:          "root catch-all can claim reserved subtree",
			route:         Route{Method: "POST", Path: "/*rest", Type: RouteTypeSubmit},
			expectedError: "intersects reserved namespace",
		},
		{
			name:          "repeated slash",
			route:         Route{Method: "POST", Path: "/vendor//jobs", Type: RouteTypeSubmit},
			expectedError: "empty segments",
		},
		{
			name:          "noncanonical method",
			route:         Route{Method: " post ", Path: "/vendor/jobs", Type: RouteTypeSubmit},
			expectedError: "canonical uppercase",
		},
		{
			name:          "non-terminal catch-all",
			route:         Route{Method: "POST", Path: "/vendor/*rest/jobs", Type: RouteTypeSubmit},
			expectedError: "invalid catch-all",
		},
		{
			name:          "query id mismatch",
			route:         Route{Method: "GET", Path: "/vendor/jobs/:id", Type: RouteTypeQuery, Render: "native"},
			expectedError: "must contain :task_id",
		},
		{
			name:          "query declares decoder",
			route:         Route{Method: "GET", Path: "/vendor/jobs/:task_id", Type: RouteTypeQuery, Decode: "decode", Render: "show"},
			expectedError: "must not declare decode",
		},
		{
			name:          "submit missing presenter",
			route:         Route{Method: "POST", Path: "/vendor/jobs", Type: RouteTypeSubmit, Decode: "decode"},
			expectedError: "must declare decode and render",
		},
		{
			name:          "dynamic missing decoder",
			route:         Route{Method: "POST", Path: "/vendor/query", Type: RouteTypeDynamic, Render: "show"},
			expectedError: "must declare decode and render",
		},
		{
			name:          "dynamic missing presenter",
			route:         Route{Method: "POST", Path: "/vendor/query", Type: RouteTypeDynamic, Decode: "decode"},
			expectedError: "must declare decode and render",
		},
		{
			name:          "query declares action",
			route:         Route{Method: "GET", Path: "/vendor/jobs/:task_id", Type: RouteTypeQuery, Render: "show", Action: "retrieve"},
			expectedError: "must not declare action",
		},
		{
			name:          "submit declares task id parameter",
			route:         Route{Method: "POST", Path: "/vendor/jobs", Type: RouteTypeSubmit, Decode: "decode", Render: "created", TaskIDParam: "task_id"},
			expectedError: "must not declare taskIdParam",
		},
		{
			name:          "dynamic declares task id parameter",
			route:         Route{Method: "POST", Path: "/vendor/query", Type: RouteTypeDynamic, Decode: "decode", Render: "show", TaskIDParam: "task_id"},
			expectedError: "must not declare taskIdParam",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			meta := Meta{APIVersion: 1, Key: "validation", Name: "Validation", Version: "1.0.0", Author: AuthorMeta{Name: "Test"}, Models: []string{"model"}, FetchMode: "per_task", Routes: []Route{testCase.route}}
			require.ErrorContains(t, ValidateV1Meta(meta), testCase.expectedError)
		})
	}
}

func TestRemovedEndpointsAndProtocolHooksAreValidatedAtCompileTime(t *testing.T) {
	removed := routingTestPluginSource(
		"removed-endpoint",
		0,
		`["model"]`,
		`endpoints: [{method: "POST", path: "/v1/chat/completions", protocol: "chat"}],`,
		routingProtocolExport("chat"),
	)
	_, err := CompilePlugin(removed, Options{})
	require.ErrorContains(t, err, "endpoints is no longer supported")

	unknown := routingTestPluginSource("unknown-protocol", 0, `["model"]`, `protocols: ["chat"],`, ``)
	_, err = CompilePlugin(unknown, Options{})
	require.ErrorContains(t, err, `protocol "chat" is unknown`)

	missingHook := routingTestPluginSource(
		"bad-protocol",
		0,
		`["model"]`,
		`protocols: ["openai_responses"],`,
		`export const protocols = {openai_responses: {
			decodeRequest: function() { return {}; },
			renderEvents: function() { return {events: [], done: false}; }
		}};`,
	)
	_, err = CompilePlugin(missingHook, Options{})
	require.ErrorContains(t, err, `plugin bad-protocol protocol "openai_responses" must declare supports; replace the bare string with {name: "openai_responses", supports: [...]} choosing from "stream", "sync", "background"`)

	for _, removedExport := range []string{"renderers", "renderError", "resolveRequest"} {
		t.Run("removed export "+removedExport, func(t *testing.T) {
			source := routingTestPluginSource("removed-export", 0, `["model"]`, "", `export const `+removedExport+` = {};`)
			_, compileErr := CompilePlugin(source, Options{})
			require.ErrorContains(t, compileErr, `export "`+removedExport+`" is no longer supported`)
		})
	}

	t.Run("removed route renderer", func(t *testing.T) {
		source := routingTestPluginSource(
			"removed-route-renderer", 0, `["model"]`,
			`routes: [{method:"GET",path:"/vendor/:task_id",type:"query",render:"show",renderer:"legacy"}],`,
			`export const native = {show: function(ctx, task) { return task; }};`,
		)
		_, compileErr := CompilePlugin(source, Options{})
		require.ErrorContains(t, compileErr, "field renderer is no longer supported")
	})
}

func TestRegistryRejectsPrototypeInheritedHooks(t *testing.T) {
	for _, member := range []string{"constructor", "toString", "__proto__"} {
		t.Run("native "+member, func(t *testing.T) {
			source := routingTestPluginSource(
				"inherited-renderer",
				0,
				`["model"]`,
				fmt.Sprintf(`routes: [{method: "GET", path: "/vendor/jobs/:task_id", type: "query", render: %q}],`, member),
				`const nativePrototype = {
					constructor: function(task) { return task; },
					toString: function(task) { return task; },
					["__proto__"]: function(task) { return task; },
				};
				export const native = Object.create(nativePrototype);`,
			)

			_, err := CompilePlugin(source, Options{})
			require.ErrorContains(t, err, fmt.Sprintf(`references missing native render %q`, member))
		})
	}

	t.Run("protocol object", func(t *testing.T) {
		source := routingTestPluginSource(
			"inherited-protocol",
			0,
			`["model"]`,
			`protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
			`const protocol = {
				decodeRequest: function(ctx) { return ctx; },
				renderEvents: function() { return {events: [], state: null, done: false}; },
				renderFinal: function(ctx, task) { return task; },
			};
			export const protocols = Object.create({openai_responses: protocol});`,
		)

		_, err := CompilePlugin(source, Options{})
		require.ErrorContains(t, err, `protocol "openai_responses" is missing hook "decodeRequest"`)
	})

	t.Run("protocol hook", func(t *testing.T) {
		source := routingTestPluginSource(
			"inherited-protocol-hook",
			0,
			`["model"]`,
			`protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],`,
			`const protocolPrototype = {
				decodeRequest: function(ctx) { return ctx; },
				renderEvents: function() { return {events: [], state: null, done: false}; },
				renderFinal: function(ctx, task) { return task; },
			};
			export const protocols = {openai_responses: Object.create(protocolPrototype)};`,
		)

		_, err := CompilePlugin(source, Options{})
		require.ErrorContains(t, err, `protocol "openai_responses" is missing hook "decodeRequest"`)
	})
}

func TestMetaDecoderRejectsLossyOrUnknownRoutingFields(t *testing.T) {
	tests := []struct {
		name          string
		metaFields    string
		expectedError string
	}{
		{
			name:          "fractional channel type",
			metaFields:    `channelTypes: [50.5],`,
			expectedError: "channelTypes element 1 must be an integer",
		},
		{
			name:          "non-string model",
			metaFields:    `models: ["model", 2],`,
			expectedError: "models must be an array of strings",
		},
		{
			name:          "unknown route field",
			metaFields:    `routes: [{method: "POST", path: "/vendor/jobs", type: "submit", auth: false}],`,
			expectedError: `unknown field "auth"`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			source := fmt.Sprintf(`
export const meta = {
	apiVersion: 1, key: "strict-meta", name: "Strict", version: "1.0.0",
	author: {name: "Test"},
	models: ["model"], fetchMode: "per_task", %s
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
export function resolveRequest() { return {kind: "submit", model: "model"}; }
`, testCase.metaFields)
			_, err := CompilePlugin(source, Options{})
			require.ErrorContains(t, err, testCase.expectedError)
		})
	}
}

func TestProtocolHookInspectionIsDeadlineBounded(t *testing.T) {
	source := `
export const meta = {
	apiVersion: 1, key: "getter-timeout", name: "Getter", version: "1.0.0",
	author: {name: "Test"},
	models: ["model"], fetchMode: "per_task",
	protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
export const protocols = {
	get openai_responses() { while (true) {} }
};
`
	_, err := CompilePlugin(source, Options{Timeout: 20 * time.Millisecond})
	require.ErrorContains(t, err, "inspection interrupted")
}

func TestReservedNamespaceChecksUseSegmentBoundaries(t *testing.T) {
	meta := Meta{
		APIVersion: 1,
		Key:        "apiary",
		Name:       "Apiary",
		Version:    "1.0.0",
		Author:     AuthorMeta{Name: "Test"},
		Models:     []string{"model"},
		FetchMode:  "per_task",
		Routes:     []Route{{Method: "POST", Path: "/apiary/jobs", Type: RouteTypeSubmit, Decode: "decode", Render: "render"}},
	}
	require.NoError(t, ValidateV1Meta(meta))
}

func TestResolveRouteActionPrefersResolvedAction(t *testing.T) {
	route := Route{Action: "manifest-action"}
	assert.Equal(t, "hook-action", ResolveRouteAction(route, "hook-action"))
	assert.Equal(t, "manifest-action", ResolveRouteAction(route, ""))
}

func mustCompileRoutingPlugin(t *testing.T, key string, channelType int, models, metaFields, exports string) *LoadedPlugin {
	t.Helper()
	plugin, err := CompilePlugin(routingTestPluginSource(key, channelType, models, metaFields, exports), Options{})
	require.NoError(t, err)
	return plugin
}

func routingTestPluginSource(key string, channelType int, models, metaFields, exports string) string {
	channelTypesField := ""
	if channelType > 0 && channelType != constant.ChannelTypeTaskPlugin {
		channelTypesField = fmt.Sprintf("channelTypes: [%d],", channelType)
	}
	return fmt.Sprintf(`
export const meta = {
	apiVersion: 1,
	key: %q,
	name: %q,
	version: "1.0.0",
	author: {name: "Test"},
	%s
	models: %s,
	fetchMode: "per_task",
	%s
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
%s
`, key, key, channelTypesField, models, metaFields, exports)
}

func routingProtocolExport(name string) string {
	return fmt.Sprintf(`export const protocols = {%s: {
		decodeRequest: function(ctx) { return ctx; },
		renderEvents: function() { return {events: [], state: null, done: false}; },
		renderFinal: function(ctx, task) { return task; }
	}};`, name)
}
