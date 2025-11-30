package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// TestStripCopilotPrefix verifies that the copilot- prefix is correctly stripped from model names.
func TestStripCopilotPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "model with copilot prefix",
			input:    "copilot-claude-opus-4.5",
			expected: "claude-opus-4.5",
		},
		{
			name:     "model with copilot prefix - gpt",
			input:    "copilot-gpt-5",
			expected: "gpt-5",
		},
		{
			name:     "model with copilot prefix - gemini",
			input:    "copilot-gemini-2.5-pro",
			expected: "gemini-2.5-pro",
		},
		{
			name:     "model without prefix",
			input:    "claude-opus-4.5",
			expected: "claude-opus-4.5",
		},
		{
			name:     "model without prefix - gpt",
			input:    "gpt-5",
			expected: "gpt-5",
		},
		{
			name:     "model with -copilot suffix (not prefix)",
			input:    "gpt-41-copilot",
			expected: "gpt-41-copilot",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "just the prefix",
			input:    "copilot-",
			expected: "",
		},
		{
			name:     "copilot without hyphen",
			input:    "copilotmodel",
			expected: "copilotmodel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripCopilotPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("stripCopilotPrefix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestCopilotModelPrefixConstant verifies the prefix constant is correct.
func TestCopilotModelPrefixConstant(t *testing.T) {
	if registry.CopilotModelPrefix != "copilot-" {
		t.Errorf("CopilotModelPrefix = %q, want %q", registry.CopilotModelPrefix, "copilot-")
	}
}

// TestStatusErr_Error verifies that statusErr implements error correctly.
func TestStatusErr_Error(t *testing.T) {
	err := statusErr{code: 401, msg: "unauthorized"}
	expected := "unauthorized"
	if err.Error() != expected {
		t.Errorf("statusErr.Error() = %q, want %q", err.Error(), expected)
	}

	// Test fallback when msg is empty
	err2 := statusErr{code: 500, msg: ""}
	expected2 := "status 500"
	if err2.Error() != expected2 {
		t.Errorf("statusErr.Error() = %q, want %q", err2.Error(), expected2)
	}
}

// TestGetCopilotModels verifies the static model list contains expected core models.
func TestGetCopilotModels(t *testing.T) {
	models := registry.GetCopilotModels()

	if len(models) == 0 {
		t.Fatal("GetCopilotModels() returned empty list")
	}

	// Check for expected core models (raptor-mini and oswe-vscode-prime)
	expectedModels := map[string]bool{
		"oswe-vscode-prime": false,
		"raptor-mini":       false,
	}

	for _, m := range models {
		if _, ok := expectedModels[m.ID]; ok {
			expectedModels[m.ID] = true
		}
	}

	for model, found := range expectedModels {
		if !found {
			t.Errorf("GetCopilotModels() missing expected model %q", model)
		}
	}
}

// TestGenerateCopilotAliases verifies alias generation.
func TestGenerateCopilotAliases(t *testing.T) {
	input := []*registry.ModelInfo{
		{ID: "gpt-5", DisplayName: "GPT-5", Description: "Test model"},
	}

	result := registry.GenerateCopilotAliases(input)

	// Should have original + alias
	if len(result) != 2 {
		t.Errorf("GenerateCopilotAliases() returned %d models, want 2", len(result))
	}

	// Check alias was created
	var foundAlias bool
	for _, m := range result {
		if m.ID == "copilot-gpt-5" {
			foundAlias = true
			if m.DisplayName != "GPT-5 (Copilot)" {
				t.Errorf("alias DisplayName = %q, want %q", m.DisplayName, "GPT-5 (Copilot)")
			}
		}
	}

	if !foundAlias {
		t.Error("GenerateCopilotAliases() did not create copilot- prefixed alias")
	}
}
