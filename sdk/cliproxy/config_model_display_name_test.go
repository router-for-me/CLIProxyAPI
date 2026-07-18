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

func TestBuildCodexConfigModelsPreservesBuiltinDisplayNames(t *testing.T) {
	models := buildCodexConfigModels(&config.CodexKey{Models: []config.CodexModel{
		{Name: "gpt-image-1.5", DisplayName: "Configured Image 1.5"},
		{Name: "gpt-image-2", DisplayName: "Configured Image 2"},
	}})

	wantDisplayNames := map[string]string{
		"gpt-image-1.5": "Configured Image 1.5",
		"gpt-image-2":   "Configured Image 2",
	}
	for _, model := range models {
		wantDisplayName, ok := wantDisplayNames[model.ID]
		if !ok {
			continue
		}
		if model.DisplayName != wantDisplayName {
			t.Errorf("%s DisplayName = %q, want %q", model.ID, model.DisplayName, wantDisplayName)
		}
		if model.Object != "model" || model.OwnedBy != "openai" || model.Type != "openai" || model.Created != 1704067200 || model.Version != model.ID || model.UserDefined {
			t.Errorf("%s builtin metadata was not preserved: %#v", model.ID, model)
		}
		delete(wantDisplayNames, model.ID)
	}
	for modelID := range wantDisplayNames {
		t.Errorf("missing builtin model %s", modelID)
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
