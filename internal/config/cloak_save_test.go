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

func TestSaveConfigPreserveComments_SensitiveWordsReplaceEmptyValueAndPruning(t *testing.T) {
	for _, v8Layout := range []bool{false, true} {
		name := "legacy"
		initialYAML := "port: 8317\nclaude-api-key:\n  - api-key: sk-ant-test\n"
		if v8Layout {
			name = "v8"
			initialYAML = "config-version: 8\nserver:\n  port: 8317\napi-keys:\n  claude:\n    - name: claude-1\n      keys:\n        - api-key: sk-ant-test\n"
		}
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
				t.Fatalf("os.WriteFile() error = %v", errWrite)
			}
			cfg, errLoad := LoadConfigOptional(configPath, false)
			if errLoad != nil {
				t.Fatalf("initial LoadConfigOptional() error = %v", errLoad)
			}

			// Step 1: Save empty-string deletion entries in all three scopes.
			cfg.Antigravity.SensitiveWordsReplace = map[string]string{
				"You are Codex, an agent based on GPT-5.": "",
				"TempAntigrav": "Keep",
			}
			cfg.Devin.SensitiveWordsReplace = map[string]string{
				"DropDevinPhrase": "",
			}
			cfg.ClaudeKey[0].Cloak = &CloakConfig{
				SensitiveWordsReplace: map[string]string{
					"DropClaudePhrase": "",
					"OldClaudeTarget":  "OldRepl",
				},
			}
			if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
				t.Fatalf("SaveConfigPreserveComments() step 1 error = %v", errSave)
			}
			reloaded, errReload := LoadConfigOptional(configPath, false)
			if errReload != nil {
				t.Fatalf("reload step 1 error = %v", errReload)
			}
			if val, ok := reloaded.Antigravity.SensitiveWordsReplace["You are Codex, an agent based on GPT-5."]; !ok || val != "" {
				t.Fatalf("Antigravity empty replacement lost: %#v", reloaded.Antigravity.SensitiveWordsReplace)
			}
			if val, ok := reloaded.Devin.SensitiveWordsReplace["DropDevinPhrase"]; !ok || val != "" {
				t.Fatalf("Devin empty replacement lost: %#v", reloaded.Devin.SensitiveWordsReplace)
			}
			if reloaded.ClaudeKey[0].Cloak == nil {
				t.Fatal("Claude Cloak is nil after step 1")
			}
			if val, ok := reloaded.ClaudeKey[0].Cloak.SensitiveWordsReplace["DropClaudePhrase"]; !ok || val != "" {
				t.Fatalf("Claude empty replacement lost: %#v", reloaded.ClaudeKey[0].Cloak.SensitiveWordsReplace)
			}

			// Step 2: Prune individual keys and clear Devin replacements completely.
			delete(reloaded.Antigravity.SensitiveWordsReplace, "TempAntigrav")
			reloaded.Devin.SensitiveWordsReplace = nil
			delete(reloaded.ClaudeKey[0].Cloak.SensitiveWordsReplace, "OldClaudeTarget")
			if errSave2 := SaveConfigPreserveComments(configPath, reloaded); errSave2 != nil {
				t.Fatalf("SaveConfigPreserveComments() step 2 error = %v", errSave2)
			}
			reloaded2, errReload2 := LoadConfigOptional(configPath, false)
			if errReload2 != nil {
				t.Fatalf("reload step 2 error = %v", errReload2)
			}
			if _, exists := reloaded2.Antigravity.SensitiveWordsReplace["TempAntigrav"]; exists {
				t.Fatalf("deleted Antigravity key persisted: %#v", reloaded2.Antigravity.SensitiveWordsReplace)
			}
			if len(reloaded2.Devin.SensitiveWordsReplace) != 0 {
				t.Fatalf("cleared Devin replacements persisted: %#v", reloaded2.Devin.SensitiveWordsReplace)
			}
			if _, exists := reloaded2.ClaudeKey[0].Cloak.SensitiveWordsReplace["OldClaudeTarget"]; exists {
				t.Fatalf("deleted Claude replacement key persisted: %#v", reloaded2.ClaudeKey[0].Cloak.SensitiveWordsReplace)
			}
		})
	}
}

