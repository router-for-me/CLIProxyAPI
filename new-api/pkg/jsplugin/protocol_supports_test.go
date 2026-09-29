package jsplugin

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	responsesDecodeOnly = `export const protocols = {openai_responses: {
		decodeRequest: function(ctx) { return ctx; }
	}};`
	responsesDecodeEvents = `export const protocols = {openai_responses: {
		decodeRequest: function(ctx) { return ctx; },
		renderEvents: function() { return {events: [], state: null, done: false}; }
	}};`
	responsesDecodeFinal = `export const protocols = {openai_responses: {
		decodeRequest: function(ctx) { return ctx; },
		renderFinal: function(ctx, task) { return task; }
	}};`
	responsesDecodeBoth = `export const protocols = {openai_responses: {
		decodeRequest: function(ctx) { return ctx; },
		renderEvents: function() { return {events: [], state: null, done: false}; },
		renderFinal: function(ctx, task) { return task; }
	}};`
	videoProtocolExport = `export const protocols = {openai_video: {
		decodeRequest: function(ctx) { return ctx; },
		render: function(ctx, task) { return task; }
	}};
	export function listArtifacts() { return []; }
	export function buildContentRequest() { return {}; }`
)

func TestProtocolSupportsLoadErrors(t *testing.T) {
	tests := []struct {
		name      string
		protocols string
		exports   string
		err       string
	}{
		{
			name:      "bare string",
			protocols: `["openai_responses"]`,
			exports:   responsesDecodeBoth,
			err:       `plugin acme protocol "openai_responses" must declare supports; replace the bare string with {name: "openai_responses", supports: [...]} choosing from "stream", "sync", "background"`,
		},
		{
			name:      "object without supports",
			protocols: `[{name: "openai_responses"}]`,
			exports:   responsesDecodeBoth,
			err:       `plugin acme protocol "openai_responses" must declare supports; add supports: [...] choosing from "stream", "sync", "background"`,
		},
		{
			name:      "supports sync but only renderEvents",
			protocols: `[{name: "openai_responses", supports: ["sync"]}]`,
			exports:   responsesDecodeEvents,
			err:       `plugin acme protocol "openai_responses" supports "sync" but does not export protocols.openai_responses.renderFinal; implement it or declare supports: ["stream"]`,
		},
		{
			name:      "supports sync with only decodeRequest",
			protocols: `[{name: "openai_responses", supports: ["sync"]}]`,
			exports:   responsesDecodeOnly,
			err:       `plugin acme protocol "openai_responses" supports "sync" but does not export protocols.openai_responses.renderFinal; implement it`,
		},
		{
			name:      "supports stream but also exports renderFinal",
			protocols: `[{name: "openai_responses", supports: ["stream"]}]`,
			exports:   responsesDecodeBoth,
			err:       `plugin acme protocol "openai_responses" exports protocols.openai_responses.renderFinal but no supported mode uses it; add "sync" or "background" to supports or remove the hook`,
		},
		{
			name:      "supports sync and background but also exports renderEvents",
			protocols: `[{name: "openai_responses", supports: ["sync", "background"]}]`,
			exports:   responsesDecodeBoth,
			err:       `plugin acme protocol "openai_responses" exports protocols.openai_responses.renderEvents but no supported mode uses it; add "stream" to supports or remove the hook`,
		},
		{
			name:      "empty supports",
			protocols: `[{name: "openai_responses", supports: []}]`,
			exports:   responsesDecodeBoth,
			err:       `plugin acme protocol "openai_responses" supports must contain at least one of "stream", "sync", "background"`,
		},
		{
			name:      "duplicate supports",
			protocols: `[{name: "openai_responses", supports: ["stream", "stream"]}]`,
			exports:   responsesDecodeBoth,
			err:       `plugin acme protocol "openai_responses" supports must be unique`,
		},
		{
			name:      "retrieve is not a mode",
			protocols: `[{name: "openai_responses", supports: ["retrieve"]}]`,
			exports:   responsesDecodeBoth,
			err:       `plugin acme protocol "openai_responses" has no mode "retrieve"; retrieval of a created response is always available and is never declared`,
		},
		{
			name:      "openai_video forbids supports",
			protocols: `[{name: "openai_video", supports: ["stream"]}]`,
			exports:   videoProtocolExport,
			err:       `plugin acme protocol "openai_video" does not define modes; supports is not allowed`,
		},
		{
			name:      "unknown protocol forbids supports",
			protocols: `[{name: "openai_custom", supports: ["stream"]}]`,
			err:       `plugin acme protocol "openai_custom" does not define modes; supports is not allowed`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := compileProtocolPlugin(t, "acme", `["model"]`, testCase.protocols, testCase.exports)
			require.ErrorContains(t, err, testCase.err)
		})
	}
}

