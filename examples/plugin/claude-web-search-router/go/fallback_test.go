package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func claudeWebSearchRouteBody(t *testing.T) []byte {
	t.Helper()
	body := []byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5}],
		"system":[{"type":"text","text":"You have access to the web search tool use."}],
		"messages":[{"role":"user","content":[{"type":"text","text":"Perform a web search for the query: test"}]}]
	}`)
	return body
}

func decodeModelRouteResponse(t *testing.T, raw []byte) pluginapi.ModelRouteResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp pluginapi.ModelRouteResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRouteWithFallbackAntigravityFirst(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	const clientID = "test-fallback-antigravity"
	reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{ID: "gem-fallback-test", SupportsWebSearch: true},
	})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	currentConfig.Store(pluginConfig{
		Enabled: true,
		Route:   string(backendFallback),
	})
	raw, err := routeModel(mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:       "claude",
			Body:               claudeWebSearchRouteBody(t),
			RequestedModel:     "claude-sonnet-4-6",
			AvailableProviders: []string{"antigravity", "codex", "xai"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeModelRouteResponse(t, raw)
	if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestRouteWithFallbackSkipsAntigravityToCodex(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled: true,
		Route:   string(backendFallback),
	})
	raw, err := routeModel(mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:       "claude",
			Body:               claudeWebSearchRouteBody(t),
			RequestedModel:     "claude-sonnet-4-6",
			AvailableProviders: []string{"codex", "xai"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeModelRouteResponse(t, raw)
	if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestRouteWithFallbackToTavily(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled:       true,
		Route:         string(backendFallback),
		TavilyAPIKeys: []string{"tvly-test"},
	})
	raw, err := routeModel(mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:       "claude",
			Body:               claudeWebSearchRouteBody(t),
			AvailableProviders: []string{},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeModelRouteResponse(t, raw)
	if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestRouteWithFallbackExhausted(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled: true,
		Route:   string(backendFallback),
	})
	raw, err := routeModel(mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:       "claude",
			Body:               claudeWebSearchRouteBody(t),
			AvailableProviders: []string{},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeModelRouteResponse(t, raw)
	if resp.Handled {
		t.Fatalf("expected declined, got %#v", resp)
	}
	if resp.Reason == "" || resp.Reason[:len("web_search_fallback_exhausted")] != "web_search_fallback_exhausted" {
		t.Fatalf("reason = %q", resp.Reason)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
