package qoder

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApplierWritesQoderReasoningEffort(t *testing.T) {
	applier := &Applier{}
	model := &registry.ModelInfo{ID: "auto", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}, ZeroAllowed: true}}
	for _, test := range []struct {
		name   string
		config thinking.ThinkingConfig
		want   string
	}{
		{name: "level", config: thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, want: "high"},
		{name: "disabled", config: thinking.ThinkingConfig{Mode: thinking.ModeNone, Level: thinking.LevelNone}, want: "none"},
		{name: "budget", config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 16000}, want: "high"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := applier.Apply([]byte(`{"model":"auto"}`), test.config, model)
			if err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(body, "reasoning_effort").String(); got != test.want {
				t.Fatalf("reasoning_effort = %q, want %q; body = %s", got, test.want, body)
			}
		})
	}
}

func TestApplierLeavesModelsWithoutThinkingMetadata(t *testing.T) {
	body := []byte(`{"model":"auto"}`)
	out, err := (&Applier{}).Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, &registry.ModelInfo{ID: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(body) {
		t.Fatalf("body changed: %s", out)
	}
}
