package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// Anthropic-compatible executors (zhipu/minimax) must be pre-registered so that the
// manager reports missing credentials rather than executor gaps when no auth exists.
func TestBuilderSeedsZhipuExecutor(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("port: 53355\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{
		Port:    53355,
		AuthDir: filepath.Join(tmp, "auth"),
	}

	svc, err := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(cfgPath).
		Build()
	if err != nil {
		t.Fatalf("build service: %v", err)
	}

	manager := svc.coreManager
	if manager == nil {
		t.Fatalf("core manager not initialized")
	}

	providers := []string{"zhipu"}
	req := cliproxyexecutor.Request{Model: "glm-4.6"}
	_, execErr := manager.Execute(context.Background(), providers, req, cliproxyexecutor.Options{})
	if execErr == nil {
		t.Fatalf("expected error due to missing auth")
	}
	authErr, ok := execErr.(*coreauth.Error)
	if !ok {
		t.Fatalf("expected *coreauth.Error, got %T", execErr)
	}
	if authErr.Code != "auth_not_found" {
		t.Fatalf("expected auth_not_found when no Zhipu credentials are configured, got %q", authErr.Code)
	}

	// MiniMax Anthropic-compatible path should behave the same way.
	providers = []string{"minimax"}
	req = cliproxyexecutor.Request{Model: "MiniMax-M2"}
	_, execErr = manager.Execute(context.Background(), providers, req, cliproxyexecutor.Options{})
	if execErr == nil {
		t.Fatalf("expected error due to missing auth (minimax)")
	}
	authErr, ok = execErr.(*coreauth.Error)
	if !ok {
		t.Fatalf("expected *coreauth.Error for minimax, got %T", execErr)
	}
	if authErr.Code != "auth_not_found" {
		t.Fatalf("expected auth_not_found when no MiniMax credentials are configured, got %q", authErr.Code)
	}
}