func TestSaveConfigPreserveComments_SensitivePatternsReplaceEmptyValueAndPruning(t *testing.T) {
	for _, v8Layout := range []bool{false, true} {
		name := "legacy"
		initialYAML := "port: 8317\nclaude-api-key:\n  - api-key: sk-ant-test\n"
		if v8Layout {
			name = "v8"
			initialYAML = "config-version: 8\nserver:\n  port: 8317\napi-keys:\n  claude:\n    - name: claude-1\n      keys:\n        - api-key: sk-ant-test\n"
		}
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
				t.Fatalf("os.WriteFile() error = %v", errWrite)
			}
			cfg, errLoad := LoadConfigOptional(configPath, false)
			if errLoad != nil {
				t.Fatalf("initial LoadConfigOptional() error = %v", errLoad)
			}

			cfg.Antigravity.SensitivePatternsReplace = []SensitivePatternRule{
				{Pattern: `(?m)^RFC 2119[^\n]*\n?`, Replacement: ""},
			}
			cfg.Devin.SensitivePatternsReplace = []SensitivePatternRule{
				{Pattern: `(?m)^You are omp[^\n]*\n?`, Replacement: ""},
			}
			cfg.ClaudeKey[0].Cloak = &CloakConfig{
				SensitivePatternsReplace: []SensitivePatternRule{
					{Pattern: `(?s)<system-conventions>.*?</system-conventions>\n?`, Replacement: ""},
				},
			}
			if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
				t.Fatalf("SaveConfigPreserveComments() step 1 error = %v", errSave)
			}
			reloaded, errReload := LoadConfigOptional(configPath, false)
			if errReload != nil {
				t.Fatalf("reload step 1 error = %v", errReload)
			}
			if len(reloaded.Antigravity.SensitivePatternsReplace) != 1 || reloaded.Antigravity.SensitivePatternsReplace[0].Replacement != "" {
				t.Fatalf("Antigravity patterns = %#v", reloaded.Antigravity.SensitivePatternsReplace)
			}
			if len(reloaded.Devin.SensitivePatternsReplace) != 1 || reloaded.Devin.SensitivePatternsReplace[0].Replacement != "" {
				t.Fatalf("Devin patterns = %#v", reloaded.Devin.SensitivePatternsReplace)
			}
			if reloaded.ClaudeKey[0].Cloak == nil || len(reloaded.ClaudeKey[0].Cloak.SensitivePatternsReplace) != 1 {
				t.Fatalf("Claude patterns = %#v", reloaded.ClaudeKey[0].Cloak)
			}

			reloaded.Antigravity.SensitivePatternsReplace = nil
			reloaded.Devin.SensitivePatternsReplace = nil
			reloaded.ClaudeKey[0].Cloak.SensitivePatternsReplace = nil
			if errSave2 := SaveConfigPreserveComments(configPath, reloaded); errSave2 != nil {
				t.Fatalf("SaveConfigPreserveComments() step 2 error = %v", errSave2)
			}
			reloaded2, errReload2 := LoadConfigOptional(configPath, false)
			if errReload2 != nil {
				t.Fatalf("reload step 2 error = %v", errReload2)
			}
			if len(reloaded2.Antigravity.SensitivePatternsReplace) != 0 {
				t.Fatalf("cleared Antigravity patterns persisted: %#v", reloaded2.Antigravity.SensitivePatternsReplace)
			}
			if len(reloaded2.Devin.SensitivePatternsReplace) != 0 {
				t.Fatalf("cleared Devin patterns persisted: %#v", reloaded2.Devin.SensitivePatternsReplace)
			}
			if reloaded2.ClaudeKey[0].Cloak != nil && len(reloaded2.ClaudeKey[0].Cloak.SensitivePatternsReplace) != 0 {
				t.Fatalf("cleared Claude patterns persisted: %#v", reloaded2.ClaudeKey[0].Cloak.SensitivePatternsReplace)
			}
		})
	}
}

func TestSaveConfigPreserveComments_SensitivePatternsReplacePreservesComments(t *testing.T) {
	for _, v8Layout := range []bool{false, true} {
		name := "legacy"
		initialYAML := `port: 8317
antigravity:
  # keep section comment
  sensitive-patterns-replace:
    # keep rule comment
    - pattern: '(?m)^RFC 2119[^\n]*\n?' # keep pattern comment
      replacement: "" # keep replacement comment
`
		if v8Layout {
			name = "v8"
			initialYAML = `config-version: 8
server:
  port: 8317
oauth:
  providers:
    antigravity:
      # keep section comment
      sensitive-patterns-replace:
        # keep rule comment
        - pattern: '(?m)^RFC 2119[^\n]*\n?' # keep pattern comment
          replacement: "" # keep replacement comment
`
		}
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
				t.Fatalf("os.WriteFile() error = %v", errWrite)
			}
			cfg, errLoad := LoadConfigOptional(configPath, false)
			if errLoad != nil {
				t.Fatalf("LoadConfigOptional() error = %v", errLoad)
			}
			if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
				t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
			}
			savedBytes, errRead := os.ReadFile(configPath)
			if errRead != nil {
				t.Fatalf("os.ReadFile() error = %v", errRead)
			}
			saved := string(savedBytes)
			for _, comment := range []string{
				"# keep section comment",
				"# keep rule comment",
				"# keep pattern comment",
				"# keep replacement comment",
			} {
				if !strings.Contains(saved, comment) {
					t.Fatalf("saved YAML lost comment %q:\n%s", comment, saved)
				}
			}
		})
	}
}
