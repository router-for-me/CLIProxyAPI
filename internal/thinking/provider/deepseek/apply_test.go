package deepseek

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApplierApply_LevelSetsThinkingAndEffort(t *testing.T) {
	t.Parallel()

	applier := NewApplier()
	modelInfo := &registry.ModelInfo{Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}}}

	tests := []struct {
		name  string
		level thinking.ThinkingLevel
		want  string
	}{
		{name: "high", level: thinking.LevelHigh, want: "high"},
		{name: "max", level: thinking.LevelMax, want: "max"},
		{name: "medium maps to high", level: thinking.LevelMedium, want: "high"},
		{name: "xhigh maps to max", level: thinking.LevelXHigh, want: "max"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := applier.Apply([]byte(`{"model":"deepseek-v4-pro"}`), thinking.ThinkingConfig{
				Mode:  thinking.ModeLevel,
				Level: tt.level,
			}, modelInfo)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled; body=%s", got, out)
			}
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != tt.want {
				t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, tt.want, out)
			}
		})
	}
}

func TestApplierApply_NoneDisablesThinking(t *testing.T) {
	t.Parallel()

	applier := NewApplier()
	out, err := applier.Apply([]byte(`{"reasoning_effort":"high"}`), thinking.ThinkingConfig{
		Mode: thinking.ModeNone,
	}, &registry.ModelInfo{Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled; body=%s", got, out)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed; body=%s", out)
	}
}
