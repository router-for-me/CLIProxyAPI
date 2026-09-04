package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mixedAPIKeysConfig = `# Top comment
port: 8317

# API keys for authentication
api-keys:
  # first key
  - "plain-key"
  - key: "named-key"
    name: "alice"
  - "second-plain"
`

func TestSaveConfigPreserveCommentsKeepsMixedAPIKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(mixedAPIKeysConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := []APIKeyEntry{
		{Key: "plain-key"},
		{Key: "named-key", Name: "alice"},
		{Key: "second-plain"},
	}
	for i := range want {
		if cfg.APIKeys[i] != want[i] {
			t.Fatalf("loaded entry[%d] = %#v, want %#v", i, cfg.APIKeys[i], want[i])
		}
	}

	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatalf("save config: %v", errSave)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(saved)
	if !strings.Contains(text, "# Top comment") || !strings.Contains(text, "# API keys for authentication") || !strings.Contains(text, "# first key") {
		t.Fatalf("comments not preserved:\n%s", text)
	}
	if !strings.Contains(text, `- "plain-key"`) || !strings.Contains(text, `- "second-plain"`) {
		t.Fatalf("scalar entries not preserved:\n%s", text)
	}
	if !strings.Contains(text, "name: alice") && !strings.Contains(text, `name: "alice"`) {
		t.Fatalf("named entry name not preserved:\n%s", text)
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(reloaded.APIKeys) != len(want) {
		t.Fatalf("reloaded entries = %#v, want %#v", reloaded.APIKeys, want)
	}
	for i := range want {
		if reloaded.APIKeys[i] != want[i] {
			t.Fatalf("reloaded entry[%d] = %#v, want %#v", i, reloaded.APIKeys[i], want[i])
		}
	}
}
