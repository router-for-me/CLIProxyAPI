package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
)

// registerGemini3Models loads Gemini 3 models into the registry for testing.
func registerGemini3Models(t *testing.T) func() {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("gemini3-test-%d", time.Now().UnixNano())
	reg.RegisterClient(uid+"-gemini", "gemini", registry.GetGeminiModels())
	reg.RegisterClient(uid+"-aistudio", "aistudio", registry.GetAIStudioModels())
	return func() {
		reg.UnregisterClient(uid + "-gemini")
		reg.UnregisterClient(uid + "-aistudio")
	}
}

func TestIsGemini3Model(t *testing.T) {
	cases := []struct {
		model    string
		expected bool
	}{
		{"gemini-3-pro-preview", true},
		{"gemini-3-flash-preview", true},
		{"gemini_3_pro_preview", true},
		{"gemini-3-pro", true},
		{"gemini-3-flash", true},
		{"GEMINI-3-PRO-PREVIEW", true},
		{"gemini-2.5-pro", false},
		{"gemini-2.5-flash", false},
		{"gpt-5", false},
		{"claude-sonnet-4-5", false},
		{"", false},
	}

	for _, cs := range cases {
		t.Run(cs.model, func(t *testing.T) {
			got := util.IsGemini3Model(cs.model)
			if got != cs.expected {
				t.Fatalf("IsGemini3Model(%q) = %v, want %v", cs.model, got, cs.expected)
			}
		})
	}
}

func TestIsGemini3ProModel(t *testing.T) {
	cases := []struct {
		model    string
		expected bool
	}{
		{"gemini-3-pro-preview", true},
		{"gemini_3_pro_preview", true},
		{"gemini-3-pro", true},
		{"GEMINI-3-PRO-PREVIEW", true},
		{"gemini-3-flash-preview", false},
		{"gemini-3-flash", false},
		{"gemini-2.5-pro", false},
		{"", false},
	}

	for _, cs := range cases {
		t.Run(cs.model, func(t *testing.T) {
			got := util.IsGemini3ProModel(cs.model)
			if got != cs.expected {
				t.Fatalf("IsGemini3ProModel(%q) = %v, want %v", cs.model, got, cs.expected)
			}
		})
	}
}

func TestIsGemini3FlashModel(t *testing.T) {
	cases := []struct {
		model    string
		expected bool
	}{
		{"gemini-3-flash-preview", true},
		{"gemini_3_flash_preview", true},
		{"gemini-3-flash", true},
		{"GEMINI-3-FLASH-PREVIEW", true},
		{"gemini-3-pro-preview", false},
		{"gemini-3-pro", false},
		{"gemini-2.5-flash", false},
		{"", false},
	}

	for _, cs := range cases {
		t.Run(cs.model, func(t *testing.T) {
			got := util.IsGemini3FlashModel(cs.model)
			if got != cs.expected {
				t.Fatalf("IsGemini3FlashModel(%q) = %v, want %v", cs.model, got, cs.expected)
			}
		})
	}
}

func TestValidateGemini3ThinkingLevel(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		level   string
		wantOK  bool
		wantVal string
	}{
		// Gemini 3 Pro: supports "low", "high"
		{"pro-low", "gemini-3-pro-preview", "low", true, "low"},
		{"pro-high", "gemini-3-pro-preview", "high", true, "high"},
		{"pro-minimal-invalid", "gemini-3-pro-preview", "minimal", false, ""},
		{"pro-medium-invalid", "gemini-3-pro-preview", "medium", false, ""},

		// Gemini 3 Flash: supports "minimal", "low", "medium", "high"
		{"flash-minimal", "gemini-3-flash-preview", "minimal", true, "minimal"},
		{"flash-low", "gemini-3-flash-preview", "low", true, "low"},
		{"flash-medium", "gemini-3-flash-preview", "medium", true, "medium"},
		{"flash-high", "gemini-3-flash-preview", "high", true, "high"},

		// Case insensitivity
		{"flash-LOW-case", "gemini-3-flash-preview", "LOW", true, "low"},
		{"flash-High-case", "gemini-3-flash-preview", "High", true, "high"},
		{"pro-HIGH-case", "gemini-3-pro-preview", "HIGH", true, "high"},

		// Invalid levels
		{"flash-invalid", "gemini-3-flash-preview", "xhigh", false, ""},
		{"flash-invalid-auto", "gemini-3-flash-preview", "auto", false, ""},
		{"flash-empty", "gemini-3-flash-preview", "", false, ""},

		// Non-Gemini 3 models
		{"non-gemini3", "gemini-2.5-pro", "high", false, ""},
		{"gpt5", "gpt-5", "high", false, ""},
	}

	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			got, ok := util.ValidateGemini3ThinkingLevel(cs.model, cs.level)
			if ok != cs.wantOK {
				t.Fatalf("ValidateGemini3ThinkingLevel(%q, %q) ok = %v, want %v", cs.model, cs.level, ok, cs.wantOK)
			}
			if got != cs.wantVal {
				t.Fatalf("ValidateGemini3ThinkingLevel(%q, %q) = %q, want %q", cs.model, cs.level, got, cs.wantVal)
			}
		})
	}
}

