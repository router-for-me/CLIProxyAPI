package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestMapToClaudeEffort(t *testing.T) {
	tests := []struct {
		name        string
		level       string
		supportsMax bool
		want        string
		ok          bool
	}{
		{"xhigh passes through when max is supported", "xhigh", true, "xhigh", true},
		{"max passes through when max is supported", "max", true, "max", true},
		{"xhigh clamps to high without max", "xhigh", false, "high", true},
		{"max clamps to high without max", "max", false, "high", true},
		{"minimal maps to low", "minimal", true, "low", true},
		{"medium passes through", "medium", true, "medium", true},
		{"auto maps to high", "auto", true, "high", true},
		{"case and whitespace normalized", " XHigh ", true, "xhigh", true},
		{"unknown level rejected", "ultra", true, "", false},
		{"empty level rejected", "", true, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MapToClaudeEffort(tt.level, tt.supportsMax)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("MapToClaudeEffort(%q, %v) = (%q, %v), want (%q, %v)", tt.level, tt.supportsMax, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestClampLevelPrefersHighIntentFallbacks(t *testing.T) {
	tests := []struct {
		name      string
		level     ThinkingLevel
		supported []string
		want      ThinkingLevel
	}{
		{"xhigh stays xhigh", LevelXHigh, []string{"low", "medium", "high", "xhigh", "max"}, LevelXHigh},
		{"xhigh prefers max over high", LevelXHigh, []string{"low", "medium", "high", "max"}, LevelMax},
		{"xhigh falls back to high", LevelXHigh, []string{"low", "medium", "high"}, LevelHigh},
		{"max prefers xhigh over high", LevelMax, []string{"low", "medium", "high", "xhigh"}, LevelXHigh},
		{"max falls back to high", LevelMax, []string{"low", "medium", "high"}, LevelHigh},
		{"other levels keep nearest-lower tie break", LevelMedium, []string{"low", "high"}, LevelLow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modelInfo := &registry.ModelInfo{ID: "claude-test", Thinking: &registry.ThinkingSupport{Levels: tt.supported}}
			if got := clampLevel(tt.level, modelInfo, "claude"); got != tt.want {
				t.Fatalf("clampLevel(%q, %v) = %q, want %q", tt.level, tt.supported, got, tt.want)
			}
		})
	}
}
