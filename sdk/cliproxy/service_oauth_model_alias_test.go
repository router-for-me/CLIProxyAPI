package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestApplyOAuthModelAlias_Rename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "g5" {
		t.Fatalf("expected model id %q, got %q", "g5", out[0].ID)
	}
	if out[0].Name != "models/g5" {
		t.Fatalf("expected model name %q, got %q", "models/g5", out[0].Name)
	}
}

func TestApplyOAuthModelAlias_ForkAddsAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
}

func TestApplyOAuthModelAlias_ForkAddsMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
				{Name: "gpt-5", Alias: "g5-2", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[2].ID != "g5-2" {
		t.Fatalf("expected third model id %q, got %q", "g5-2", out[2].ID)
	}
	if out[2].Name != "models/g5-2" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5-2", out[2].Name)
	}
}

func TestApplyOAuthModelAlias_DefaultGitHubCopilotAliasViaSanitize(t *testing.T) {
	cfg := &config.Config{}
	cfg.SanitizeOAuthModelAlias()

	models := []*ModelInfo{
		{ID: "claude-opus-4.6", Name: "models/claude-opus-4.6"},
	}

	out := applyOAuthModelAlias(cfg, "github-copilot", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models (original + default alias), got %d", len(out))
	}
	if out[0].ID != "claude-opus-4.6" {
		t.Fatalf("expected first model id %q, got %q", "claude-opus-4.6", out[0].ID)
	}
	if out[1].ID != "claude-opus-4-6" {
		t.Fatalf("expected second model id %q, got %q", "claude-opus-4-6", out[1].ID)
	}
	if out[1].Name != "models/claude-opus-4-6" {
		t.Fatalf("expected aliased model name %q, got %q", "models/claude-opus-4-6", out[1].Name)
	}
}

func TestMergeKiroDynamicWithStaticModelsAddsStaticOpus48(t *testing.T) {
	models := mergeKiroDynamicWithStaticModels([]*ModelInfo{
		{
			ID:                  "kiro-claude-sonnet-4-5",
			Object:              "model",
			Created:             1,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Dynamic Sonnet",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
	})

	if findCliproxyModelInfo(models, "kiro-claude-opus-4-8") == nil {
		t.Fatal("expected static Kiro Opus 4.8 to be included when dynamic API list omits it")
	}
	if findCliproxyModelInfo(models, "kiro-claude-opus-4-8-agentic") == nil {
		t.Fatal("expected static Kiro Opus 4.8 agentic variant to be included when dynamic API list omits it")
	}
	if findCliproxyModelInfo(models, "kiro-claude-opus-4-6") != nil {
		t.Fatal("did not expect unrelated static Kiro models to be included when dynamic API list omits them")
	}
	if findCliproxyModelInfo(models, "kiro-gpt-4o") != nil {
		t.Fatal("did not expect unrelated static third-party Kiro models to be included when dynamic API list omits them")
	}
}

func findCliproxyModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}
