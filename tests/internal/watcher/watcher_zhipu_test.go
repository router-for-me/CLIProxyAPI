package watcher_test

import (
	"path/filepath"
	"testing"

	appconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher"
)

func TestSnapshotCoreAuths_ZhipuAPIKey(t *testing.T) {
	cfg := &appconfig.Config{}
	cfg.ZhipuKey = []appconfig.ZhipuKey{{
		APIKey:  "glmsk-123",
		BaseURL: "https://example.zhipu/api/paas/v4",
		ProxyURL: "socks5://127.0.0.1:1080",
	}}

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
		if a == nil || a.Provider != "zhipu" {
			continue
		}
		found = true
		if a.Attributes == nil || a.Attributes["api_key"] == "" {
			t.Errorf("expected api_key attribute present for zhipu auth")
		}
		if a.Attributes["base_url"] == "" {
			t.Errorf("expected base_url attribute present for zhipu auth")
		}
		if a.ProxyURL == "" {
			t.Errorf("expected ProxyURL propagated for zhipu auth")
		}
	}
	if !found {
		t.Fatalf("expected a zhipu provider auth synthesized")
	}
}
