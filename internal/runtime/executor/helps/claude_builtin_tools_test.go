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

func TestClaudeBuiltinToolRegistry_AugmentsTypedBuiltinsFromBody(t *testing.T) {
	registry := AugmentClaudeBuiltinToolRegistry([]byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search"},
			{"type": "custom_builtin_20250401", "name": "special_builtin"},
			{"name": "Read"}
		]
	}`), nil)

	if !registry["web_search"] {
		t.Fatal("expected default typed builtin web_search in registry")
	}
	if !registry["special_builtin"] {
		t.Fatal("expected typed builtin from body to be added to registry")
	}
	if registry["Read"] {
		t.Fatal("expected untyped custom tool to stay out of builtin registry")
	}
}
