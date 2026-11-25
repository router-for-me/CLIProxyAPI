package thinking_test

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/interactions"
)

func TestGeminiApplyDoc_SnakeCaseContainersAndIncludeThoughts(t *testing.T) {
	applier := gemini.NewApplier()

	levelModel := &registry.ModelInfo{
		ID: "gemini-2.5-pro",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}
	budgetModel := &registry.ModelInfo{
		ID: "gemini-2.5-flash",
		Thinking: &registry.ThinkingSupport{
			Min: 0,
			Max: 24576,
		},
	}

	t.Run("level mode deletes snake_case containers and preserves include_thoughts", func(t *testing.T) {
		body := `{
			"generationConfig": {
				"thinking_config": {
					"thinking_budget": 1024,
					"include_thoughts": true
				}
			},
			"generation_config": {
				"thinking_config": {
					"thinking_level": "low"
				},
				"thinkingConfig": {
					"thinkingBudget": 512
				}
			}
		}`
		doc := ojson.NewDocString(body)
		if err := applier.ApplyDoc(doc, thinking.ThinkingConfig{
			Mode:  thinking.ModeLevel,
			Level: thinking.LevelHigh,
		}, levelModel); err != nil {
			t.Fatalf("ApplyDoc failed: %v", err)
		}

		if doc.Get("generationConfig.thinking_config").Exists() {
			t.Fatalf("expected generationConfig.thinking_config to be deleted, got: %s", doc.String())
		}
		if doc.Get("generation_config.thinking_config").Exists() {
			t.Fatalf("expected generation_config.thinking_config to be deleted, got: %s", doc.String())
		}
		if doc.Get("generation_config.thinkingConfig").Exists() {
			t.Fatalf("expected generation_config.thinkingConfig to be deleted, got: %s", doc.String())
		}
		if got := doc.Get("generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
			t.Fatalf("expected thinkingLevel=high, got %q (%s)", got, doc.String())
		}
		if got := doc.Get("generationConfig.thinkingConfig.includeThoughts"); !got.Exists() || got.Type != ojson.True {
			t.Fatalf("expected includeThoughts=true to be preserved from thinking_config, got: %s", doc.String())
		}
	})

	t.Run("budget mode deletes snake_case containers and preserves include_thoughts from generation_config", func(t *testing.T) {
		body := `{
			"generation_config": {
				"thinking_config": {
					"thinking_level": "high",
					"include_thoughts": true
				}
			}
		}`
		doc := ojson.NewDocString(body)
		if err := applier.ApplyDoc(doc, thinking.ThinkingConfig{
			Mode:   thinking.ModeBudget,
			Budget: 2048,
		}, budgetModel); err != nil {
			t.Fatalf("ApplyDoc failed: %v", err)
		}

		if doc.Get("generation_config.thinking_config").Exists() {
			t.Fatalf("expected generation_config.thinking_config to be deleted, got: %s", doc.String())
		}
		if got := doc.Get("generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
			t.Fatalf("expected thinkingBudget=2048, got %d (%s)", got, doc.String())
		}
		if got := doc.Get("generationConfig.thinkingConfig.includeThoughts"); !got.Exists() || got.Type != ojson.True {
			t.Fatalf("expected includeThoughts=true to be preserved from generation_config.thinking_config, got: %s", doc.String())
		}
	})
}

func TestStripThinkingConfig_SnakeCaseAndCamelCaseContainers(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		body     string
		removed  []string
		kept     []string
	}{
		{
			name:     "gemini strips all snake_case and camelCase thinking containers",
			provider: "gemini",
			body: `{
				"generationConfig": {
					"temperature": 0.5,
					"thinkingConfig": {"thinkingBudget": 1024},
					"thinking_config": {"thinking_budget": 1024}
				},
				"generation_config": {
					"top_p": 0.9,
					"thinkingConfig": {"thinkingBudget": 512},
					"thinking_config": {"thinking_budget": 512}
				}
			}`,
			removed: []string{
				"generationConfig.thinkingConfig",
				"generationConfig.thinking_config",
				"generation_config.thinkingConfig",
				"generation_config.thinking_config",
			},
			kept: []string{
				"generationConfig.temperature",
				"generation_config.top_p",
			},
		},
		{
			name:     "antigravity strips all snake_case and camelCase thinking containers",
			provider: "antigravity",
			body: `{
				"request": {
					"generationConfig": {
						"temperature": 0.5,
						"thinkingConfig": {"thinkingBudget": 1024},
						"thinking_config": {"thinking_budget": 1024}
					},
					"generation_config": {
						"top_p": 0.9,
						"thinkingConfig": {"thinkingBudget": 512},
						"thinking_config": {"thinking_budget": 512}
					}
				}
			}`,
			removed: []string{
				"request.generationConfig.thinkingConfig",
				"request.generationConfig.thinking_config",
				"request.generation_config.thinkingConfig",
				"request.generation_config.thinking_config",
			},
			kept: []string{
				"request.generationConfig.temperature",
				"request.generation_config.top_p",
			},
		},
		{
			name:     "interactions strips both generation_config and generationConfig thinking fields",
			provider: "interactions",
			body: `{
				"generation_config": {
					"temperature": 0.5,
					"thinking_level": "high",
					"thinking_config": {"thinking_budget": 1024}
				},
				"generationConfig": {
					"topP": 0.9,
					"thinkingLevel": "medium",
					"thinkingBudget": 2048,
					"thinkingSummaries": "auto",
					"thinkingConfig": {"thinkingBudget": 1024},
					"thinking_config": {"thinking_budget": 512}
				}
			}`,
			removed: []string{
				"generation_config.thinking_level",
				"generation_config.thinking_config",
				"generationConfig.thinkingLevel",
				"generationConfig.thinkingBudget",
				"generationConfig.thinkingSummaries",
				"generationConfig.thinkingConfig",
				"generationConfig.thinking_config",
			},
			kept: []string{
				"generation_config.temperature",
				"generationConfig.topP",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := thinking.StripThinkingConfig([]byte(tc.body), tc.provider)
			doc := ojson.NewDoc(out)
			for _, path := range tc.removed {
				if doc.Get(path).Exists() {
					t.Errorf("expected path %q to be stripped, got: %s", path, doc.String())
				}
			}
			for _, path := range tc.kept {
				if !doc.Get(path).Exists() {
					t.Errorf("expected path %q to be preserved, got: %s", path, doc.String())
				}
			}
		})
	}
}

