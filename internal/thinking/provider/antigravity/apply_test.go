package antigravity

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestAntigravityBudgetDerivedLevelsAreSupported(t *testing.T) {
	model := &registry.ModelInfo{ID: "gemini-3.8-flash", Thinking: &registry.ThinkingSupport{
		Max: 65535, DynamicAllowed: true, Levels: []string{"low", "medium", "high"},
	}}
	for _, tc := range []struct {
		budget int
		level  string
	}{{1, "low"}, {512, "low"}, {1024, "low"}, {8192, "medium"}, {24576, "high"}, {32768, "high"}, {65535, "high"}} {
		cfg, err := thinking.ValidateConfig(thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: tc.budget}, model, "openai", "antigravity", false)
		if err != nil {
			t.Fatal(err)
		}
		body, errApply := NewApplier().Apply([]byte(`{"request":{}}`), *cfg, model)
		if errApply != nil {
			t.Fatal(errApply)
		}
		if got := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != tc.level {
			t.Errorf("budget=%d: thinkingLevel=%q, want %q", tc.budget, got, tc.level)
		}
	}
}
