package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/thinking"
	"github.com/tidwall/gjson"
)

func TestNormalizeClaudeBudget_WritesDefaultedMaxTokensAndReducesBudget(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"model":"claude-sonnet-4.5","input":"ping"}`)
	model := &registry.ModelInfo{
		ID:                  "claude-sonnet-4.5",
		MaxCompletionTokens: 1024,
		Thinking:            &registry.ThinkingSupport{Min: 256},
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 2000,
	}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res := gjson.ParseBytes(out)
	if res.Get("max_tokens").Int() != 1024 {
		t.Fatalf("expected max_tokens to be set from model default, got %d", res.Get("max_tokens").Int())
	}
	if res.Get("thinking.budget_tokens").Int() != 1023 {
		t.Fatalf("expected budget_tokens to be reduced below max_tokens, got %d", res.Get("thinking.budget_tokens").Int())
	}
}

func TestNormalizeClaudeBudget_RespectsProvidedMaxTokens(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"model":"claude-sonnet-4.5","max_tokens":4096,"input":"ping"}`)
	model := &registry.ModelInfo{
		ID:                  "claude-sonnet-4.5",
		MaxCompletionTokens: 1024,
		Thinking:            &registry.ThinkingSupport{Min: 256},
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 2048,
	}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res := gjson.ParseBytes(out)
	if res.Get("thinking.budget_tokens").Int() != 2048 {
		t.Fatalf("expected explicit budget_tokens to be preserved when max_tokens is higher, got %d", res.Get("thinking.budget_tokens").Int())
	}
	if res.Get("max_tokens").Int() != 4096 {
		t.Fatalf("expected explicit max_tokens to be preserved, got %d", res.Get("max_tokens").Int())
	}
}

func TestNormalizeClaudeBudget_NoMinBudgetRegressionBelowMinimum(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"model":"claude-sonnet-4.5","max_tokens":300,"input":"ping"}`)
	model := &registry.ModelInfo{
		ID:                  "claude-sonnet-4.5",
		MaxCompletionTokens: 1024,
		Thinking:            &registry.ThinkingSupport{Min: 1024},
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 2000,
	}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res := gjson.ParseBytes(out)
	if res.Get("thinking.budget_tokens").Int() != 2000 {
		t.Fatalf("expected no budget adjustment when reduction would violate model minimum, got %d", res.Get("thinking.budget_tokens").Int())
	}
}
