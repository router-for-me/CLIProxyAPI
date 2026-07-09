package thinking

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestValidateConfigMapsCrossProviderHighIntentLevels(t *testing.T) {
	tests := []struct {
		name      string
		requested ThinkingLevel
		supported []string
		want      ThinkingLevel
	}{
		{
			name:      "xhigh prefers max before high",
			requested: LevelXHigh,
			supported: []string{"max", "high"},
			want:      LevelMax,
		},
		{
			name:      "xhigh falls back to high",
			requested: LevelXHigh,
			supported: []string{"high", "medium", "low"},
			want:      LevelHigh,
		},
		{
			name:      "max falls back to xhigh",
			requested: LevelMax,
			supported: []string{"xhigh", "high"},
			want:      LevelXHigh,
		},
		{
			name:      "max falls back to high",
			requested: LevelMax,
			supported: []string{"high", "medium", "low"},
			want:      LevelHigh,
		},
		{
			name:      "xhigh remains xhigh when supported",
			requested: LevelXHigh,
			supported: []string{"xhigh", "max", "high"},
			want:      LevelXHigh,
		},
		{
			name:      "max remains max when supported",
			requested: LevelMax,
			supported: []string{"max", "xhigh", "high"},
			want:      LevelMax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateConfig(
				ThinkingConfig{Mode: ModeLevel, Level: tt.requested},
				userDefinedLevelModel(tt.supported),
				"openai",
				"claude",
				false,
			)
			if err != nil {
				t.Fatalf("ValidateConfig() error = %v", err)
			}
			if got == nil {
				t.Fatal("ValidateConfig() = nil, want config")
			}
			if got.Mode != ModeLevel || got.Level != tt.want {
				t.Fatalf("ValidateConfig() = mode %s level %q, want level %q", got.Mode, got.Level, tt.want)
			}
		})
	}
}

func TestValidateConfigDoesNotClampHighIntentBelowHigh(t *testing.T) {
	for _, requested := range []ThinkingLevel{LevelXHigh, LevelMax} {
		t.Run(string(requested), func(t *testing.T) {
			_, err := ValidateConfig(
				ThinkingConfig{Mode: ModeLevel, Level: requested},
				userDefinedLevelModel([]string{"medium", "low"}),
				"openai",
				"claude",
				false,
			)
			if err == nil {
				t.Fatal("ValidateConfig() error = nil, want unsupported level")
			}
			if !strings.Contains(err.Error(), `not supported`) {
				t.Fatalf("error = %q, want unsupported level", err.Error())
			}
		})
	}
}

func TestValidateConfigDoesNotMapHighIntentWithinSameProviderFamily(t *testing.T) {
	tests := []struct {
		name      string
		requested ThinkingLevel
		supported []string
		from      string
		to        string
	}{
		{
			name:      "claude xhigh to claude high",
			requested: LevelXHigh,
			supported: []string{"high", "medium", "low"},
			from:      "claude",
			to:        "claude",
		},
		{
			name:      "claude max to claude xhigh",
			requested: LevelMax,
			supported: []string{"xhigh", "high"},
			from:      "claude",
			to:        "claude",
		},
		{
			name:      "openai xhigh to codex high",
			requested: LevelXHigh,
			supported: []string{"high"},
			from:      "openai",
			to:        "codex",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := userDefinedLevelModel(tt.supported)
			model.Type = tt.to
			_, err := ValidateConfig(
				ThinkingConfig{Mode: ModeLevel, Level: tt.requested},
				model,
				tt.from,
				tt.to,
				false,
			)
			if err == nil {
				t.Fatal("ValidateConfig() error = nil, want unsupported level")
			}
			if !strings.Contains(err.Error(), `not supported`) {
				t.Fatalf("error = %q, want unsupported level", err.Error())
			}
		})
	}
}

func TestValidateConfigHandlesModelFamilyMismatch(t *testing.T) {
	t.Run("maps high intent across model family mismatch", func(t *testing.T) {
		model := userDefinedLevelModel([]string{"max", "high"})
		model.Type = "kimi"

		got, err := ValidateConfig(
			ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh},
			model,
			"claude",
			"claude",
			false,
		)
		if err != nil {
			t.Fatalf("ValidateConfig() error = %v", err)
		}
		if got == nil || got.Mode != ModeLevel || got.Level != LevelMax {
			t.Fatalf("ValidateConfig() = %+v, want max level", got)
		}
	})

	t.Run("keeps ordinary explicit levels strict", func(t *testing.T) {
		model := userDefinedLevelModel([]string{"low", "high"})
		model.Type = "kimi"

		_, err := ValidateConfig(
			ThinkingConfig{Mode: ModeLevel, Level: LevelMinimal},
			model,
			"claude",
			"claude",
			false,
		)
		if err == nil {
			t.Fatal("ValidateConfig() error = nil, want unsupported level")
		}
		if !strings.Contains(err.Error(), `not supported`) {
			t.Fatalf("error = %q, want unsupported level", err.Error())
		}
	})
}

