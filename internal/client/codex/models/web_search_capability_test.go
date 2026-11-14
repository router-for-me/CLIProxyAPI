package models

import "testing"

func TestCPAWebSearchCapabilityNeverTrustsTemplateClaims(t *testing.T) {
	for _, clientVersion := range []string{"cpa", "CPA", "codex", ""} {
		t.Run(clientVersion, func(t *testing.T) {
			entry := map[string]any{"cpa_capabilities": map[string]any{"web_search": true}}
			applyCPAWebSearchCapability(entry, "unknown", func(string) *bool { return nil }, clientVersion)
			if _, exists := entry["cpa_capabilities"]; exists {
				t.Fatal("inherited template capability should be removed")
			}
		})
	}
}

func TestCPAWebSearchCapabilityOverridesTemplateWithExplicitFalse(t *testing.T) {
	entry := map[string]any{"cpa_capabilities": map[string]any{"web_search": true}}
	unsupported := false
	applyCPAWebSearchCapability(entry, "known-unsupported", func(string) *bool { return &unsupported }, "cpa")
	capabilities, ok := entry["cpa_capabilities"].(map[string]any)
	if !ok || capabilities["web_search"] != false {
		t.Fatal("explicit registered false must override template claims")
	}
}