func TestProtocolSupportsHappyPaths(t *testing.T) {
	tests := []struct {
		name          string
		models        string
		protocols     string
		exports       string
		wantProtocols []ProtocolClaim
	}{
		{
			name:      "stream only with renderEvents",
			models:    `["model"]`,
			protocols: `[{name: "openai_responses", supports: ["stream"]}]`,
			exports:   responsesDecodeEvents,
			wantProtocols: []ProtocolClaim{
				{Name: "openai_responses", Supports: []string{"stream"}, objectForm: true},
			},
		},
		{
			name:      "sync and background with renderFinal",
			models:    `["model"]`,
			protocols: `[{name: "openai_responses", supports: ["sync", "background"]}]`,
			exports:   responsesDecodeFinal,
			wantProtocols: []ProtocolClaim{
				{Name: "openai_responses", Supports: []string{"sync", "background"}, objectForm: true},
			},
		},
		{
			name:      "all modes normalize to table order",
			models:    `["model"]`,
			protocols: `[{name: "openai_responses", supports: ["background", "stream", "sync"]}]`,
			exports:   responsesDecodeBoth,
			wantProtocols: []ProtocolClaim{
				{Name: "openai_responses", Supports: []string{"stream", "sync", "background"}, objectForm: true},
			},
		},
		{
			name:      "openai_video object without supports",
			models:    `["gpt-5.5", "gpt-5.6"]`,
			protocols: `[{name: "openai_video", models: ["gpt-5.5"]}]`,
			exports:   videoProtocolExport,
			wantProtocols: []ProtocolClaim{
				{Name: "openai_video", Models: []string{"gpt-5.5"}, objectForm: true},
			},
		},
		{
			name:      "bare openai_video",
			models:    `["model"]`,
			protocols: `["openai_video"]`,
			exports:   videoProtocolExport,
			wantProtocols: []ProtocolClaim{
				{Name: "openai_video"},
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			plugin, err := compileProtocolPlugin(t, "acme", testCase.models, testCase.protocols, testCase.exports)
			require.NoError(t, err)
			require.Equal(t, testCase.wantProtocols, plugin.Meta.Protocols)
		})
	}
}

func TestCloneMetaDeepCopiesSupports(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Register(protocolPluginSource(
		"acme",
		`["model"]`,
		`[{name: "openai_responses", supports: ["stream", "sync", "background"]}]`,
		responsesDecodeBoth,
	), Options{})
	require.NoError(t, err)

	snapshot := registry.Snapshot()
	require.Len(t, snapshot.Override, 1)
	require.Len(t, snapshot.Override[0].Protocols, 1)
	snapshot.Override[0].Protocols[0].Supports[0] = "mutated"

	plugin, ok := registry.Get("acme")
	require.True(t, ok)
	assert.Equal(t, []string{"stream", "sync", "background"}, plugin.Meta.Protocols[0].Supports)
}

func TestMetaProtocolSupports(t *testing.T) {
	streamOnly, err := compileProtocolPlugin(t, "acme", `["model"]`,
		`[{name: "openai_responses", supports: ["stream"]}]`, responsesDecodeEvents)
	require.NoError(t, err)
	assert.True(t, streamOnly.Meta.ProtocolSupports("openai_responses", "stream"))
	assert.False(t, streamOnly.Meta.ProtocolSupports("openai_responses", "sync"))
	assert.False(t, streamOnly.Meta.ProtocolSupports("openai_responses", "background"))
	assert.False(t, streamOnly.Meta.ProtocolSupports("openai_responses", "retrieve"))
	assert.False(t, streamOnly.Meta.ProtocolSupports("openai_video", "stream"))
	assert.False(t, streamOnly.Meta.ProtocolSupports("missing", "stream"))

	video, err := compileProtocolPlugin(t, "acme-video", `["model"]`, `["openai_video"]`, videoProtocolExport)
	require.NoError(t, err)
	assert.False(t, video.Meta.ProtocolSupports("openai_video", "stream"))
}

func TestProtocolClaimMarshalEmitsSupportsInTableOrder(t *testing.T) {
	plugin, err := compileProtocolPlugin(t, "acme", `["model"]`,
		`[{name: "openai_responses", supports: ["background", "sync", "stream"]}]`, responsesDecodeBoth)
	require.NoError(t, err)
	encoded, err := common.Marshal(plugin.Meta.Protocols[0])
	require.NoError(t, err)
	assert.Equal(t, `{"name":"openai_responses","supports":["stream","sync","background"]}`, string(encoded))
}

func compileProtocolPlugin(t *testing.T, key, models, protocols, exports string) (*LoadedPlugin, error) {
	t.Helper()
	return CompilePlugin(protocolPluginSource(key, models, protocols, exports), Options{})
}

func protocolPluginSource(key, models, protocols, exports string) string {
	return fmt.Sprintf(`
export const meta = {
	apiVersion: 1, key: %q, name: %q, version: "1.0.0",
	author: {name: "Test"},
	models: %s, fetchMode: "per_task",
	protocols: %s,
};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
%s
`, key, key, models, protocols, exports)
}
