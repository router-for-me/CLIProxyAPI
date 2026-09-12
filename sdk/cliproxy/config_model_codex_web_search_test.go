package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigModelsPropagatesCodexWebSearch(t *testing.T) {
	trueValue := true
	falseValue := false
	tests := []struct {
		name string
		got  func() *ModelInfo
		want bool
	}{
		{
			name: "codex",
			got: func() *ModelInfo {
				return buildCodexConfigModels(&config.CodexKey{Models: []config.CodexModel{{
					Name:           "codex-upstream",
					Alias:          "codex-alias",
					CodexWebSearch: &trueValue,
				}}})[0]
			},
			want: true,
		},
		{
			name: "claude",
			got: func() *ModelInfo {
				return buildClaudeConfigModels(&config.ClaudeKey{Models: []config.ClaudeModel{{
					Name:           "claude-upstream",
					Alias:          "claude-alias",
					CodexWebSearch: &falseValue,
				}}})[0]
			},
			want: false,
		},
		{
			name: "gemini",
			got: func() *ModelInfo {
				return buildGeminiConfigModels(&config.GeminiKey{Models: []config.GeminiModel{{
					Name:           "gemini-upstream",
					Alias:          "gemini-alias",
					CodexWebSearch: &trueValue,
				}}})[0]
			},
			want: true,
		},
		{
			name: "openai compatibility",
			got: func() *ModelInfo {
				return buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{Models: []config.OpenAICompatibilityModel{{
					Name:           "compat-upstream",
					Alias:          "compat-alias",
					CodexWebSearch: &falseValue,
				}}})[0]
			},
			want: false,
		},
		{
			name: "vertex compatibility",
			got: func() *ModelInfo {
				return buildVertexCompatConfigModels(&config.VertexCompatKey{Models: []config.VertexCompatModel{{
					Name:           "vertex-upstream",
					Alias:          "vertex-alias",
					CodexWebSearch: &trueValue,
				}}})[0]
			},
			want: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			model := testCase.got()
			if model == nil || model.CodexWebSearch == nil {
				t.Fatalf("CodexWebSearch = nil, want %v", testCase.want)
			}
			if got := *model.CodexWebSearch; got != testCase.want {
				t.Fatalf("CodexWebSearch = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestApplyModelPrefixesMergesCodexWebSearchOverride(t *testing.T) {
	trueValue := true
	falseValue := false
	models := []*ModelInfo{
		{ID: "pool-alias", CodexWebSearch: &trueValue},
		{ID: "pool-alias", CodexWebSearch: &falseValue},
		{ID: "other-alias", CodexWebSearch: &trueValue},
		{ID: "other-alias"},
	}

	out := applyModelPrefixes(models, "team", false)
	if len(out) != 4 {
		t.Fatalf("expected 4 models (2 unprefixed + 2 prefixed), got %d", len(out))
	}

	entryMap := make(map[string]*ModelInfo, len(out))
	for _, m := range out {
		entryMap[m.ID] = m
	}

	// A false entry in the pool must win even when a true entry comes first,
	// for both the unprefixed and the prefixed IDs.
	for _, id := range []string{"pool-alias", "team/pool-alias"} {
		model := entryMap[id]
		if model == nil || model.CodexWebSearch == nil {
			t.Fatalf("%s CodexWebSearch = nil, want false", id)
		}
		if *model.CodexWebSearch {
			t.Fatalf("%s CodexWebSearch = true, want false", id)
		}
	}

	// A nil override defers to the explicit value.
	for _, id := range []string{"other-alias", "team/other-alias"} {
		model := entryMap[id]
		if model == nil || model.CodexWebSearch == nil {
			t.Fatalf("%s CodexWebSearch = nil, want true", id)
		}
		if !*model.CodexWebSearch {
			t.Fatalf("%s CodexWebSearch = false, want true", id)
		}
	}

	// Merging must not mutate the caller's models.
	if !*models[0].CodexWebSearch {
		t.Fatal("input model CodexWebSearch mutated")
	}
}
