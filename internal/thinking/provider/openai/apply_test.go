package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApplyLevelFallbackAfterDisabledThinking(t *testing.T) {
	model := &registry.ModelInfo{
		ID: "openrouter-3o",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"max", "xhigh", "high", "medium", "low"},
		},
	}
	config, err := thinking.ValidateConfig(
		thinking.ThinkingConfig{Mode: thinking.ModeNone}, model, "claude", "openai", false,
	)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	body, err := NewApplier().Apply(
		[]byte(`{"messages":[{"role":"user","content":"hi"}]}`), *config, model,
	)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "low" {
		t.Fatalf("reasoning_effort = %q, want low; body=%s", got, body)
	}
}
