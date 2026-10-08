package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Models for auths loaded from the auth directory must be registered before
// the HTTP listener opens, not only after the file watcher's first load.
func TestServiceRun_RegistersLoadedAuthModelsBeforeListening(t *testing.T) {
	authDir := t.TempDir()
	authJSON := []byte(`{"type":"codex","email":"startup@example.com","access_token":"test-access","expired":"2999-01-01T00:00:00Z"}`)
	if errWrite := os.WriteFile(filepath.Join(authDir, "codex-startup.json"), authJSON, 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient("codex-startup.json") })

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(cfgPath, []byte("{}"), 0o644); errWrite != nil {
		t.Fatalf("failed to write config: %v", errWrite)
	}
	cfg := &config.Config{Host: "127.0.0.1", Port: 0, AuthDir: authDir}

	modelsAtStart := make(chan int, 1)
	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(cfgPath).
		WithHooks(Hooks{OnBeforeStart: func(*config.Config) {
			modelsAtStart <- len(GlobalModelRegistry().GetModelsForClient("codex-startup.json"))
		}}).
		Build()
	if errBuild != nil {
		t.Fatalf("failed to build service: %v", errBuild)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- service.Run(ctx) }()

	select {
	case n := <-modelsAtStart:
		if n == 0 {
			t.Fatal("no models registered for the loaded auth before the listener started")
		}
	case errRun := <-runDone:
		cancel()
		t.Fatalf("service.Run returned before start: %v", errRun)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("timed out waiting for OnBeforeStart")
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("service.Run did not stop")
	}
}
