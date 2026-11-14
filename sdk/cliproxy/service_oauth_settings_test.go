package cliproxy

import (
	"testing"

	codexmodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestApplyOAuthSettings_MaxContextLength(t *testing.T) {
	cfg := &config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"codex": {
				{Name: "gpt-6-sol", MaxContextLength: 524288},
				{Name: "deepseek-v4-flash", MaxContextLength: 1048576},
			},
		},
	}

	models := []*ModelInfo{
		{ID: "gpt-6-sol", ContextLength: 272000},
		{ID: "deepseek-v4-flash", ContextLength: 272000},
		{ID: "gpt-5-codex", ContextLength: 272000},
	}

	out := applyOAuthSettings(cfg, "codex", "oauth", models)

	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
	if out[0].MaxContextLength != 524288 || out[0].ContextLength != 524288 {
		t.Errorf("gpt-6-sol MaxContextLength = %d, ContextLength = %d; want 524288", out[0].MaxContextLength, out[0].ContextLength)
	}
	if out[1].MaxContextLength != 1048576 || out[1].ContextLength != 1048576 {
		t.Errorf("deepseek-v4-flash MaxContextLength = %d, ContextLength = %d; want 1048576", out[1].MaxContextLength, out[1].ContextLength)
	}
	if out[2].MaxContextLength != 0 || out[2].ContextLength != 272000 {
		t.Errorf("gpt-5-codex MaxContextLength = %d, ContextLength = %d; want untouched (0, 272000)", out[2].MaxContextLength, out[2].ContextLength)
	}
}

func TestApplyOAuthSettings_AliasedModel(t *testing.T) {
	cfg := &config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"codex": {
				{Name: "gpt-6-sol", MaxContextLength: 524288},
			},
		},
	}

	// Model was aliased: ID is custom-sol, MetadataModelID is gpt-6-sol
	models := []*ModelInfo{
		{ID: "custom-sol", MetadataModelID: "gpt-6-sol", ContextLength: 272000},
	}

	out := applyOAuthSettings(cfg, "codex", "oauth", models)

	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].MaxContextLength != 524288 {
		t.Errorf("aliased model MaxContextLength = %d, want 524288", out[0].MaxContextLength)
	}
}

func TestApplyOAuthSettings_MatchedByAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"codex": {
				{Name: "gpt-6-sol", Alias: "sol-preview", MaxContextLength: 524288},
			},
		},
	}

	models := []*ModelInfo{
		{ID: "sol-preview", ContextLength: 272000},
		{ID: "other-model", ContextLength: 272000},
	}

	out := applyOAuthSettings(cfg, "codex", "oauth", models)

	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].MaxContextLength != 524288 {
		t.Errorf("sol-preview MaxContextLength = %d, want 524288", out[0].MaxContextLength)
	}
	if out[1].MaxContextLength != 0 {
		t.Errorf("other-model MaxContextLength = %d, want 0", out[1].MaxContextLength)
	}
}

func TestApplyOAuthSettings_CodexCatalogPipeline(t *testing.T) {
	cfg := &config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"codex": {
				{Name: "gpt-6-sol", MaxContextLength: 524288},
			},
		},
	}

	models := []*ModelInfo{
		{ID: "gpt-6-sol", ContextLength: 272000},
	}

	out := applyOAuthSettings(cfg, "codex", "oauth", models)
	if len(out) != 1 || out[0].MaxContextLength != 524288 {
		t.Fatalf("unexpected model output: %+v", out)
	}

	availableModels := []map[string]any{
		{
			"id":                 out[0].ID,
			"object":             "model",
			"context_length":     out[0].ContextLength,
			"max_context_length": out[0].MaxContextLength,
		},
	}

	resp := codexmodels.BuildResponse(availableModels, nil, false)
	respModels, ok := resp["models"].([]map[string]any)
	if !ok || len(respModels) == 0 {
		t.Fatalf("unexpected catalog models response: %#v", resp)
	}

	var foundSol map[string]any
	for _, m := range respModels {
		if id, _ := m["slug"].(string); id == "gpt-6-sol" {
			foundSol = m
			break
		}
	}
	if foundSol == nil {
		t.Fatalf("gpt-6-sol not found in catalog response: %#v", respModels)
	}
	if contextWindow, _ := foundSol["context_window"].(int); contextWindow != 524288 {
		t.Errorf("catalog context_window = %v, want 524288", foundSol["context_window"])
	}
	if maxContextWindow, _ := foundSol["max_context_window"].(int); maxContextWindow != 524288 {
		t.Errorf("catalog max_context_window = %v, want 524288", foundSol["max_context_window"])
	}
}

func TestApplyOAuthSettings_SkipsAPIKey(t *testing.T) {
	cfg := &config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"codex": {
				{Name: "gpt-6-sol", MaxContextLength: 524288},
			},
		},
	}

	// An API-key credential with its own configured max-context-length
	models := []*ModelInfo{
		{ID: "gpt-6-sol", ContextLength: 1048576, MaxContextLength: 1048576},
	}

	out := applyOAuthSettingsForAuth(cfg, "codex", "api_key", models)

	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].MaxContextLength != 1048576 || out[0].ContextLength != 1048576 {
		t.Errorf("API-key model was modified by OAuthSettings: MaxContextLength = %d, ContextLength = %d; want 1048576", out[0].MaxContextLength, out[0].ContextLength)
	}
}
