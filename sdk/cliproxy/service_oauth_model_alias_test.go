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

func TestApplyOAuthModelAlias_CodexImageAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "codex/gpt-image", Fork: true},
			},
		},
	}
	models := []*ModelInfo{{ID: "gpt-5", Name: "models/gpt-5"}}
	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected original plus image alias, got %d", len(out))
	}
	if out[1].ID != "codex/gpt-image" || out[1].Name != "models/codex/gpt-image" {
		t.Fatalf("unexpected alias model: id=%q name=%q", out[1].ID, out[1].Name)
	}
}

func TestApplyOAuthModelAlias_CodexImage2Alias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5.4", Alias: "gpt-image-2", Fork: true},
			},
		},
	}
	models := []*ModelInfo{{ID: "gpt-5.4", Name: "models/gpt-5.4"}}
	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if !hasModelInfoIDName(out, "gpt-image-2", "models/gpt-image-2") {
		t.Fatalf("expected image alias in model list, got %#v", modelInfoIDs(out))
	}
}

func TestApplyOAuthModelAlias_ForkAddsSuffixedReasoningAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5.5(high)", Alias: "gpt-5.5-high", Fork: true},
			},
		},
	}
	models := []*ModelInfo{{ID: "gpt-5.5", Name: "models/gpt-5.5"}}
	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if !hasModelInfoIDName(out, "gpt-5.5", "models/gpt-5.5") {
		t.Fatalf("expected original model in list, got %#v", modelInfoIDs(out))
	}
	if !hasModelInfoIDName(out, "gpt-5.5-high", "models/gpt-5.5-high") {
		t.Fatalf("expected reasoning alias in model list, got %#v", modelInfoIDs(out))
	}
}

func hasModelInfoIDName(models []*ModelInfo, id, name string) bool {
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == id && model.Name == name {
			return true
		}
	}
	return false
}

func modelInfoIDs(models []*ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		out = append(out, model.ID)
	}
	return out
}

func TestApplyOAuthModelAlias_UserReasoningAliasOverridesDefaultInListings(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5.4(low)", Alias: "gpt-5.5-low", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5.5", Name: "models/gpt-5.5", DisplayName: "GPT 5.5"},
		{ID: "gpt-5.4", Name: "models/gpt-5.4", DisplayName: "GPT 5.4"},
	}
	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)

	aliasModels := matchingModelInfos(out, "gpt-5.5-low")
	if len(aliasModels) != 1 {
		t.Fatalf("expected exactly one gpt-5.5-low alias, got %d in %#v", len(aliasModels), modelInfoIDs(out))
	}
	if aliasModels[0].DisplayName != "GPT 5.4" {
		t.Fatalf("expected user override alias to be cloned from GPT 5.4, got display name %q", aliasModels[0].DisplayName)
	}
}

func matchingModelInfos(models []*ModelInfo, id string) []*ModelInfo {
	out := make([]*ModelInfo, 0)
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == id {
			out = append(out, model)
		}
	}
	return out
}
