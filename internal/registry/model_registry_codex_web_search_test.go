package registry

import "testing"

func TestCodexWebSearchOverrideFalseWinsAcrossProviders(t *testing.T) {
	trueValue := true
	falseValue := false

	r := newTestModelRegistry()
	r.RegisterClient("codex-client", "codex", []*ModelInfo{{
		ID:             "shared-model",
		CodexWebSearch: &trueValue,
	}})
	r.RegisterClient("compat-client", "openai-compatible-test", []*ModelInfo{{
		ID:             "shared-model",
		CodexWebSearch: &falseValue,
	}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if got, ok := models[0]["codex_web_search"].(bool); !ok || got {
		t.Fatalf("codex_web_search = %#v, want false", models[0]["codex_web_search"])
	}
}

func TestCodexWebSearchOverrideFalseWinsWithinRepeatedAliasPool(t *testing.T) {
	trueValue := true
	falseValue := false
	tests := []struct {
		name    string
		entries []*ModelInfo
		want    bool
	}{
		{
			name: "true then false",
			entries: []*ModelInfo{
				{ID: "pooled-model", CodexWebSearch: &trueValue},
				{ID: "pooled-model", CodexWebSearch: &falseValue},
			},
			want: false,
		},
		{
			name: "false then true",
			entries: []*ModelInfo{
				{ID: "pooled-model", CodexWebSearch: &falseValue},
				{ID: "pooled-model", CodexWebSearch: &trueValue},
			},
			want: false,
		},
		{
			name: "true then unset",
			entries: []*ModelInfo{
				{ID: "pooled-model", CodexWebSearch: &trueValue},
				{ID: "pooled-model"},
			},
			want: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			r := newTestModelRegistry()
			r.RegisterClient("pooled-client", "openai-compatible-test", testCase.entries)

			models := r.GetAvailableModels("openai")
			if len(models) != 1 {
				t.Fatalf("models len = %d, want 1", len(models))
			}
			if got, ok := models[0]["codex_web_search"].(bool); !ok || got != testCase.want {
				t.Fatalf("codex_web_search = %#v, want %v", models[0]["codex_web_search"], testCase.want)
			}
		})
	}
}

func TestCodexWebSearchOverrideUnaffectedBySuspension(t *testing.T) {
	trueValue := true
	falseValue := false
	for _, reason := range []string{"manual", "quota"} {
		t.Run(reason, func(t *testing.T) {
			r := newTestModelRegistry()
			r.RegisterClient("true-client", "codex", []*ModelInfo{{
				ID:             "shared-model",
				CodexWebSearch: &trueValue,
			}})
			r.RegisterClient("false-client", "openai-compatible-test", []*ModelInfo{{
				ID:             "shared-model",
				CodexWebSearch: &falseValue,
			}})
			r.SuspendClientModel("false-client", "shared-model", reason)

			models := r.GetAvailableModels("openai")
			if len(models) != 1 {
				t.Fatalf("models len = %d, want 1", len(models))
			}
			if got, ok := models[0]["codex_web_search"].(bool); !ok || got {
				t.Fatalf("codex_web_search = %#v, want false", models[0]["codex_web_search"])
			}
		})
	}
}

func TestCodexWebSearchOverrideTrueRequiresExplicitValue(t *testing.T) {
	trueValue := true

	t.Run("explicit true", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("codex-client", "codex", []*ModelInfo{{
			ID:             "search-model",
			CodexWebSearch: &trueValue,
		}})

		models := r.GetAvailableModels("openai")
		if len(models) != 1 {
			t.Fatalf("models len = %d, want 1", len(models))
		}
		if got, ok := models[0]["codex_web_search"].(bool); !ok || !got {
			t.Fatalf("codex_web_search = %#v, want true", models[0]["codex_web_search"])
		}
	})

	t.Run("absent", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("codex-client", "codex", []*ModelInfo{{ID: "search-model"}})

		models := r.GetAvailableModels("openai")
		if len(models) != 1 {
			t.Fatalf("models len = %d, want 1", len(models))
		}
		if _, exists := models[0]["codex_web_search"]; exists {
			t.Fatalf("codex_web_search = %#v, want absent", models[0]["codex_web_search"])
		}
	})
}
