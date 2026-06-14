package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApplyCompatibleOpenAI_ModeLevel_PassesThroughLevels(t *testing.T) {
	tests := []struct {
		name            string
		level           thinking.ThinkingLevel
		wantEffort      string
		wantFieldExists bool
	}{
		{
			name:            "xhigh passes through unchanged",
			level:           thinking.LevelXHigh,
			wantEffort:      "xhigh",
			wantFieldExists: true,
		},
		{
			name:            "max passes through unchanged",
			level:           thinking.LevelMax,
			wantEffort:      "max",
			wantFieldExists: true,
		},
		{
			name:            "high passes through unchanged",
			level:           thinking.LevelHigh,
			wantEffort:      "high",
			wantFieldExists: true,
		},
		{
			name:            "medium passes through unchanged",
			level:           thinking.LevelMedium,
			wantEffort:      "medium",
			wantFieldExists: true,
		},
		{
			name:            "low passes through unchanged",
			level:           thinking.LevelLow,
			wantEffort:      "low",
			wantFieldExists: true,
		},
		{
			name:            "empty level returns body unchanged",
			level:           "",
			wantFieldExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"test-model","messages":[]}`)
			config := thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: tt.level}
			result, err := applyCompatibleOpenAI(body, config)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			field := gjson.GetBytes(result, "reasoning_effort")
			if field.Exists() != tt.wantFieldExists {
				t.Fatalf("reasoning_effort exists = %v, want %v. Output: %s", field.Exists(), tt.wantFieldExists, string(result))
			}
			if tt.wantFieldExists && field.String() != tt.wantEffort {
				t.Fatalf("reasoning_effort = %q, want %q. Output: %s", field.String(), tt.wantEffort, string(result))
			}
		})
	}
}

func TestApplyCompatibleOpenAI_ModeNone(t *testing.T) {
	body := []byte(`{"model":"test-model","messages":[]}`)
	config := thinking.ThinkingConfig{Mode: thinking.ModeNone}
	result, err := applyCompatibleOpenAI(body, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be absent for ModeNone. Output: %s", string(result))
	}
}

func TestApplyCompatibleOpenAI_ModeAuto(t *testing.T) {
	body := []byte(`{"model":"test-model","reasoning_effort":"medium","messages":[]}`)
	config := thinking.ThinkingConfig{Mode: thinking.ModeAuto}
	result, err := applyCompatibleOpenAI(body, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be deleted for ModeAuto. Output: %s", string(result))
	}
}

func TestApplyCompatibleOpenAI_ModeBudget_HighBudgetPassesThrough(t *testing.T) {
	body := []byte(`{"model":"test-model","messages":[]}`)
	// Budget 64000 maps to xhigh, which should now pass through as xhigh.
	config := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 64000}
	result, err := applyCompatibleOpenAI(body, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := gjson.GetBytes(result, "reasoning_effort").String()
	if got != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want %q (xhigh should pass through). Output: %s", got, "xhigh", string(result))
	}
}
