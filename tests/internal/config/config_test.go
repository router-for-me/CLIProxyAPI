package config_test

import (
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestZhipuConfigParse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `
port: 8317
zhipu-api-key:
  - api-key: "glmsk-abc123"
    base-url: "https://open.bigmodel.cn/api/paas/v4"
    proxy-url: "socks5://127.0.0.1:1080"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := appconfig.LoadConfigOptional(cfgPath, true)
	if err != nil {
		t.Fatalf("LoadConfigOptional returned error: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected config, got nil")
	}
	if len(cfg.ZhipuKey) != 1 {
		t.Fatalf("expected 1 zhipu key entry, got %d", len(cfg.ZhipuKey))
	}
	zk := cfg.ZhipuKey[0]
	if zk.APIKey == "" {
		t.Errorf("expected api-key parsed, got empty")
	}
	if zk.BaseURL == "" {
		t.Errorf("expected base-url parsed, got empty")
	}
	if zk.ProxyURL == "" {
		t.Errorf("expected proxy-url parsed, got empty")
	}
}
