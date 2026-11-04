package api

import "testing"

func TestHomeModelNativeCapabilitiesParsesToolSearch(t *testing.T) {
	both := homeModelNativeCapabilities(map[string]any{"native_capabilities": map[string]any{"web_search": true, "tool_search": false}})
	if both == nil || both.WebSearch == nil || !*both.WebSearch || both.ToolSearch == nil || *both.ToolSearch {
		t.Fatalf("web_search+tool_search = %+v", both)
	}
	toolOnly := homeModelNativeCapabilities(map[string]any{"native_capabilities": map[string]any{"tool_search": true}})
	if toolOnly == nil || toolOnly.WebSearch != nil || toolOnly.ToolSearch == nil || !*toolOnly.ToolSearch {
		t.Fatalf("tool_search only = %+v", toolOnly)
	}
	if missing := homeModelNativeCapabilities(map[string]any{"id": "m"}); missing != nil {
		t.Fatalf("missing native_capabilities = %+v, want nil", missing)
	}
}
