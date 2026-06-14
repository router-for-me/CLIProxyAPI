package thinking

import (
	"testing"
)

func TestParseSuffix(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		wantName   string
		wantSuffix string
		wantHas    bool
	}{
		{
			name:       "simple budget suffix",
			model:      "claude-sonnet-4-5(16384)",
			wantName:   "claude-sonnet-4-5",
			wantSuffix: "16384",
			wantHas:    true,
		},
		{
			name:       "level suffix",
			model:      "gpt-5.2(high)",
			wantName:   "gpt-5.2",
			wantSuffix: "high",
			wantHas:    true,
		},
		{
			name:       "no suffix",
			model:      "gemini-2.5-pro",
			wantName:   "gemini-2.5-pro",
			wantSuffix: "",
			wantHas:    false,
		},
		{
			name:       "empty string",
			model:      "",
			wantName:   "",
			wantSuffix: "",
			wantHas:    false,
		},
		{
			name:       "missing closing parenthesis",
			model:      "model(abc",
			wantName:   "model(abc",
			wantSuffix: "",
			wantHas:    false,
		},
		{
			name:       "parentheses but no content",
			model:      "model()",
			wantName:   "model",
			wantSuffix: "",
			wantHas:    true,
		},
		{
			name:       "nested parentheses in model name",
			model:      "model-v1(beta)(8192)",
			wantName:   "model-v1(beta)",
			wantSuffix: "8192",
			wantHas:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSuffix(tt.model)
			if got.ModelName != tt.wantName {
				t.Errorf("ModelName = %q, want %q", got.ModelName, tt.wantName)
			}
			if got.RawSuffix != tt.wantSuffix {
				t.Errorf("RawSuffix = %q, want %q", got.RawSuffix, tt.wantSuffix)
			}
			if got.HasSuffix != tt.wantHas {
				t.Errorf("HasSuffix = %v, want %v", got.HasSuffix, tt.wantHas)
			}
		})
	}
}

func TestParseNumericSuffix(t *testing.T) {
	tests := []struct {
		name       string
		rawSuffix  string
		wantBudget int
		wantOK     bool
	}{
		{name: "positive integer", rawSuffix: "8192", wantBudget: 8192, wantOK: true},
		{name: "zero", rawSuffix: "0", wantBudget: 0, wantOK: true},
		{name: "leading zeros", rawSuffix: "08192", wantBudget: 8192, wantOK: true},
		{name: "max int on 64-bit", rawSuffix: "9223372036854775807", wantBudget: 9223372036854775807, wantOK: true},
		{name: "negative number", rawSuffix: "-1", wantBudget: 0, wantOK: false},
		{name: "negative large", rawSuffix: "-8192", wantBudget: 0, wantOK: false},
		{name: "level name", rawSuffix: "high", wantBudget: 0, wantOK: false},
		{name: "empty string", rawSuffix: "", wantBudget: 0, wantOK: false},
		{name: "non-numeric", rawSuffix: "abc", wantBudget: 0, wantOK: false},
		{name: "overflow on 64-bit", rawSuffix: "9223372036854775808", wantBudget: 0, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, ok := ParseNumericSuffix(tt.rawSuffix)
			if budget != tt.wantBudget {
				t.Errorf("budget = %d, want %d", budget, tt.wantBudget)
			}
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestParseSpecialSuffix(t *testing.T) {
	tests := []struct {
		name      string
		rawSuffix string
		wantMode  ThinkingMode
		wantOK    bool
	}{
		{name: "none", rawSuffix: "none", wantMode: ModeNone, wantOK: true},
		{name: "NONE uppercase", rawSuffix: "NONE", wantMode: ModeNone, wantOK: true},
		{name: "None mixed case", rawSuffix: "None", wantMode: ModeNone, wantOK: true},
		{name: "auto", rawSuffix: "auto", wantMode: ModeAuto, wantOK: true},
		{name: "AUTO uppercase", rawSuffix: "AUTO", wantMode: ModeAuto, wantOK: true},
		{name: "-1 as auto", rawSuffix: "-1", wantMode: ModeAuto, wantOK: true},
		{name: "empty string", rawSuffix: "", wantMode: ModeBudget, wantOK: false},
		{name: "non-special value", rawSuffix: "high", wantMode: ModeBudget, wantOK: false},
		{name: "numeric", rawSuffix: "8192", wantMode: ModeBudget, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, ok := ParseSpecialSuffix(tt.rawSuffix)
			if mode != tt.wantMode {
				t.Errorf("mode = %v, want %v", mode, tt.wantMode)
			}
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestParseLevelSuffix(t *testing.T) {
	tests := []struct {
		name      string
		rawSuffix string
		wantLevel ThinkingLevel
		wantOK    bool
	}{
		{name: "minimal", rawSuffix: "minimal", wantLevel: LevelMinimal, wantOK: true},
		{name: "low", rawSuffix: "low", wantLevel: LevelLow, wantOK: true},
		{name: "medium", rawSuffix: "medium", wantLevel: LevelMedium, wantOK: true},
		{name: "high", rawSuffix: "high", wantLevel: LevelHigh, wantOK: true},
		{name: "xhigh", rawSuffix: "xhigh", wantLevel: LevelXHigh, wantOK: true},
		{name: "max", rawSuffix: "max", wantLevel: LevelMax, wantOK: true},
		{name: "HIGH uppercase", rawSuffix: "HIGH", wantLevel: LevelHigh, wantOK: true},
		{name: "Medium mixed case", rawSuffix: "Medium", wantLevel: LevelMedium, wantOK: true},
		{name: "none (special, not level)", rawSuffix: "none", wantLevel: "", wantOK: false},
		{name: "auto (special, not level)", rawSuffix: "auto", wantLevel: "", wantOK: false},
		{name: "numeric", rawSuffix: "8192", wantLevel: "", wantOK: false},
		{name: "unknown level", rawSuffix: "ultra", wantLevel: "", wantOK: false},
		{name: "empty string", rawSuffix: "", wantLevel: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, ok := ParseLevelSuffix(tt.rawSuffix)
			if level != tt.wantLevel {
				t.Errorf("level = %q, want %q", level, tt.wantLevel)
			}
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}
