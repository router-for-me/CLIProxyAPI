package watcher_test

import (
	"path/filepath"
	"testing"

	appconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher"
)

func TestSnapshotCoreAuths_PackycodeProvider(t *testing.T) {
	cfg := &appconfig.Config{}
	cfg.Packycode.Enabled = true
	cfg.Packycode.BaseURL = "https://codex-api.packycode.com/v1"
	cfg.Packycode.RequiresOpenAIAuth = true
	cfg.Packycode.Credentials.OpenAIAPIKey = "sk-openai-123"

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	authDir := t.TempDir()
	w, err := watcher.NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatalf("NewWatcher error: %v", err)
	}
	w.SetConfig(cfg)
	auths := w.SnapshotCoreAuths()
	if len(auths) == 0 {
		t.Fatalf("expected synthesized auths, got none")
	}
	found := false
	for _, a := range auths {
		if a == nil || a.Provider != "packycode" { // 期待对外 provider 为 packycode
			continue
		}
		found = true
		if a.Label != "packycode" {
			t.Errorf("expected label=packycode, got %q", a.Label)
		}
		if a.Attributes == nil || a.Attributes["base_url"] == "" {
			t.Errorf("expected base_url attribute present for packycode auth")
		}
		if src := a.Attributes["source"]; src != "config:packycode" {
			t.Errorf("expected source=config:packycode, got %q", src)
		}
	}
	if !found {
		t.Fatalf("expected a packycode provider auth synthesized")
	}
}
