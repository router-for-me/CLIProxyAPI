package helps

import "testing"

func TestClaudeBuiltinToolRegistry_DefaultSeedFallback(t *testing.T) {
	registry := AugmentClaudeBuiltinToolRegistry(nil, nil)
	for _, name := range defaultClaudeBuiltinToolNames {
		if !registry[name] {
			t.Fatalf("default builtin %q missing from fallback registry", name)
		}
	}
}

func TestClaudeBuiltinToolRegistry_AugmentsKnownTypedBuiltinsFromBody(t *testing.T) {
	registry := AugmentClaudeBuiltinToolRegistry([]byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search"},
			{"type": "custom", "name": "client_custom"},
			{"type": "custom_builtin_20250401", "name": "unknown_typed"},
			{"name": "Read"}
		]
	}`), nil)

	if !registry["web_search"] {
		t.Fatal("expected known typed builtin web_search in registry")
	}
	for _, name := range []string{"client_custom", "unknown_typed", "Read"} {
		if registry[name] {
			t.Fatalf("expected client tool %q to stay out of builtin registry", name)
		}
	}
}

func TestIsClaudeServerToolType(t *testing.T) {
	for _, toolType := range []string{"web_search_20250305", "code_execution_20250522", "tool_search_tool_regex_20251119"} {
		if !IsClaudeServerToolType(toolType) {
			t.Fatalf("IsClaudeServerToolType(%q) = false, want true", toolType)
		}
	}
	for _, toolType := range []string{"", "custom", "custom_builtin_20250401"} {
		if IsClaudeServerToolType(toolType) {
			t.Fatalf("IsClaudeServerToolType(%q) = true, want false", toolType)
		}
	}
}
