package registry

import (
	"testing"
)

func TestGetKiroModels(t *testing.T) {
	models := GetKiroModels()
	if len(models) == 0 {
		t.Fatalf("expected non-empty Kiro models list")
	}

	foundSonnet := false
	for _, m := range models {
		if m.ID == "claude-sonnet-4.5" || m.ID == "kiro/claude-sonnet-4.5" {
			foundSonnet = true
			if m.Type != "kiro" {
				t.Errorf("expected type 'kiro', got %s", m.Type)
			}
		}
	}
	if !foundSonnet {
		t.Fatalf("expected claude-sonnet-4.5 in Kiro models")
	}

	staticLookup := LookupStaticModelInfoByChannel("claude-sonnet-4.5", "kiro")
	if staticLookup == nil {
		t.Fatalf("LookupStaticModelInfoByChannel failed for kiro")
	}
	if staticLookup.ID != "claude-sonnet-4.5" {
		t.Fatalf("unexpected model ID: %s", staticLookup.ID)
	}
}