func TestThinkingBudgetToGemini3Level(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		budget  int
		wantOK  bool
		wantVal string
	}{
		// Gemini 3 Pro: maps to "low" or "high"
		{"pro-dynamic", "gemini-3-pro-preview", -1, true, "high"},
		{"pro-zero", "gemini-3-pro-preview", 0, true, "low"},
		{"pro-small", "gemini-3-pro-preview", 1000, true, "low"},
		{"pro-medium", "gemini-3-pro-preview", 8000, true, "low"},
		{"pro-large", "gemini-3-pro-preview", 20000, true, "high"},
		{"pro-huge", "gemini-3-pro-preview", 50000, true, "high"},

		// Gemini 3 Flash: maps to "minimal", "low", "medium", "high"
		{"flash-dynamic", "gemini-3-flash-preview", -1, true, "high"},
		{"flash-zero", "gemini-3-flash-preview", 0, true, "minimal"},
		{"flash-tiny", "gemini-3-flash-preview", 500, true, "minimal"},
		{"flash-small", "gemini-3-flash-preview", 1000, true, "low"},
		{"flash-medium-val", "gemini-3-flash-preview", 8000, true, "medium"},
		{"flash-large", "gemini-3-flash-preview", 20000, true, "high"},
		{"flash-huge", "gemini-3-flash-preview", 50000, true, "high"},

		// Non-Gemini 3 models should return false
		{"gemini25-budget", "gemini-2.5-pro", 8000, false, ""},
		{"gpt5-budget", "gpt-5", 8000, false, ""},
	}

	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			got, ok := util.ThinkingBudgetToGemini3Level(cs.model, cs.budget)
			if ok != cs.wantOK {
				t.Fatalf("ThinkingBudgetToGemini3Level(%q, %d) ok = %v, want %v", cs.model, cs.budget, ok, cs.wantOK)
			}
			if got != cs.wantVal {
				t.Fatalf("ThinkingBudgetToGemini3Level(%q, %d) = %q, want %q", cs.model, cs.budget, got, cs.wantVal)
			}
		})
	}
}

func TestApplyGemini3ThinkingLevelFromMetadata(t *testing.T) {
	cleanup := registerGemini3Models(t)
	defer cleanup()

	cases := []struct {
		name         string
		model        string
		metadata     map[string]any
		inputBody    string
		wantLevel    string
		wantInclude  bool
		wantNoChange bool
	}{
		{
			name:        "flash-minimal-from-suffix",
			model:       "gemini-3-flash-preview",
			metadata:    map[string]any{"reasoning_effort": "minimal"},
			inputBody:   `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`,
			wantLevel:   "minimal",
			wantInclude: true,
		},
		{
			name:        "flash-medium-from-suffix",
			model:       "gemini-3-flash-preview",
			metadata:    map[string]any{"reasoning_effort": "medium"},
			inputBody:   `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`,
			wantLevel:   "medium",
			wantInclude: true,
		},
		{
			name:        "pro-high-from-suffix",
			model:       "gemini-3-pro-preview",
			metadata:    map[string]any{"reasoning_effort": "high"},
			inputBody:   `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`,
			wantLevel:   "high",
			wantInclude: true,
		},
		{
			name:         "no-metadata-no-change",
			model:        "gemini-3-flash-preview",
			metadata:     nil,
			inputBody:    `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`,
			wantNoChange: true,
		},
		{
			name:         "non-gemini3-no-change",
			model:        "gemini-2.5-pro",
			metadata:     map[string]any{"reasoning_effort": "high"},
			inputBody:    `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`,
			wantNoChange: true,
		},
		{
			name:         "invalid-level-no-change",
			model:        "gemini-3-flash-preview",
			metadata:     map[string]any{"reasoning_effort": "xhigh"},
			inputBody:    `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`,
			wantNoChange: true,
		},
	}

	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			input := []byte(cs.inputBody)
			result := util.ApplyGemini3ThinkingLevelFromMetadata(cs.model, cs.metadata, input)

			if cs.wantNoChange {
				if string(result) != cs.inputBody {
					t.Fatalf("expected no change, but got: %s", string(result))
				}
				return
			}

			level := gjson.GetBytes(result, "generationConfig.thinkingConfig.thinkingLevel")
			if !level.Exists() {
				t.Fatalf("thinkingLevel not set in result: %s", string(result))
			}
			if level.String() != cs.wantLevel {
				t.Fatalf("thinkingLevel = %q, want %q", level.String(), cs.wantLevel)
			}

			include := gjson.GetBytes(result, "generationConfig.thinkingConfig.includeThoughts")
			if cs.wantInclude && (!include.Exists() || !include.Bool()) {
				t.Fatalf("includeThoughts should be true, got: %s", string(result))
			}
		})
	}
}

