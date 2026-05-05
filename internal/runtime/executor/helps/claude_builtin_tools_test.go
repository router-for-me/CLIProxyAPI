package helps

import "testing"

func TestIsClaudeBuiltinToolType_KnownFamilies(t *testing.T) {
	tests := []struct {
		name     string
		toolType string
		want     bool
	}{
		{name: "empty", toolType: "", want: false},
		{name: "custom", toolType: "custom", want: false},
		{name: "unknown typed", toolType: "custom_builtin_20250401", want: false},
		{name: "web search", toolType: "web_search_20250305", want: true},
		{name: "computer", toolType: "computer_20250124", want: true},
		{name: "bash", toolType: "bash_20250124", want: true},
		{name: "memory", toolType: "memory_20250818", want: true},
		{name: "web fetch", toolType: "web_fetch_20260209", want: true},
		{name: "tool search", toolType: "tool_search_tool_regex_20251119", want: true},
		{name: "advisor", toolType: "advisor_20260301", want: true},
		{name: "mcp toolset", toolType: "mcp_toolset", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsClaudeBuiltinToolType(tt.toolType); got != tt.want {
				t.Fatalf("IsClaudeBuiltinToolType(%q) = %v, want %v", tt.toolType, got, tt.want)
			}
		})
	}
}

func TestClaudeBuiltinToolRegistry_DefaultSeedFallback(t *testing.T) {
	registry := AugmentClaudeBuiltinToolRegistry(nil, nil)
	for _, name := range defaultClaudeBuiltinToolNames {
		if !registry[name] {
			t.Fatalf("default builtin %q missing from fallback registry", name)
		}
	}
}

func TestClaudeBuiltinToolRegistry_AugmentsTypedBuiltinsFromBody(t *testing.T) {
	registry := AugmentClaudeBuiltinToolRegistry([]byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search"},
			{"type": "computer_20250124", "name": "computer"},
			{"name": "Read"}
		]
	}`), nil)

	if !registry["web_search"] {
		t.Fatal("expected default typed builtin web_search in registry")
	}
	if !registry["computer"] {
		t.Fatal("expected typed builtin from body to be added to registry")
	}
	if registry["Read"] {
		t.Fatal("expected untyped custom tool to stay out of builtin registry")
	}
}

func TestClaudeBuiltinToolRegistry_CustomTypedToolsStayOutAndKnownBuiltinsStayIn(t *testing.T) {
	registry := AugmentClaudeBuiltinToolRegistry([]byte(`{
		"tools": [
			{"type": "custom", "name": "Read"},
			{"type": "custom_builtin_20250401", "name": "special_builtin"},
			{"type": "mcp_toolset", "name": "mcp_toolset"}
		]
	}`), nil)

	if registry["Read"] {
		t.Fatal("expected type=custom tool to stay out of builtin registry")
	}
	if registry["special_builtin"] {
		t.Fatal("expected unknown typed tool to stay out of builtin registry")
	}
	if !registry["mcp_toolset"] {
		t.Fatal("expected known builtin mcp_toolset to stay in builtin registry")
	}
}
