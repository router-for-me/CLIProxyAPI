package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
)

func TestValidateConfig_ClampBudgetToModelMinAndMaxBoundaries(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "clamp-model",
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            32000,
			ZeroAllowed:    false,
			DynamicAllowed: false,
		},
	}

	tests := []struct {
		name        string
		config      ThinkingConfig
		fromFormat  string
		toFormat    string
		fromSuffix  bool
		wantMode    ThinkingMode
		wantBudget  int
		wantLevel   ThinkingLevel
		wantErrCode ErrorCode
		wantErrNil  bool
	}{
		{
			name:       "below min clamps up",
			config:     ThinkingConfig{Mode: ModeBudget, Budget: 10},
			fromFormat: "openai",
			toFormat:   "claude",
			wantMode:   ModeBudget,
			wantBudget: 1024,
			wantErrNil: true,
		},
		{
			name:       "zero clamps up when zero disallowed",
			config:     ThinkingConfig{Mode: ModeBudget, Budget: 0},
			fromFormat: "openai",
			toFormat:   "claude",
			wantMode:   ModeNone,
			wantBudget: 0,
			wantErrNil: true,
		},
		{
			name:       "negative clamps up when same source is suffix-based",
			config:     ThinkingConfig{Mode: ModeBudget, Budget: -5},
			fromFormat: "openai",
			toFormat:   "claude",
			fromSuffix: true,
			wantMode:   ModeBudget,
			wantBudget: 1024,
			wantErrNil: true,
		},
		{
			name:       "above max clamps down",
			config:     ThinkingConfig{Mode: ModeBudget, Budget: 64000},
			fromFormat: "openai",
			toFormat:   "claude",
			fromSuffix: true,
			wantMode:   ModeBudget,
			wantBudget: 32000,
			wantErrNil: true,
		},
		{
			name:        "same provider strict mode rejects out-of-range budget",
			config:      ThinkingConfig{Mode: ModeBudget, Budget: 64000},
			fromFormat:  "claude",
			toFormat:    "claude",
			wantErrNil:  false,
			wantErrCode: ErrBudgetOutOfRange,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, err := ValidateConfig(tt.config, modelInfo, tt.fromFormat, tt.toFormat, tt.fromSuffix)
			if tt.wantErrNil && err != nil {
				t.Fatalf("ValidateConfig(...) unexpected error: %v", err)
			}
			if !tt.wantErrNil {
				thinkingErr, ok := err.(*ThinkingError)
				if !ok {
					t.Fatalf("expected ThinkingError, got: %T %v", err, err)
				}
				if thinkingErr.Code != tt.wantErrCode {
					t.Fatalf("error code=%s, want=%s", thinkingErr.Code, tt.wantErrCode)
				}
				return
			}

			if done == nil {
				t.Fatal("expected non-nil config")
			}
			if done.Mode != tt.wantMode {
				t.Fatalf("Mode=%s, want=%s", done.Mode, tt.wantMode)
			}
			if done.Budget != tt.wantBudget {
				t.Fatalf("Budget=%d, want=%d", done.Budget, tt.wantBudget)
			}
			if done.Level != tt.wantLevel {
				t.Fatalf("Level=%s, want=%s", done.Level, tt.wantLevel)
			}
		})
	}
}

