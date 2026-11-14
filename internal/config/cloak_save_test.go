package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveConfigPreserveComments_ClaudeCloakUpdates(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `claude-api-key:
  - api-key: sk-ant-test
    cloak:
      mode: always
      strict-mode: true
      cache-user-id: true
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	// Test case 1: Disable strict-mode, clear mode, remove cache-user-id.
	cfg := &Config{
		ClaudeKey: []ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Cloak: &CloakConfig{
					Mode:        "",
					StrictMode:  false,
					CacheUserID: nil,
				},
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	// In issue #5935, mode: always, strict-mode: true, and cache-user-id: true remained in config.yaml
	// even when strict-mode was set to false, mode was cleared, and cache-user-id was disabled.
	if strings.Contains(savedText, "mode: always") {
		t.Errorf("saved YAML still contains 'mode: always' after clearing mode; got:\n%s", savedText)
	}
	if strings.Contains(savedText, "strict-mode: true") {
		t.Errorf("saved YAML still contains 'strict-mode: true' after setting strict-mode to false; got:\n%s", savedText)
	}
	if strings.Contains(savedText, "cache-user-id: true") {
		t.Errorf("saved YAML still contains 'cache-user-id: true' after disabling cache-user-id; got:\n%s", savedText)
	}

	// Loading the saved config must reflect the updated false/empty values, while preserving explicit Cloak.
	loadedCfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if len(loadedCfg.ClaudeKey) == 0 {
		t.Fatalf("loaded ClaudeKey is empty")
	}
	cloak := loadedCfg.ClaudeKey[0].Cloak
	if cloak == nil {
		t.Fatalf("loaded Cloak is nil, want non-nil explicit cloak config")
	}
	if cloak.Mode != "" {
		t.Errorf("loaded Cloak.Mode = %q, want empty", cloak.Mode)
	}
	if cloak.StrictMode {
		t.Errorf("loaded Cloak.StrictMode = true, want false")
	}
	if cloak.CacheUserID != nil && *cloak.CacheUserID {
		t.Errorf("loaded Cloak.CacheUserID = true, want nil or false")
	}
}

func TestSaveConfigPreserveComments_ClaudeCloakExplicitFalse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `claude-api-key:
  - api-key: sk-ant-test
    cloak:
      mode: always
      strict-mode: true
      cache-user-id: true
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cacheFalse := false
	cfg := &Config{
		ClaudeKey: []ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Cloak: &CloakConfig{
					Mode:        "auto",
					StrictMode:  false,
					CacheUserID: &cacheFalse,
				},
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	if !strings.Contains(savedText, "mode: auto") {
		t.Errorf("saved YAML missing 'mode: auto'; got:\n%s", savedText)
	}
	if strings.Contains(savedText, "strict-mode: true") {
		t.Errorf("saved YAML still contains 'strict-mode: true'; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "cache-user-id: false") {
		t.Errorf("saved YAML missing explicit 'cache-user-id: false'; got:\n%s", savedText)
	}

	loadedCfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	cloak := loadedCfg.ClaudeKey[0].Cloak
	if cloak == nil {
		t.Fatalf("loaded Cloak is nil, want non-nil")
	}
	if cloak.Mode != "auto" {
		t.Errorf("loaded Cloak.Mode = %q, want %q", cloak.Mode, "auto")
	}
	if cloak.StrictMode {
		t.Errorf("loaded Cloak.StrictMode = true, want false")
	}
	if cloak.CacheUserID == nil || *cloak.CacheUserID != false {
		t.Errorf("loaded Cloak.CacheUserID = %v, want false", cloak.CacheUserID)
	}
}

func TestSaveConfigPreserveComments_ClaudeCloakExplicitFalseWhenFieldPreviouslyAbsent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	// Initial YAML only has mode: always. cache-user-id is completely absent.
	initialYAML := `claude-api-key:
  - api-key: sk-ant-test
    cloak:
      mode: always
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cacheFalse := false
	cfg := &Config{
		ClaudeKey: []ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Cloak: &CloakConfig{
					Mode:        "always",
					CacheUserID: &cacheFalse,
				},
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	// An explicit pointer false for cache-user-id must be added to YAML even if previously absent.
	if !strings.Contains(savedText, "cache-user-id: false") {
		t.Errorf("saved YAML missing newly-added 'cache-user-id: false'; got:\n%s", savedText)
	}

	loadedCfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	cloak := loadedCfg.ClaudeKey[0].Cloak
	if cloak == nil {
		t.Fatalf("loaded Cloak is nil, want non-nil")
	}
	if cloak.CacheUserID == nil || *cloak.CacheUserID != false {
		t.Errorf("loaded Cloak.CacheUserID = %v, want explicit false", cloak.CacheUserID)
	}
}

func TestSaveConfigPreserveComments_ClaudeCloakRemovedWhenNil(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `claude-api-key:
  - api-key: sk-ant-test
    cloak:
      mode: always
      strict-mode: true
      cache-user-id: true
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	// Cloak explicitly set to nil (removed)
	cfg := &Config{
		ClaudeKey: []ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Cloak:  nil,
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	if strings.Contains(savedText, "cloak:") {
		t.Errorf("saved YAML still contains 'cloak:' after nil removal; got:\n%s", savedText)
	}

	loadedCfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if loadedCfg.ClaudeKey[0].Cloak != nil {
		t.Errorf("loaded Cloak = %+v, want nil", loadedCfg.ClaudeKey[0].Cloak)
	}
}

func TestSaveConfigPreserveComments_HeadersPrunedAndNonTargetMappingsPreserved(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `remote-management:
  allow-remote: true
claude-api-key:
  - api-key: sk-ant-test
    headers:
      Custom-A: valA
      Custom-B: valB
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &Config{
		RemoteManagement: RemoteManagement{
			AllowRemote: true,
		},
		ClaudeKey: []ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Headers: map[string]string{
					"Custom-A": "valA",
				},
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	// Deleted header Custom-B must be pruned from claude-api-key.headers
	if strings.Contains(savedText, "Custom-B") {
		t.Errorf("saved YAML still contains deleted header 'Custom-B'; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "Custom-A: valA") {
		t.Errorf("saved YAML missing retained header 'Custom-A: valA'; got:\n%s", savedText)
	}

	// Non-target root mapping (remote-management) must be preserved
	if !strings.Contains(savedText, "allow-remote: true") {
		t.Errorf("saved YAML lost 'allow-remote: true'; got:\n%s", savedText)
	}
}
