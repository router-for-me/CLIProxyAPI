package kimi

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApply_ModeNone_UsesDisabledThinking(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "kimi-k2.5",
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
	}
	body := []byte(`{"model":"kimi-k2.5","reasoning_effort":"none","thinking":{"type":"enabled","budget_tokens":2048}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "disabled", string(out))
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("thinking.budget_tokens should be removed, body=%s", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed in ModeNone, body=%s", string(out))
	}
}

func TestApply_ModeLevel_UsesReasoningEffort(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "kimi-k2.5",
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
	}
	body := []byte(`{"model":"kimi-k2.5","thinking":{"type":"disabled"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "high", string(out))
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should be removed when reasoning_effort is used, body=%s", string(out))
	}
}

func TestApply_UserDefinedModeNone_UsesDisabledThinking(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:          "custom-kimi-model",
		UserDefined: true,
	}
	body := []byte(`{"model":"custom-kimi-model","reasoning_effort":"none"}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "disabled", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed in ModeNone, body=%s", string(out))
	}
}