func TestApplyGemini3ThinkingLevelFromMetadataCLI(t *testing.T) {
	cleanup := registerGemini3Models(t)
	defer cleanup()

	cases := []struct {
		name         string
		model        string
		metadata     map[string]any
		inputBody    string
		wantLevel    string
		wantInclude  bool
		wantNoChange bool
	}{
		{
			name:        "flash-minimal-from-suffix-cli",
			model:       "gemini-3-flash-preview",
			metadata:    map[string]any{"reasoning_effort": "minimal"},
			inputBody:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`,
			wantLevel:   "minimal",
			wantInclude: true,
		},
		{
			name:        "flash-low-from-suffix-cli",
			model:       "gemini-3-flash-preview",
			metadata:    map[string]any{"reasoning_effort": "low"},
			inputBody:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`,
			wantLevel:   "low",
			wantInclude: true,
		},
		{
			name:        "pro-low-from-suffix-cli",
			model:       "gemini-3-pro-preview",
			metadata:    map[string]any{"reasoning_effort": "low"},
			inputBody:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`,
			wantLevel:   "low",
			wantInclude: true,
		},
		{
			name:         "no-metadata-no-change-cli",
			model:        "gemini-3-flash-preview",
			metadata:     nil,
			inputBody:    `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`,
			wantNoChange: true,
		},
		{
			name:         "non-gemini3-no-change-cli",
			model:        "gemini-2.5-pro",
			metadata:     map[string]any{"reasoning_effort": "high"},
			inputBody:    `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}}`,
			wantNoChange: true,
		},
	}

	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			input := []byte(cs.inputBody)
			result := util.ApplyGemini3ThinkingLevelFromMetadataCLI(cs.model, cs.metadata, input)

			if cs.wantNoChange {
				if string(result) != cs.inputBody {
					t.Fatalf("expected no change, but got: %s", string(result))
				}
				return
			}

			level := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel")
			if !level.Exists() {
				t.Fatalf("thinkingLevel not set in result: %s", string(result))
			}
			if level.String() != cs.wantLevel {
				t.Fatalf("thinkingLevel = %q, want %q", level.String(), cs.wantLevel)
			}

			include := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts")
			if cs.wantInclude && (!include.Exists() || !include.Bool()) {
				t.Fatalf("includeThoughts should be true, got: %s", string(result))
			}
		})
	}
}

func TestNormalizeGeminiThinkingBudget_Gemini3Conversion(t *testing.T) {
	cleanup := registerGemini3Models(t)
	defer cleanup()

	cases := []struct {
		name       string
		model      string
		inputBody  string
		wantLevel  string
		wantBudget bool // if true, expect thinkingBudget instead of thinkingLevel
	}{
		{
			name:      "gemini3-flash-budget-to-level",
			model:     "gemini-3-flash-preview",
			inputBody: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":8000}}}`,
			wantLevel: "medium",
		},
		{
			name:      "gemini3-pro-budget-to-level",
			model:     "gemini-3-pro-preview",
			inputBody: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":20000}}}`,
			wantLevel: "high",
		},
		{
			name:       "gemini25-keeps-budget",
			model:      "gemini-2.5-pro",
			inputBody:  `{"generationConfig":{"thinkingConfig":{"thinkingBudget":8000}}}`,
			wantBudget: true,
		},
	}

	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			result := util.NormalizeGeminiThinkingBudget(cs.model, []byte(cs.inputBody))

			if cs.wantBudget {
				budget := gjson.GetBytes(result, "generationConfig.thinkingConfig.thinkingBudget")
				if !budget.Exists() {
					t.Fatalf("thinkingBudget should exist for non-Gemini3 model: %s", string(result))
				}
				level := gjson.GetBytes(result, "generationConfig.thinkingConfig.thinkingLevel")
				if level.Exists() {
					t.Fatalf("thinkingLevel should not exist for non-Gemini3 model: %s", string(result))
				}
			} else {
				level := gjson.GetBytes(result, "generationConfig.thinkingConfig.thinkingLevel")
				if !level.Exists() {
					t.Fatalf("thinkingLevel should exist for Gemini3 model: %s", string(result))
				}
				if level.String() != cs.wantLevel {
					t.Fatalf("thinkingLevel = %q, want %q", level.String(), cs.wantLevel)
				}
				budget := gjson.GetBytes(result, "generationConfig.thinkingConfig.thinkingBudget")
				if budget.Exists() {
					t.Fatalf("thinkingBudget should be removed for Gemini3 model: %s", string(result))
				}
			}
		})
	}
}
