package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeReasoningOnIngressByFormat(t *testing.T) {
	t.Parallel()

	input := map[string]ReasoningIngressDefault{
		" OPENAI ": {
			Policy: " MISSING_ONLY ",
			Mode:   " Effort ",
			Value:  " XHIGH ",
		},
		"claude": {
			Policy: "force_override",
			Mode:   "adaptive_effort",
			Value:  "High",
		},
	}

	normalized, err := NormalizeReasoningOnIngressByFormat(input)
	if err != nil {
		t.Fatalf("NormalizeReasoningOnIngressByFormat() error = %v", err)
	}

	openAIEntry, ok := normalized[ReasoningIngressFormatOpenAI]
	if !ok {
		t.Fatalf("missing format %q", ReasoningIngressFormatOpenAI)
	}
	if openAIEntry.Policy != ReasoningIngressPolicyMissingOnly || openAIEntry.Mode != ReasoningModeEffort || openAIEntry.Value != "xhigh" {
		t.Fatalf("openai entry = %+v", openAIEntry)
	}

	claudeEntry, ok := normalized[ReasoningIngressFormatClaude]
	if !ok {
		t.Fatalf("missing format %q", ReasoningIngressFormatClaude)
	}
	if claudeEntry.Policy != ReasoningIngressPolicyForceOverride || claudeEntry.Mode != ReasoningModeAdaptiveEffort || claudeEntry.Value != "high" {
		t.Fatalf("claude entry = %+v", claudeEntry)
	}
}

func TestNormalizeReasoningOnIngressByFormat_InvalidPolicy(t *testing.T) {
	t.Parallel()

	_, err := NormalizeReasoningOnIngressByFormat(map[string]ReasoningIngressDefault{
		"openai": {
			Policy: "always",
			Mode:   "effort",
			Value:  "high",
		},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "policy") {
		t.Fatalf("error = %q, want contains %q", err.Error(), "policy")
	}
}

func TestResolveReasoningOnIngressEntry(t *testing.T) {
	t.Parallel()

	defaults := map[string]ReasoningIngressDefault{
		ReasoningIngressFormatOpenAI: {
			Policy: ReasoningIngressPolicyMissingOnly,
			Mode:   ReasoningModeEffort,
			Value:  "high",
		},
	}

	entry, ok := ResolveReasoningOnIngressEntry(defaults, " OpenAI ")
	if !ok {
		t.Fatalf("expected openai entry")
	}
	if entry.Policy != ReasoningIngressPolicyMissingOnly || entry.Mode != ReasoningModeEffort || entry.Value != "high" {
		t.Fatalf("entry = %+v", entry)
	}

	if _, ok = ResolveReasoningOnIngressEntry(defaults, "claude"); ok {
		t.Fatalf("expected claude not found")
	}
}

func TestLoadConfigOptional_RejectLegacyDefaultReasoningEffortKey(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := []byte("port: 8317\ndefault-reasoning-effort-on-missing: \"high\"\n")
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfigOptional(configPath, false)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "default-reasoning-effort-on-missing") {
		t.Fatalf("error = %q, want contains legacy key", err.Error())
	}
}

func TestLoadConfigOptional_RejectLegacyByProviderKey(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := []byte("port: 8317\ndefault-reasoning-on-missing-by-provider:\n  openai:\n    mode: effort\n    value: high\n")
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfigOptional(configPath, false)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "default-reasoning-on-missing-by-provider") {
		t.Fatalf("error = %q, want contains deprecated key", err.Error())
	}
}

func TestLoadConfigOptional_NormalizeReasoningIngressByFormat(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := []byte(`port: 8317
default-reasoning-on-ingress-by-format:
  OPENAI:
    policy: " MISSING_ONLY "
    mode: " Effort "
    value: " XHIGH "
  claude:
    policy: force_override
    mode: adaptive_effort
    value: medium
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	openAIEntry, ok := cfg.DefaultReasoningOnIngressByFormat[ReasoningIngressFormatOpenAI]
	if !ok {
		t.Fatalf("expected format %q in defaults", ReasoningIngressFormatOpenAI)
	}
	if openAIEntry.Policy != ReasoningIngressPolicyMissingOnly || openAIEntry.Mode != ReasoningModeEffort || openAIEntry.Value != "xhigh" {
		t.Fatalf("openai entry = %+v", openAIEntry)
	}

	claudeEntry, ok := cfg.DefaultReasoningOnIngressByFormat[ReasoningIngressFormatClaude]
	if !ok {
		t.Fatalf("expected format %q in defaults", ReasoningIngressFormatClaude)
	}
	if claudeEntry.Policy != ReasoningIngressPolicyForceOverride || claudeEntry.Mode != ReasoningModeAdaptiveEffort || claudeEntry.Value != "medium" {
		t.Fatalf("claude entry = %+v", claudeEntry)
	}
}
