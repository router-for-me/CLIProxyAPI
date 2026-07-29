package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigModelsDisplayName(t *testing.T) {
	tests := []struct {
		name string
		want string
		got  func() *ModelInfo
	}{
		{
			name: "claude",
			want: "Claude Catalog Name",
			got: func() *ModelInfo {
				return buildClaudeConfigModels(&config.ClaudeKey{Models: []config.ClaudeModel{{
					Name: "claude-upstream", Alias: "claude-catalog", DisplayName: "Claude Catalog Name",
				}}})[0]
			},
		},
		{
			name: "gemini",
			want: "Gemini Catalog Name",
			got: func() *ModelInfo {
				return buildGeminiConfigModels(&config.GeminiKey{Models: []config.GeminiModel{{
					Name: "gemini-upstream", Alias: "gemini-catalog", DisplayName: "Gemini Catalog Name",
				}}})[0]
			},
		},
		{
			name: "vertex",
			want: "Vertex Catalog Name",
			got: func() *ModelInfo {
				return buildVertexCompatConfigModels(&config.VertexCompatKey{Models: []config.VertexCompatModel{{
					Name: "vertex-upstream", Alias: "vertex-catalog", DisplayName: "Vertex Catalog Name",
				}}})[0]
			},
		},
		{
			name: "codex",
			want: "Codex Catalog Name",
			got: func() *ModelInfo {
				return buildCodexConfigModels(&config.CodexKey{Models: []config.CodexModel{{
					Name: "gpt-5.5", Alias: "gpt-5.5", DisplayName: "Codex Catalog Name",
				}}})[0]
			},
		},
		{
			name: "xai",
			want: "xAI Catalog Name",
			got: func() *ModelInfo {
				return buildXAIConfigModels(&config.XAIKey{Models: []config.XAIModel{{
					Name: "grok-4.5", Alias: "grok-latest", DisplayName: "xAI Catalog Name",
				}}})[0]
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got().DisplayName; got != tt.want {
				t.Fatalf("DisplayName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildCodexConfigModelsOnlyIncludesConfiguredModels(t *testing.T) {
	models := buildCodexConfigModels(&config.CodexKey{Models: []config.CodexModel{{
		Name: "upstream-codex", Alias: "configured-codex",
	}}})

	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	if models[0].ID != "configured-codex" {
		t.Fatalf("model ID = %q, want configured-codex", models[0].ID)
	}

	if models := buildCodexConfigModels(&config.CodexKey{}); len(models) != 0 {
		t.Fatalf("model count without configuration = %d, want 0", len(models))
	}
}

func TestBuildConfigModelsDisplayNameFallback(t *testing.T) {
	model := buildClaudeConfigModels(&config.ClaudeKey{Models: []config.ClaudeModel{{
		Name: "claude-upstream", Alias: "claude-catalog",
	}}})[0]
	if model.DisplayName != "claude-upstream" {
		t.Fatalf("DisplayName = %q, want upstream model name", model.DisplayName)
	}
}
