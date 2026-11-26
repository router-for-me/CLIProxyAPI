package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestCapAntigravityMaxOutputTokensDoc_ClaudeMinBudget(t *testing.T) {
	t.Run("strips_thinkingConfig_when_clamped_below_1024", func(t *testing.T) {
		tests := []struct {
			name    string
			payload string
			limit   int
			wantMax int64
		}{
			{
				name:    "camelCase_maxOutputTokens_1024",
				payload: `{"request":{"generationConfig":{"maxOutputTokens":1024,"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":true}}}}`,
				limit:   64000,
				wantMax: 1024,
			},
			{
				name:    "snake_case_max_output_tokens_512",
				payload: `{"request":{"generationConfig":{"max_output_tokens":512,"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":true}}}}`,
				limit:   64000,
				wantMax: 512,
			},
			{
				name:    "limit_1_with_snake_case_thinking_budget",
				payload: `{"request":{"generationConfig":{"thinkingConfig":{"thinking_budget":2048,"include_thoughts":true}}}}`,
				limit:   1,
				wantMax: 1,
			},
			{
				name:    "limit_1024_with_snake_case_thinking_config",
				payload: `{"request":{"generationConfig":{"thinking_config":{"thinking_budget":2048,"include_thoughts":true}}}}`,
				limit:   1024,
				wantMax: 1024,
			},
			{
				name:    "existing_budget_already_below_1024",
				payload: `{"request":{"generationConfig":{"maxOutputTokens":1024,"thinkingConfig":{"thinkingBudget":512,"includeThoughts":true}}}}`,
				limit:   64000,
				wantMax: 1024,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				doc := ojson.NewDocString(tc.payload)
				CapAntigravityMaxOutputTokensDoc(doc, tc.limit, true)

				if got := doc.Get("request.generationConfig.maxOutputTokens").Int(); got != tc.wantMax {
					t.Fatalf("maxOutputTokens = %d, want %d; body=%s", got, tc.wantMax, doc.String())
				}
				if doc.Get("request.generationConfig.max_output_tokens").Exists() {
					t.Fatalf("max_output_tokens should be deleted; body=%s", doc.String())
				}
				if tcNode := doc.Get("request.generationConfig.thinkingConfig"); tcNode.Exists() {
					t.Fatalf("expected thinkingConfig to be deleted when Claude budget < 1024, got %s; body=%s", tcNode.Raw, doc.String())
				}
				if tcSnake := doc.Get("request.generationConfig.thinking_config"); tcSnake.Exists() {
					t.Fatalf("expected thinking_config to be deleted when Claude budget < 1024, got %s; body=%s", tcSnake.Raw, doc.String())
				}
			})
		}
	})

	t.Run("boundary_1025_clamps_to_1024", func(t *testing.T) {
		doc := ojson.NewDocString(`{"request":{"generationConfig":{"maxOutputTokens":1025,"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":true}}}}`)
		CapAntigravityMaxOutputTokensDoc(doc, 64000, true)

		if got := doc.Get("request.generationConfig.maxOutputTokens").Int(); got != 1025 {
			t.Fatalf("maxOutputTokens = %d, want 1025; body=%s", got, doc.String())
		}
		if got := doc.Get("request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 1024 {
			t.Fatalf("thinkingBudget = %d, want 1024; body=%s", got, doc.String())
		}
		if include := doc.Get("request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
			t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, doc.String())
		}
	})

	t.Run("preserves_non_numeric_dynamic_level_and_non_claude", func(t *testing.T) {
		t.Run("non_numeric_budget_untouched", func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"generationConfig":{"maxOutputTokens":512,"thinkingConfig":{"thinkingBudget":"2048","includeThoughts":true}}}}`)
			CapAntigravityMaxOutputTokensDoc(doc, 64000, true)

			got := doc.Get("request.generationConfig.thinkingConfig.thinkingBudget")
			if !got.Exists() || got.Type != ojson.String || got.String() != "2048" {
				t.Fatalf("expected string thinkingBudget to remain untouched, got type=%v raw=%s; body=%s", got.Type, got.Raw, doc.String())
			}
		})

		t.Run("dynamic_budget_negative_one_preserved", func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"generationConfig":{"maxOutputTokens":512,"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}}`)
			CapAntigravityMaxOutputTokensDoc(doc, 64000, true)

			got := doc.Get("request.generationConfig.thinkingConfig.thinkingBudget")
			if !got.Exists() || got.Type != ojson.Number || got.Int() != -1 {
				t.Fatalf("expected dynamic thinkingBudget -1 to remain preserved, got %s; body=%s", got.Raw, doc.String())
			}
		})

		t.Run("thinking_level_without_budget_preserved", func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"generationConfig":{"maxOutputTokens":512,"thinkingConfig":{"thinkingLevel":"LOW","includeThoughts":true}}}}`)
			CapAntigravityMaxOutputTokensDoc(doc, 64000, true)

			if got := doc.Get("request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "LOW" {
				t.Fatalf("thinkingLevel = %q, want LOW; body=%s", got, doc.String())
			}
		})

		t.Run("non_claude_model_untouched", func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"generationConfig":{"maxOutputTokens":512,"thinkingConfig":{"thinkingBudget":2048,"includeThoughts":true}}}}`)
			CapAntigravityMaxOutputTokensDoc(doc, 64000, false)

			if got := doc.Get("request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
				t.Fatalf("thinkingBudget = %d, want 2048; body=%s", got, doc.String())
			}
		})
	})

	t.Run("canonicalizes_snake_case_generation_config_without_duplication", func(t *testing.T) {
		t.Run("claude_sub_1024_strips_thinking_and_deduplicates", func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"generation_config":{"max_output_tokens":512,"temperature":0.2,"thinking_config":{"thinking_budget":2048,"include_thoughts":true}}}}`)
			CapAntigravityMaxOutputTokensDoc(doc, 64000, true)

			if doc.Get("request.generation_config").Exists() {
				t.Fatalf("expected request.generation_config to be canonicalized away; body=%s", doc.String())
			}
			if doc.Get("request.generationConfig.max_output_tokens").Exists() {
				t.Fatalf("expected max_output_tokens to be deleted; body=%s", doc.String())
			}
			if got := doc.Get("request.generationConfig.maxOutputTokens").Int(); got != 512 {
				t.Fatalf("maxOutputTokens = %d, want 512; body=%s", got, doc.String())
			}
			if got := doc.Get("request.generationConfig.temperature").Float(); got != 0.2 {
				t.Fatalf("temperature = %v, want 0.2; body=%s", got, doc.String())
			}
			if doc.Get("request.generationConfig.thinkingConfig").Exists() || doc.Get("request.generationConfig.thinking_config").Exists() {
				t.Fatalf("expected thinking config stripped when budget < 1024; body=%s", doc.String())
			}
		})

		t.Run("merges_both_containers_without_duplication", func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"generationConfig":{"temperature":0.4},"generation_config":{"max_output_tokens":2048,"top_p":0.9,"thinking_config":{"thinking_budget":4096}}}}`)
			CapAntigravityMaxOutputTokensDoc(doc, 64000, true)

			if doc.Get("request.generation_config").Exists() {
				t.Fatalf("expected request.generation_config to be merged and deleted; body=%s", doc.String())
			}
			if got := doc.Get("request.generationConfig.maxOutputTokens").Int(); got != 2048 {
				t.Fatalf("maxOutputTokens = %d, want 2048; body=%s", got, doc.String())
			}
			if got := doc.Get("request.generationConfig.temperature").Float(); got != 0.4 {
				t.Fatalf("temperature = %v, want 0.4; body=%s", got, doc.String())
			}
			if got := doc.Get("request.generationConfig.top_p").Float(); got != 0.9 {
				t.Fatalf("top_p = %v, want 0.9; body=%s", got, doc.String())
			}
			if got := doc.Get("request.generationConfig.thinking_config.thinking_budget").Int(); got != 2047 {
				t.Fatalf("thinking_budget = %d, want 2047; body=%s", got, doc.String())
			}
		})
	})
}
