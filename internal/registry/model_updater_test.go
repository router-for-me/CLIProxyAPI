package registry

import (
	"testing"
)

func TestPreserveClaudeSonnet55AcrossRemoteRefresh(t *testing.T) {
	current := []*ModelInfo{{ID: "claude-sonnet-5-5", DisplayName: "Local Sonnet 5.5"}}
	remote := []*ModelInfo{{ID: "claude-sonnet-5"}}
	merged := preserveClaudeSonnet55(remote, current)
	if len(merged) != 2 || merged[1].ID != "claude-sonnet-5-5" {
		t.Fatalf("missing local Sonnet 5.5 after refresh: %+v", merged)
	}
	merged[1].DisplayName = "changed"
	if current[0].DisplayName != "Local Sonnet 5.5" {
		t.Fatal("remote catalog reused the local model pointer")
	}

	remote = []*ModelInfo{{ID: "claude-sonnet-5-5", DisplayName: "Remote Sonnet 5.5"}}
	merged = preserveClaudeSonnet55(remote, current)
	if len(merged) != 1 || merged[0].DisplayName != "Remote Sonnet 5.5" {
		t.Fatalf("remote Sonnet 5.5 should take precedence: %+v", merged)
	}
}

func TestDetectChangedProviders_CodexConfigurationUpdate(t *testing.T) {
	oldData := &staticModelsJSON{
		CodexFree: []*ModelInfo{{ID: "gpt-6-luna"}},
	}
	newData := &staticModelsJSON{
		CodexFree: []*ModelInfo{{ID: "gpt-6-luna", SupportConfigurationUpdate: true}},
	}

	changed := detectChangedProviders(oldData, newData)
	if len(changed) != 1 || changed[0] != "codex" {
		t.Fatalf("configuration_update-only change: got providers %v, want [codex]", changed)
	}
}

func TestDetectChangedProviders_KimiAliases(t *testing.T) {
	oldData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}},
	}
	newData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}, {ID: "kimi-k3"}},
	}

	changed := detectChangedProviders(oldData, newData)
	expected := map[string]bool{
		"kimi":     false,
		"kimi-ai":  false,
		"kimi.ai":  false,
		"kimi.com": false,
	}

	for _, p := range changed {
		if _, ok := expected[p]; ok {
			expected[p] = true
		}
	}

	for p, found := range expected {
		if !found {
			t.Errorf("expected changed provider %q to be reported, got %v", p, changed)
		}
	}
}
