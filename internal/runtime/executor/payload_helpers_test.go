package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithRoot_SkipsDisabledDefaultRule(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{
				{
					Disabled: true,
					Models:   []config.PayloadModelRule{{Name: "gpt-*"}},
					Params:   map[string]any{"temperature": 0.7},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-4o","messages":[]}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "")
	if gjson.GetBytes(got, "temperature").Exists() {
		t.Fatalf("disabled default rule should not write temperature, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_SkipsDisabledOverrideParams(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models:         []config.PayloadModelRule{{Name: "gpt-*"}},
					Params:         map[string]any{"temperature": 0.7, "top_p": 0.9},
					DisabledParams: []string{"top_p"},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-4o","messages":[]}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "")

	if value := gjson.GetBytes(got, "temperature"); !value.Exists() || value.Float() != 0.7 {
		t.Fatalf("enabled param should be written, got %s", got)
	}
	if gjson.GetBytes(got, "top_p").Exists() {
		t.Fatalf("disabled param should not be written, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_SkipsDisabledDefaultRawParams(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			DefaultRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-*"}},
					Params: map[string]any{
						"metadata":  `{"enabled":true}`,
						"reasoning": `{"budget_tokens":1024}`,
					},
					DisabledParams: []string{"metadata"},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-4o","messages":[]}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "")

	if gjson.GetBytes(got, "metadata").Exists() {
		t.Fatalf("disabled raw param should not be written, got %s", got)
	}
	if value := gjson.GetBytes(got, "reasoning.budget_tokens"); !value.Exists() || value.Int() != 1024 {
		t.Fatalf("enabled raw param should be written, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_SkipsDisabledFilterRule(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Disabled: true,
					Models:   []config.PayloadModelRule{{Name: "gpt-*"}},
					Params:   []string{"temperature"},
				},
				{
					Models: []config.PayloadModelRule{{Name: "gpt-*"}},
					Params: []string{"top_p"},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-4o","messages":[],"temperature":0.7,"top_p":0.9}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "")

	if !gjson.GetBytes(got, "temperature").Exists() {
		t.Fatalf("disabled filter rule should keep temperature, got %s", got)
	}
	if gjson.GetBytes(got, "top_p").Exists() {
		t.Fatalf("enabled filter rule should remove top_p, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_SkipsDisabledFilterParams(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models:         []config.PayloadModelRule{{Name: "gpt-*"}},
					Params:         []string{"temperature", "top_p"},
					DisabledParams: []string{"temperature"},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-4o","messages":[],"temperature":0.7,"top_p":0.9}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "")

	if !gjson.GetBytes(got, "temperature").Exists() {
		t.Fatalf("disabled filter param should keep temperature, got %s", got)
	}
	if gjson.GetBytes(got, "top_p").Exists() {
		t.Fatalf("enabled filter param should remove top_p, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_KeepsRuleWhenDisabledRawParamHasInvalidJSON(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			DefaultRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-*"}},
					Params: map[string]any{
						"metadata":  `{"enabled":`,
						"reasoning": `{"budget_tokens":1024}`,
					},
					DisabledParams: []string{" metadata "},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()
	if len(cfg.Payload.DefaultRaw) != 1 {
		t.Fatalf("disabled invalid raw param should not drop the whole rule, got %d rules", len(cfg.Payload.DefaultRaw))
	}

	payload := []byte(`{"model":"gpt-4o","messages":[]}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "")

	if gjson.GetBytes(got, "metadata").Exists() {
		t.Fatalf("disabled invalid raw param should not be written, got %s", got)
	}
	if value := gjson.GetBytes(got, "reasoning.budget_tokens"); !value.Exists() || value.Int() != 1024 {
		t.Fatalf("enabled raw param should still be written after sanitize, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_DropsRuleWhenEnabledRawParamHasInvalidJSON(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			DefaultRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-*"}},
					Params: map[string]any{
						"metadata":  `{"enabled":`,
						"reasoning": `{"budget_tokens":1024}`,
					},
				},
			},
		},
	}

	cfg.SanitizePayloadRules()
	if len(cfg.Payload.DefaultRaw) != 0 {
		t.Fatalf("enabled invalid raw param should still drop the rule, got %d rules", len(cfg.Payload.DefaultRaw))
	}

	payload := []byte(`{"model":"gpt-4o","messages":[]}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "")
	if gjson.GetBytes(got, "reasoning").Exists() {
		t.Fatalf("dropped rule should not write any raw params, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_SkipsDisabledParamsWithEquivalentDottedPath(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models:         []config.PayloadModelRule{{Name: "gpt-*"}},
					Params:         map[string]any{".trace_id": "abc123"},
					DisabledParams: []string{"trace_id"},
				},
			},
		},
	}

	payload := []byte(`{"model":"gpt-4o","metadata":{}}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gpt-4o", "", "", payload, payload, "metadata")

	if gjson.GetBytes(got, "metadata.trace_id").Exists() {
		t.Fatalf("disabled dotted param should not be written, got %s", got)
	}
}

func TestApplyPayloadConfigWithRoot_SkipsDisabledParamsWithRootedPath(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			OverrideRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gemini-*"}},
					Params: map[string]any{
						"generationConfig.thinkingConfig.thinkingBudget": `8192`,
					},
					DisabledParams: []string{"request.generationConfig.thinkingConfig.thinkingBudget"},
				},
			},
		},
	}

	payload := []byte(`{"request":{"model":"gemini-2.5-pro","generationConfig":{"thinkingConfig":{}}}}`)
	got := helps.ApplyPayloadConfigWithRoot(cfg, "gemini-2.5-pro", "gemini", "request", payload, payload, "")

	if gjson.GetBytes(got, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Fatalf("disabled rooted param should not be written, got %s", got)
	}
}
