package config

import "testing"

func TestSanitizePayloadRules_KeepsDisabledRawRuleWithInvalidJSON(t *testing.T) {
	cfg := &Config{
		Payload: PayloadConfig{
			DefaultRaw: []PayloadRule{
				{
					Disabled: true,
					Models:   []PayloadModelRule{{Name: "gpt-*"}},
					Params: map[string]any{
						"metadata": `{"enabled":`,
					},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()

	if len(cfg.Payload.DefaultRaw) != 1 {
		t.Fatalf("disabled raw rule should be preserved during sanitize, got %d rules", len(cfg.Payload.DefaultRaw))
	}
	if !cfg.Payload.DefaultRaw[0].Disabled {
		t.Fatalf("disabled raw rule should remain disabled after sanitize")
	}
}

func TestSanitizePayloadRules_DropsEnabledRawRuleWithInvalidJSON(t *testing.T) {
	cfg := &Config{
		Payload: PayloadConfig{
			DefaultRaw: []PayloadRule{
				{
					Models: []PayloadModelRule{{Name: "gpt-*"}},
					Params: map[string]any{
						"metadata": `{"enabled":`,
					},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()

	if len(cfg.Payload.DefaultRaw) != 0 {
		t.Fatalf("enabled raw rule with invalid JSON should be dropped, got %d rules", len(cfg.Payload.DefaultRaw))
	}
}

func TestSanitizePayloadRules_KeepsDisabledRawParamWithEquivalentDottedPath(t *testing.T) {
	cfg := &Config{
		Payload: PayloadConfig{
			DefaultRaw: []PayloadRule{
				{
					Models: []PayloadModelRule{{Name: "gpt-*"}},
					Params: map[string]any{
						".trace_id": `{"enabled":`,
						"reasoning": `{"budget_tokens":1024}`,
					},
					DisabledParams: []string{"trace_id"},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()

	if len(cfg.Payload.DefaultRaw) != 1 {
		t.Fatalf("disabled dotted raw param should not drop the whole rule, got %d rules", len(cfg.Payload.DefaultRaw))
	}
}
