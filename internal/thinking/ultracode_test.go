package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestUltracodeNormalizesToXHigh(t *testing.T) {
	if got := NormalizeLevelAlias("UltraCode"); got != string(LevelXHigh) {
		t.Fatalf("NormalizeLevelAlias(UltraCode) = %q, want %q", got, LevelXHigh)
	}

	level, ok := ParseLevelSuffix("ultra-code")
	if !ok || level != LevelXHigh {
		t.Fatalf("ParseLevelSuffix(ultra-code) = %q, %v; want %q, true", level, ok, LevelXHigh)
	}

	budget, ok := ConvertLevelToBudget("ultra_code")
	if !ok || budget != 32768 {
		t.Fatalf("ConvertLevelToBudget(ultra_code) = %d, %v; want 32768, true", budget, ok)
	}
}

func TestMapToClaudeEffortKeepsXHighWhenSupported(t *testing.T) {
	levels := []string{"low", "medium", "high", "xhigh", "max"}

	got, ok := MapToClaudeEffort("ultracode", levels)
	if !ok || got != string(LevelXHigh) {
		t.Fatalf("MapToClaudeEffort(ultracode) = %q, %v; want %q, true", got, ok, LevelXHigh)
	}
}

func TestMapToClaudeEffortFallsBackBelowUnsupportedXHigh(t *testing.T) {
	levels := []string{"low", "medium", "high", "max"}

	got, ok := MapToClaudeEffort("ultracode", levels)
	if !ok || got != string(LevelHigh) {
		t.Fatalf("MapToClaudeEffort(ultracode) = %q, %v; want %q, true", got, ok, LevelHigh)
	}
}

func TestValidateConfigClampsClaudeXHighWhenUnsupported(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:   "claude-sonnet-4-6",
		Type: "claude",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high", "max"},
		},
	}

	config := ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh}
	got, err := ValidateConfig(config, modelInfo, "claude", "claude", false)
	if err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	if got.Level != LevelHigh {
		t.Fatalf("ValidateConfig level = %q, want %q", got.Level, LevelHigh)
	}
}