func TestValidateConfig_LevelReboundToSupportedSet(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "hybrid-level-model",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "high"},
		},
	}

	tests := []struct {
		name        string
		budget      int
		fromFormat  string
		toFormat    string
		wantLevel   ThinkingLevel
		wantBudget  int
		wantMode    ThinkingMode
		wantErrCode ErrorCode
	}{
		{
			name:       "budget converts to minimal then clamps to lowest supported",
			budget:     10,
			fromFormat: "gemini",
			toFormat:   "openai",
			wantMode:   ModeLevel,
			wantLevel:  LevelLow,
			wantBudget: 0,
		},
		{
			name:       "budget between low and high stays low on tie lower",
			budget:     3000,
			fromFormat: "gemini",
			toFormat:   "openai",
			wantMode:   ModeLevel,
			wantLevel:  LevelLow,
			wantBudget: 0,
		},
		{
			name:        "unsupported discrete level rejected",
			budget:      0,
			fromFormat:  "openai",
			toFormat:    "openai",
			wantMode:    ModeLevel,
			wantLevel:   LevelXHigh,
			wantErrCode: ErrLevelNotSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := ThinkingConfig{Mode: ModeBudget, Budget: tt.budget}
			if tt.name == "unsupported discrete level rejected" {
				config = ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh}
			}

			got, err := ValidateConfig(config, modelInfo, tt.fromFormat, tt.toFormat, false)
			if tt.name == "unsupported discrete level rejected" {
				if err == nil {
					t.Fatal("expected error")
				}
				thinkingErr, ok := err.(*ThinkingError)
				if !ok {
					t.Fatalf("expected ThinkingError, got %T %v", err, err)
				}
				if thinkingErr.Code != tt.wantErrCode {
					t.Fatalf("error code=%s, want=%s", thinkingErr.Code, tt.wantErrCode)
				}
				return
			}

			if err != nil {
				t.Fatalf("ValidateConfig unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("expected non-nil config")
			}
			if got.Mode != tt.wantMode {
				t.Fatalf("Mode=%s, want=%s", got.Mode, tt.wantMode)
			}
			if got.Budget != tt.wantBudget {
				t.Fatalf("Budget=%d, want=%d", got.Budget, tt.wantBudget)
			}
			if got.Level != tt.wantLevel {
				t.Fatalf("Level=%s, want=%s", got.Level, tt.wantLevel)
			}
		})
	}
}

func TestValidateConfig_ZeroAllowedBudgetPreserved(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "zero-allowed-model",
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            32000,
			ZeroAllowed:    true,
			DynamicAllowed: false,
		},
	}

	got, err := ValidateConfig(ThinkingConfig{Mode: ModeBudget, Budget: 0}, modelInfo, "openai", "openai", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected config")
	}
	if got.Mode != ModeNone {
		t.Fatalf("Mode=%s, want=%s", got.Mode, ModeNone)
	}
	if got.Budget != 0 {
		t.Fatalf("Budget=%d, want=0", got.Budget)
	}
}

func TestValidateConfig_ModeAutoFallsBackToMidpointWhenDynamicUnsupported(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "auto-midpoint-model",
		Thinking: &registry.ThinkingSupport{
			Min:            1000,
			Max:            3000,
			DynamicAllowed: false,
		},
	}

	got, err := ValidateConfig(ThinkingConfig{Mode: ModeAuto, Budget: -1}, modelInfo, "openai", "claude", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected config")
	}
	if got.Mode != ModeBudget {
		t.Fatalf("Mode=%s, want=%s", got.Mode, ModeBudget)
	}
	if got.Budget != 2000 {
		t.Fatalf("Budget=%d, want=2000", got.Budget)
	}
}

func TestValidateConfig_ModeAutoPreservedWhenDynamicAllowed(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "auto-preserved-model",
		Thinking: &registry.ThinkingSupport{
			Min:            1000,
			Max:            3000,
			DynamicAllowed: true,
		},
	}

	got, err := ValidateConfig(ThinkingConfig{Mode: ModeAuto, Budget: -1}, modelInfo, "openai", "claude", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected config")
	}
	if got.Mode != ModeAuto {
		t.Fatalf("Mode=%s, want=%s", got.Mode, ModeAuto)
	}
	if got.Budget != -1 {
		t.Fatalf("Budget=%d, want=-1", got.Budget)
	}
}

func TestValidateConfig_ErrorIncludesModelContext(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "ctx-model",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "high"},
		},
	}

	_, err := ValidateConfig(ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh}, modelInfo, "openai", "openai", false)
	if err == nil {
		t.Fatal("expected error")
	}
	thinkingErr, ok := err.(*ThinkingError)
	if !ok {
		t.Fatalf("expected ThinkingError, got %T %v", err, err)
	}
	if thinkingErr.Code != ErrLevelNotSupported {
		t.Fatalf("error code=%s, want=%s", thinkingErr.Code, ErrLevelNotSupported)
	}
	if thinkingErr.Model != "ctx-model" {
		t.Fatalf("error model=%q, want=%q", thinkingErr.Model, "ctx-model")
	}
}
