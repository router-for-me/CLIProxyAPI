package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
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

func TestBuildCodexConfigModelsSelectsDefaultsOrConfiguredModels(t *testing.T) {
	configured := buildCodexConfigModels(&config.CodexKey{Models: []config.CodexModel{{
		Name: "upstream-codex", Alias: "configured-codex",
	}}})
	if len(configured) != 1 {
		t.Fatalf("configured model count = %d, want 1", len(configured))
	}
	if configured[0].ID != "configured-codex" {
		t.Fatalf("configured model ID = %q, want configured-codex", configured[0].ID)
	}

	defaults := buildCodexConfigModels(&config.CodexKey{})
	wantDefaults := registry.GetCodexProModels()
	if len(defaults) != len(wantDefaults) {
		t.Fatalf("default model count = %d, want %d", len(defaults), len(wantDefaults))
	}
	defaultIDs := make(map[string]struct{}, len(defaults))
	for _, model := range defaults {
		if model != nil {
			defaultIDs[model.ID] = struct{}{}
		}
	}
	for _, modelID := range []string{"gpt-image-1.5", "gpt-image-2"} {
		if _, ok := defaultIDs[modelID]; !ok {
			t.Errorf("missing default model %q", modelID)
		}
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
