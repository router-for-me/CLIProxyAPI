package cliproxy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRun_FailsWhenUsagePersistenceInitFails(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "postgres://invalid dsn")
	t.Setenv("pgstore_dsn", "")
	t.Setenv("PGSTORE_SCHEMA", "")
	t.Setenv("pgstore_schema", "")

	tmpDir := t.TempDir()
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		AuthDir:                 filepath.Join(tmpDir, "auth"),
		UsagePersistenceEnabled: true,
		RemoteManagement: config.RemoteManagement{
			DisableControlPanel: true,
		},
	}

	service := &Service{cfg: cfg, configPath: filepath.Join(tmpDir, "config.yaml")}
	err := service.Run(context.Background())
	if err == nil {
		t.Fatalf("expected run to fail when usage persistence init fails")
	}
	if !strings.Contains(err.Error(), "initialize usage persistence") {
		t.Fatalf("unexpected error: %v", err)
	}
}
