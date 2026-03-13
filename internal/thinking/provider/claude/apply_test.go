package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/tidwall/gjson"
)

func TestNormalizeClaudeBudget_RaisesMaxTokens(t *testing.T) {
	a := &Applier{}
	modelInfo := &registry.ModelInfo{
		MaxCompletionTokens: 64000,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000},
	}
	body := []byte(`{"max_tokens":1000,"thinking":{"type":"enabled","budget_tokens":5000}}`)

	out := a.normalizeClaudeBudget(body, 5000, modelInfo)

	maxTok := gjson.GetBytes(out, "max_tokens").Int()
	if maxTok != 5001 {
		t.Fatalf("max_tokens = %d, want 5001, body=%s", maxTok, string(out))
	}
}

func TestNormalizeClaudeBudget_ClampsToModelMax(t *testing.T) {
	a := &Applier{}
	modelInfo := &registry.ModelInfo{
		MaxCompletionTokens: 64000,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000},
	}
	body := []byte(`{"max_tokens":500,"thinking":{"type":"enabled","budget_tokens":200000}}`)

	out := a.normalizeClaudeBudget(body, 200000, modelInfo)

	maxTok := gjson.GetBytes(out, "max_tokens").Int()
	if maxTok != 64000 {
		t.Fatalf("max_tokens = %d, want 64000 (capped to model limit), body=%s", maxTok, string(out))
	}
	budget := gjson.GetBytes(out, "thinking.budget_tokens").Int()
	if budget != 63999 {
		t.Fatalf("budget_tokens = %d, want 63999 (max_tokens-1), body=%s", budget, string(out))
	}
}

func TestNormalizeClaudeBudget_DisablesThinkingWhenUnsatisfiable(t *testing.T) {
	a := &Applier{}
	modelInfo := &registry.ModelInfo{
		MaxCompletionTokens: 1000,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000},
	}
	body := []byte(`{"max_tokens":500,"thinking":{"type":"enabled","budget_tokens":2000}}`)

	out := a.normalizeClaudeBudget(body, 2000, modelInfo)

	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should be removed when constraints are unsatisfiable, body=%s", string(out))
	}
}

func TestNormalizeClaudeBudget_NoClamping(t *testing.T) {
	a := &Applier{}
	modelInfo := &registry.ModelInfo{
		MaxCompletionTokens: 64000,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000},
	}
	body := []byte(`{"max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":16000}}`)

	out := a.normalizeClaudeBudget(body, 16000, modelInfo)

	maxTok := gjson.GetBytes(out, "max_tokens").Int()
	if maxTok != 32000 {
		t.Fatalf("max_tokens should remain 32000, got %d, body=%s", maxTok, string(out))
	}
	budget := gjson.GetBytes(out, "thinking.budget_tokens").Int()
	if budget != 16000 {
		t.Fatalf("budget_tokens should remain 16000, got %d, body=%s", budget, string(out))
	}
}

func TestNormalizeClaudeBudget_AdjustsBudgetToMaxMinus1(t *testing.T) {
	a := &Applier{}
	modelInfo := &registry.ModelInfo{
		MaxCompletionTokens: 8192,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000},
	}
	body := []byte(`{"max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":10000}}`)

	out := a.normalizeClaudeBudget(body, 10000, modelInfo)

	maxTok := gjson.GetBytes(out, "max_tokens").Int()
	if maxTok != 8192 {
		t.Fatalf("max_tokens = %d, want 8192 (unchanged), body=%s", maxTok, string(out))
	}
	budget := gjson.GetBytes(out, "thinking.budget_tokens").Int()
	if budget != 8191 {
		t.Fatalf("budget_tokens = %d, want 8191 (max_tokens-1), body=%s", budget, string(out))
	}
}
