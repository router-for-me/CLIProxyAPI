package cliproxy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConfigureCodexSyncWorkerHonorsIntegrationFlags(t *testing.T) {
	service := &Service{}
	integration := config.DefaultCodexIntegrationConfig()
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{CodexIntegration: integration},
		Host:      "127.0.0.1",
		Port:      8317,
	}
	service.configureCodexSyncWorker(context.Background(), cfg)
	if service.codexSyncCancel != nil {
		t.Fatal("disabled integration started auto-sync")
	}

	cfg.CodexIntegration.Enabled = true
	cfg.CodexIntegration.AutoSync = false
	service.configureCodexSyncWorker(context.Background(), cfg)
	if service.codexSyncCancel != nil {
		t.Fatal("auto-sync=false started auto-sync")
	}

	cfg.CodexIntegration.AutoSync = true
	cfg.CodexIntegration.CodexHome = filepath.Join(t.TempDir(), "codex-home")
	service.configureCodexSyncWorker(context.Background(), cfg)
	if service.codexSyncCancel == nil {
		t.Fatal("enabled auto-sync did not start worker")
	}
	service.configureCodexSyncWorker(context.Background(), nil)
	if service.codexSyncCancel != nil {
		t.Fatal("disabling integration did not stop worker")
	}
}
