package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigModelsPropagatesMaxContextLength(t *testing.T) {
	const want = 1048576

	tests := []struct {
		name string
		got  func() *ModelInfo
	}{
		{
			name: "codex",
			got: func() *ModelInfo {
				return buildCodexConfigModels(&config.CodexKey{
					Models: []config.CodexModel{{
						Name: "codex-upstream", Alias: "codex-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "claude",
			got: func() *ModelInfo {
				return buildClaudeConfigModels(&config.ClaudeKey{
					Models: []config.ClaudeModel{{
						Name: "claude-upstream", Alias: "claude-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "gemini",
			got: func() *ModelInfo {
				return buildGeminiConfigModels(&config.GeminiKey{
					Models: []config.GeminiModel{{
						Name: "gemini-upstream", Alias: "gemini-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "interactions",
			got: func() *ModelInfo {
				return buildGeminiConfigModels(&config.GeminiKey{
					Models: []config.GeminiModel{{
						Name: "interactions-upstream", Alias: "interactions-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "xai",
			got: func() *ModelInfo {
				return buildXAIConfigModels(&config.XAIKey{
					Models: []config.XAIModel{{
						Name: "xai-upstream", Alias: "xai-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "openai compatibility",
			got: func() *ModelInfo {
				return buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
					Models: []config.OpenAICompatibilityModel{{
						Name: "compat-upstream", Alias: "compat-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			model := testCase.got()
			if model == nil {
				t.Fatal("model = nil")
			}
			if model.ContextLength != want {
				t.Errorf("context length = %d, want %d", model.ContextLength, want)
			}
			if model.MaxContextLength != want {
				t.Errorf("max context length = %d, want %d", model.MaxContextLength, want)
			}
		})
	}
}
