package deepseek

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApply_ModeNone_UsesDisabledThinking(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "deepseek-v4-pro",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"model":"deepseek-v4-pro","reasoning_effort":"high","thinking":{"type":"enabled"}}`)

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

func TestApply_ModeLevel_UsesReasoningEffort(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "deepseek-v4-pro",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"model":"deepseek-v4-pro","thinking":{"type":"disabled"}}`)

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
		ID:          "custom-deepseek-model",
		UserDefined: true,
	}
	body := []byte(`{"model":"custom-deepseek-model","reasoning_effort":"high"}`)

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

func TestApply_ModeLevel_MaxLevel(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "deepseek-v4-pro",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"model":"deepseek-v4-pro"}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelMax}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "max" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "max", string(out))
	}
}

func TestApply_ModeBudget_ConvertsToLevel(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "deepseek-v4-pro",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}

	tests := []struct {
		name       string
		budget     int
		wantEffort string
	}{
		{name: "budget_20000_maps_to_high", budget: 20000, wantEffort: "high"},
		{name: "budget_30000_maps_to_xhigh", budget: 30000, wantEffort: "xhigh"},
		{name: "budget_0_maps_to_none", budget: 0, wantEffort: "none"},
		{name: "budget_minus1_maps_to_auto", budget: -1, wantEffort: "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"deepseek-v4-pro","thinking":{"type":"enabled"}}`)
			out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: tt.budget}, modelInfo)
			if errApply != nil {
				t.Fatalf("Apply() error = %v", errApply)
			}
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != tt.wantEffort {
				t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, tt.wantEffort, string(out))
			}
		})
	}
}

func TestApply_ModeBudget_InvalidReturnsBody(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "deepseek-v4-pro",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"model":"deepseek-v4-pro","thinking":{"type":"enabled"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: -5}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be absent for invalid budget, body=%s", string(out))
	}
}

func TestApply_ModeAuto_SetsAuto(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "deepseek-v4-pro",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"model":"deepseek-v4-pro","thinking":{"type":"enabled"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeAuto}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "auto" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "auto", string(out))
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should be removed in ModeAuto, body=%s", string(out))
	}
}

func TestApply_UserDefinedModeBudget_ConvertsToLevel(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:          "custom-deepseek-model",
		UserDefined: true,
	}
	body := []byte(`{"model":"custom-deepseek-model","thinking":{"type":"enabled"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 20000}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "high", string(out))
	}
}

func TestApply_UserDefinedModeAuto_SetsAuto(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:          "custom-deepseek-model",
		UserDefined: true,
	}
	body := []byte(`{"model":"custom-deepseek-model","reasoning_effort":"high"}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeAuto}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "auto" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "auto", string(out))
	}
}
