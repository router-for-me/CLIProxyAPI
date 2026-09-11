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
