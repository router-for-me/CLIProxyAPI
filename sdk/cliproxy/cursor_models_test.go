package cliproxy

import (
	"testing"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestBuildCursorCachedModelsThinkingVariants(t *testing.T) {
	auth := &coreauth.Auth{Metadata: map[string]any{cursorauth.ModelCacheKey: []any{
		map[string]any{"id": "gpt-test", "display_name": "GPT Test"},
		map[string]any{"id": "gpt-test-low", "thinking": true},
		map[string]any{"id": "gpt-test-high", "thinking": true},
	}}}
	models := buildCursorCachedModels(auth)
	if len(models) != 3 {
		t.Fatalf("models = %d, want 3", len(models))
	}
	if models[0].ID != "gpt-test" || models[0].Thinking == nil {
		t.Fatalf("unexpected base model: %#v", models[0])
	}
	want := []string{"none", "low", "high"}
	if len(models[0].Thinking.Levels) != len(want) {
		t.Fatalf("levels = %v", models[0].Thinking.Levels)
	}
	for i := range want {
		if models[0].Thinking.Levels[i] != want[i] {
			t.Fatalf("levels = %v", models[0].Thinking.Levels)
		}
	}
}
