package config

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy"
)

func TestSanitizeOpenAICompatibilityCodeBuddyDefaults(t *testing.T) {
	cfg := &Config{OpenAICompatibility: []OpenAICompatibility{{
		Name:     "  codebuddy  ",
		AuthType: " CodeBuddy ",
	}}}
	cfg.SanitizeOpenAICompatibility()

	entry := cfg.OpenAICompatibility[0]
	if entry.Name != "codebuddy" {
		t.Fatalf("Name = %q, want codebuddy", entry.Name)
	}
	if entry.AuthType != codebuddy.AuthType {
		t.Fatalf("AuthType = %q, want %q", entry.AuthType, codebuddy.AuthType)
	}
	if entry.BaseURL != codebuddy.DefaultBackendBaseURL {
		t.Fatalf("BaseURL = %q, want %q", entry.BaseURL, codebuddy.DefaultBackendBaseURL)
	}
	if len(entry.Models) != len(codebuddy.DefaultModels) {
		t.Fatalf("model count = %d, want %d", len(entry.Models), len(codebuddy.DefaultModels))
	}
	for i, model := range entry.Models {
		if model.Name != codebuddy.DefaultModels[i] || model.Alias != codebuddy.DefaultModels[i] {
			t.Errorf("model[%d] = %#v, want name/alias %q", i, model, codebuddy.DefaultModels[i])
		}
	}
}
