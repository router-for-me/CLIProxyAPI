package qoder

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

func reasoningModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:       "qoder/ultimate",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "max", "xhigh"}, ZeroAllowed: true},
	}
}

func TestApply(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		config    thinking.ThinkingConfig
		modelInfo *registry.ModelInfo
		want      string
		wantSet   bool
	}{
		{
			name:      "level is written as reasoning_effort",
			body:      `{"model":"qoder/ultimate"}`,
			config:    thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelMax},
			modelInfo: reasoningModel(),
			want:      "max",
			wantSet:   true,
		},
		{
			name:      "disabled thinking is signalled with none",
			body:      `{}`,
			config:    thinking.ThinkingConfig{Mode: thinking.ModeNone},
			modelInfo: reasoningModel(),
			want:      "none",
			wantSet:   true,
		},
		{
			name:      "clamped fallback level survives ModeNone",
			body:      `{}`,
			config:    thinking.ThinkingConfig{Mode: thinking.ModeNone, Level: thinking.LevelLow},
			modelInfo: reasoningModel(),
			want:      "low",
			wantSet:   true,
		},
		{
			name:      "budget is converted to a level",
			body:      `{}`,
			config:    thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 24576},
			modelInfo: reasoningModel(),
			wantSet:   true,
		},
		{
			name:      "auto leaves the request untouched",
			body:      `{"model":"qoder/ultimate"}`,
			config:    thinking.ThinkingConfig{Mode: thinking.ModeAuto},
			modelInfo: reasoningModel(),
		},
		{
			name:      "model without thinking support is untouched",
			body:      `{"model":"qoder/efficient"}`,
			config:    thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh},
			modelInfo: &registry.ModelInfo{ID: "qoder/efficient"},
		},
	}

	applier := NewApplier()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applier.Apply([]byte(tc.body), tc.config, tc.modelInfo)
			if err != nil {
				t.Fatalf("Apply returned error: %v", err)
			}
			effort := ojson.GetBytes(got, "reasoning_effort")
			if !tc.wantSet {
				if effort.Exists() {
					t.Fatalf("reasoning_effort = %q, want it absent", effort.String())
				}
				return
			}
			if !effort.Exists() {
				t.Fatal("reasoning_effort is absent, want it set")
			}
			if tc.want != "" && effort.String() != tc.want {
				t.Fatalf("reasoning_effort = %q, want %q", effort.String(), tc.want)
			}
		})
	}
}

func TestApplyRegistersProvider(t *testing.T) {
	if thinking.GetProviderApplier("qoder") == nil {
		t.Fatal("qoder applier is not registered with the thinking pipeline")
	}
}
