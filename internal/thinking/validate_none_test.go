package thinking

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
)

func TestValidateConfigDisabledLevelOnlyUsesLowestWithoutBudgetWarning(t *testing.T) {
	previousOutput := log.StandardLogger().Out
	previousLevel := log.GetLevel()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetLevel(log.WarnLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
	})

	model := &registry.ModelInfo{
		ID: "openrouter-3o",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"max", "xhigh", "high", "medium", "low"},
		},
	}
	got, err := ValidateConfig(ThinkingConfig{Mode: ModeNone}, model, "claude", "openai", false)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got.Mode != ModeLevel || got.Budget != 0 || got.Level != LevelLow {
		t.Fatalf("ValidateConfig() = %+v, want ModeLevel low with zero budget", got)
	}
	if strings.Contains(output.String(), "budget zero not allowed") {
		t.Fatalf("disabled level-only thinking emitted a misleading warning: %s", output.String())
	}
}

func TestValidateConfigDisabledHybridKeepsBudgetFallback(t *testing.T) {
	model := &registry.ModelInfo{
		ID: "hybrid-model",
		Thinking: &registry.ThinkingSupport{
			Min: 1024, Max: 32768, Levels: []string{"low", "high"},
		},
	}
	got, err := ValidateConfig(ThinkingConfig{Mode: ModeNone}, model, "claude", "gemini", false)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got.Mode != ModeNone || got.Budget != 1024 || got.Level != LevelLow {
		t.Fatalf("ValidateConfig() = %+v, want prior hybrid fallback budget 1024 and level low", got)
	}
}

func TestValidateConfigDisabledLevelOnlyKeepsSupportedNoneWithoutBudgetWarning(t *testing.T) {
	previousOutput := log.StandardLogger().Out
	previousLevel := log.GetLevel()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetLevel(log.WarnLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
	})

	model := &registry.ModelInfo{
		ID: "level-model-with-none",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"high", "none", "low"},
		},
	}
	got, err := ValidateConfig(ThinkingConfig{Mode: ModeNone}, model, "claude", "openai", false)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got.Mode != ModeNone || got.Budget != 0 || got.Level != "" {
		t.Fatalf("ValidateConfig() = %+v, want unchanged disabled mode", got)
	}
	if strings.Contains(output.String(), "budget zero not allowed") {
		t.Fatalf("supported disabled thinking emitted a misleading warning: %s", output.String())
	}
}
