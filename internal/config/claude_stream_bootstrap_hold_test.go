package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The hold defaults on, so an explicit false is its off switch: it must survive
// a save that adds it, both when the claude mapping is new and in a v8 file.
func TestSaveConfigPreserveComments_StreamBootstrapHoldExplicitFalse(t *testing.T) {
	if !(&ClaudeConfig{}).StreamBootstrapHoldEnabled() {
		t.Fatal("the hold is off when unset, want on")
	}

	t.Run("new claude mapping", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
			t.Fatalf("os.WriteFile() error = %v", errWrite)
		}
		if errSave := SaveConfigPreserveComments(configPath, &Config{Claude: ClaudeConfig{StreamBootstrapHold: new(false)}}); errSave != nil {
			t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
		}
		savedBytes, errRead := os.ReadFile(configPath)
		if errRead != nil {
			t.Fatalf("os.ReadFile() error = %v", errRead)
		}
		if !strings.Contains(string(savedBytes), "stream-bootstrap-hold: false") {
			t.Fatalf("saved YAML missing 'stream-bootstrap-hold: false':\n%s", savedBytes)
		}
		loaded, errLoad := LoadConfigOptional(configPath, false)
		if errLoad != nil {
			t.Fatalf("LoadConfigOptional() error = %v", errLoad)
		}
		if loaded.Claude.StreamBootstrapHoldEnabled() {
			t.Fatalf("the hold is on after reloading an explicit false; saved:\n%s", savedBytes)
		}
	})

	t.Run("v8 file and clearing", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		if errWrite := os.WriteFile(configPath, []byte("config-version: 8\nserver:\n  port: 8317\n"), 0o600); errWrite != nil {
			t.Fatalf("os.WriteFile() error = %v", errWrite)
		}
		if errSave := SaveConfigPreserveComments(configPath, &Config{Port: 8317, Claude: ClaudeConfig{StreamBootstrapHold: new(false)}}, true); errSave != nil {
			t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
		}
		savedBytes, errRead := os.ReadFile(configPath)
		if errRead != nil {
			t.Fatalf("os.ReadFile() error = %v", errRead)
		}
		var savedDoc yaml.Node
		if errUnmarshal := yaml.Unmarshal(savedBytes, &savedDoc); errUnmarshal != nil {
			t.Fatalf("yaml.Unmarshal(savedBytes) error = %v", errUnmarshal)
		}
		if node := yamlPath(savedDoc.Content[0], "upstream.claude.stream-bootstrap-hold"); node == nil || node.Value != "false" {
			t.Fatalf("saved YAML missing upstream.claude.stream-bootstrap-hold=false:\n%s", savedBytes)
		}
		reloaded, errReload := LoadConfig(configPath)
		if errReload != nil {
			t.Fatalf("LoadConfig() error = %v", errReload)
		}
		if reloaded.Claude.StreamBootstrapHoldEnabled() {
			t.Fatalf("the hold is on after reloading an explicit false; saved:\n%s", savedBytes)
		}

		// Clearing back to nil removes the persisted override, so the default applies again.
		reloaded.Claude.StreamBootstrapHold = nil
		if errSave := SaveConfigPreserveComments(configPath, reloaded, true); errSave != nil {
			t.Fatalf("SaveConfigPreserveComments(nil) error = %v", errSave)
		}
		cleared, errClear := LoadConfig(configPath)
		if errClear != nil {
			t.Fatalf("LoadConfig() after clearing error = %v", errClear)
		}
		if cleared.Claude.StreamBootstrapHold != nil || !cleared.Claude.StreamBootstrapHoldEnabled() {
			t.Fatalf("cleared StreamBootstrapHold = %v, want nil (default on)", cleared.Claude.StreamBootstrapHold)
		}
	})
}
