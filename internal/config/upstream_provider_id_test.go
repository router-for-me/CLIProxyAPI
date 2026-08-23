package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpstreamProviderIDSurvivesConfigReload(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("write initial config: %v", errWrite)
	}

	wantID := int64(94)
	cfg := &Config{
		ClaudeKey: []ClaudeKey{{APIKey: "test-key", UpstreamProviderID: wantID}},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	loaded, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if got := loaded.ClaudeKey[0].UpstreamProviderID; got != wantID {
		t.Fatalf("reloaded UpstreamProviderID = %d, want %d", got, wantID)
	}
}