func TestValidateConfigClampsInferredAliasThinking(t *testing.T) {
	got, err := ValidateConfig(
		ThinkingConfig{Mode: ModeLevel, Level: LevelMinimal},
		inferredAliasLevelModel([]string{"low", "high"}),
		"openai",
		"gemini",
		false,
	)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got == nil {
		t.Fatal("ValidateConfig() = nil, want config")
	}
	if got.Mode != ModeLevel || got.Level != LevelLow {
		t.Fatalf("ValidateConfig() = mode %s level %q, want low", got.Mode, got.Level)
	}
}

func TestValidateConfigRejectsExplicitUnsupportedAliasThinking(t *testing.T) {
	_, err := ValidateConfig(
		ThinkingConfig{Mode: ModeLevel, Level: LevelMinimal},
		userDefinedLevelModel([]string{"low", "high"}),
		"openai",
		"gemini",
		false,
	)
	if err == nil {
		t.Fatal("ValidateConfig() error = nil, want unsupported level")
	}
	if !strings.Contains(err.Error(), `not supported`) {
		t.Fatalf("error = %q, want unsupported level", err.Error())
	}
}

func TestValidateConfigConvertsAutoToSupportedMidpointLevel(t *testing.T) {
	got, err := ValidateConfig(
		ThinkingConfig{Mode: ModeLevel, Level: LevelAuto},
		userDefinedLevelModel([]string{"low", "high"}),
		"openai",
		"claude",
		false,
	)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got == nil {
		t.Fatal("ValidateConfig() = nil, want config")
	}
	if got.Mode != ModeLevel || got.Level != LevelLow {
		t.Fatalf("ValidateConfig() = mode %s level %q, want low", got.Mode, got.Level)
	}
}

func TestValidateConfigRejectsExplicitUnsupportedNone(t *testing.T) {
	model := &registry.ModelInfo{
		ID:               "test/model",
		Type:             "codex",
		UserDefined:      true,
		ThinkingExplicit: true,
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}
	for _, config := range []ThinkingConfig{
		{Mode: ModeLevel, Level: LevelNone},
		{Mode: ModeBudget, Budget: 0},
		{Mode: ModeNone},
	} {
		_, err := ValidateConfig(
			config,
			model,
			"openai",
			"codex",
			false,
		)
		if err == nil {
			t.Fatalf("ValidateConfig(%+v) error = nil, want unsupported none", config)
		}
		if !strings.Contains(err.Error(), `level "none" not supported`) {
			t.Fatalf("error = %q, want unsupported none", err.Error())
		}
	}
}

func TestValidateConfigAllowsExplicitSupportedNone(t *testing.T) {
	tests := []struct {
		name  string
		model *registry.ModelInfo
	}{
		{
			name:  "none level",
			model: userDefinedLevelModel([]string{"low", "medium", "high", "none"}),
		},
		{
			name: "zero allowed",
			model: &registry.ModelInfo{
				ID:               "test/model",
				Type:             "codex",
				UserDefined:      true,
				ThinkingExplicit: true,
				Thinking: &registry.ThinkingSupport{
					ZeroAllowed: true,
					Levels:      []string{"low", "medium", "high"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateConfig(
				ThinkingConfig{Mode: ModeLevel, Level: LevelNone},
				tt.model,
				"openai",
				"codex",
				false,
			)
			if err != nil {
				t.Fatalf("ValidateConfig() error = %v", err)
			}
			if got == nil || got.Mode != ModeNone {
				t.Fatalf("ValidateConfig() = %+v, want ModeNone", got)
			}
		})
	}
}

func userDefinedLevelModel(levels []string) *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:               "test/model",
		Type:             "claude",
		UserDefined:      true,
		ThinkingExplicit: true,
		Thinking: &registry.ThinkingSupport{
			ZeroAllowed: true,
			Levels:      levels,
		},
	}
}

func inferredAliasLevelModel(levels []string) *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:          "test/model",
		Type:        "gemini",
		UserDefined: true,
		Thinking: &registry.ThinkingSupport{
			ZeroAllowed: true,
			Levels:      levels,
		},
	}
}