func TestApplyThinking_ExtractGeminiSnakeCaseConfig(t *testing.T) {
	t.Run("gemini extracts thinking_budget from generationConfig.thinking_config", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"thinking_config":{"thinking_budget":4096,"include_thoughts":true}}}`)
		out, err := thinking.ApplyThinking(body, "gemini-2.5-flash", "gemini", "gemini", "gemini")
		if err != nil {
			t.Fatalf("ApplyThinking failed: %v", err)
		}
		doc := ojson.NewDoc(out)
		if doc.Get("generationConfig.thinking_config").Exists() {
			t.Fatalf("expected generationConfig.thinking_config to be removed, got: %s", doc.String())
		}
		if got := doc.Get("generationConfig.thinkingConfig.thinkingBudget").Int(); got != 4096 {
			t.Fatalf("expected thinkingBudget=4096, got %d (%s)", got, doc.String())
		}
		if got := doc.Get("generationConfig.thinkingConfig.includeThoughts"); !got.Exists() || got.Type != ojson.True {
			t.Fatalf("expected includeThoughts=true, got: %s", doc.String())
		}
	})

	t.Run("gemini extracts thinking_level from generation_config.thinking_config", func(t *testing.T) {
		body := []byte(`{"generation_config":{"thinking_config":{"thinking_level":"high"}}}`)
		out, err := thinking.ApplyThinking(body, "gemini-3-pro-preview", "gemini", "gemini", "gemini")
		if err != nil {
			t.Fatalf("ApplyThinking failed: %v", err)
		}
		doc := ojson.NewDoc(out)
		if doc.Get("generation_config.thinking_config").Exists() {
			t.Fatalf("expected generation_config.thinking_config to be removed, got: %s", doc.String())
		}
		if got := doc.Get("generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
			t.Fatalf("expected thinkingLevel=high, got %q (%s)", got, doc.String())
		}
	})
}

func TestApplyThinking_InteractionsCamelCaseGenerationConfig(t *testing.T) {
	noThinkingModel := &registry.ModelInfo{
		ID:       "gemini-2.0-flash",
		Thinking: nil,
	}
	thinkingModel := &registry.ModelInfo{
		ID: "gemini-3-pro-preview",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}

	t.Run("strips generationConfig.thinkingLevel when model does not support thinking", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"temperature":0.5,"thinkingLevel":"high"}}`)
		out, err := thinking.ApplyThinkingWithModelInfo(body, body, noThinkingModel.ID, "interactions", "interactions", "interactions", noThinkingModel)
		if err != nil {
			t.Fatalf("ApplyThinkingWithModelInfo failed: %v", err)
		}
		doc := ojson.NewDoc(out)
		if doc.Get("generationConfig.thinkingLevel").Exists() {
			t.Fatalf("expected generationConfig.thinkingLevel to be stripped, got: %s", doc.String())
		}
		if !doc.Get("generationConfig.temperature").Exists() {
			t.Fatalf("expected generationConfig.temperature to be preserved, got: %s", doc.String())
		}
	})

	t.Run("strips generationConfig.thinkingSummaries when model does not support thinking", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"temperature":0.5,"thinkingSummaries":"auto"}}`)
		out, err := thinking.ApplyThinkingWithModelInfo(body, body, noThinkingModel.ID, "interactions", "interactions", "interactions", noThinkingModel)
		if err != nil {
			t.Fatalf("ApplyThinkingWithModelInfo failed: %v", err)
		}
		doc := ojson.NewDoc(out)
		if doc.Get("generationConfig.thinkingSummaries").Exists() {
			t.Fatalf("expected generationConfig.thinkingSummaries to be stripped, got: %s", doc.String())
		}
		if !doc.Get("generationConfig.temperature").Exists() {
			t.Fatalf("expected generationConfig.temperature to be preserved, got: %s", doc.String())
		}
	})

	t.Run("normalizes generationConfig camelCase thinking fields when model supports thinking", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"thinkingLevel":"high","thinkingSummaries":"auto"}}`)
		out, err := thinking.ApplyThinkingWithModelInfo(body, body, thinkingModel.ID, "interactions", "interactions", "interactions", thinkingModel)
		if err != nil {
			t.Fatalf("ApplyThinkingWithModelInfo failed: %v", err)
		}
		doc := ojson.NewDoc(out)
		if doc.Get("generationConfig.thinkingLevel").Exists() {
			t.Fatalf("expected generationConfig.thinkingLevel to be removed, got: %s", doc.String())
		}
		if doc.Get("generationConfig.thinkingSummaries").Exists() {
			t.Fatalf("expected generationConfig.thinkingSummaries to be removed, got: %s", doc.String())
		}
		if got := doc.Get("generation_config.thinking_level").String(); got != "high" {
			t.Fatalf("expected generation_config.thinking_level=high, got %q (%s)", got, doc.String())
		}
		if got := doc.Get("generation_config.thinking_summaries").String(); got != "auto" {
			t.Fatalf("expected generation_config.thinking_summaries=auto, got %q (%s)", got, doc.String())
		}
	})
}
