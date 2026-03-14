package antigravity

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApply_ClaudeBudgetIsReducedBelowMaxOutputTokens(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-opus-4-6-thinking",
		MaxCompletionTokens: 64000,
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            64000,
			ZeroAllowed:    true,
			DynamicAllowed: true,
		},
	}
	body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":64000}}}`)

	out, err := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 64000}, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("thinkingBudget = %d, want 63999, body=%s", got, string(out))
	}
	if !gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts should remain enabled, body=%s", string(out))
	}
}

func TestApply_ClaudeBudgetUsesModelMaxWhenRequestMaxMissing(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-opus-4-6-thinking",
		MaxCompletionTokens: 64000,
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            64000,
			ZeroAllowed:    true,
			DynamicAllowed: true,
		},
	}

	out, err := applier.Apply([]byte(`{"request":{}}`), thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 64000}, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("maxOutputTokens = %d, want 64000, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
		t.Fatalf("thinkingBudget = %d, want 63999, body=%s", got, string(out))
	}
}
