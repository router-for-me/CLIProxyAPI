package common

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestIsClaudeWebSearchToolTypeAcceptsFutureVersions(t *testing.T) {
	for _, toolType := range []string{
		"web_search_20250305",
		"web_search_20260209",
		"web_search_20990101",
		"WEB_SEARCH_20250305",
	} {
		if !IsClaudeWebSearchToolType(toolType) {
			t.Fatalf("%q should match", toolType)
		}
	}
	for _, toolType := range []string{"", "custom", "web_search", "web_fetch_20250305", "my_web_search_x"} {
		if IsClaudeWebSearchToolType(toolType) {
			t.Fatalf("%q should not match", toolType)
		}
	}
}

func TestIsResponsesWebSearchToolTypeVersions(t *testing.T) {
	for _, toolType := range []string{
		"web_search",
		"web_search_2025_08_26",
		"web_search_preview",
		"web_search_preview_2025_03_11",
	} {
		if !IsResponsesWebSearchToolType(toolType) {
			t.Fatalf("%q should match", toolType)
		}
	}
	for _, toolType := range []string{"", "function", "web_search_20250305", "x_search", "image_generation"} {
		if IsResponsesWebSearchToolType(toolType) {
			t.Fatalf("%q should not match", toolType)
		}
	}
}

func TestGeminiModelSupportsWebSearchDynamicFlag(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-common-websearch", "gemini", []*registry.ModelInfo{
		{ID: "common-search-capable", SupportsWebSearch: true},
		{ID: "common-search-plain"},
	})
	t.Cleanup(func() { reg.UnregisterClient("test-common-websearch") })

	if !GeminiModelSupportsWebSearch("common-search-capable") {
		t.Fatal("flagged model should be supported")
	}
	if GeminiModelSupportsWebSearch("common-search-plain") {
		t.Fatal("unflagged model should not be supported")
	}
	if GeminiModelSupportsWebSearch("common-search-missing") {
		t.Fatal("unknown model should not be supported")
	}
}
