package cliproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRun_FallsBackWhenUsagePersistencePostgresInitFails(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "postgres://invalid dsn")
	t.Setenv("PGSTORE_SCHEMA", "")

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

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	service := &Service{cfg: cfg, configPath: configPath}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	err := service.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected run to stop by context cancellation, got %v", err)
	}
}
