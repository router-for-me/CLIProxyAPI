package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestWatcherDoesNotFollowDirectorySymlinksCreatedAfterStart(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	externalDir := filepath.Join(tmp, "external")
	for _, dir := range []string{authDir, externalDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updates := make(chan AuthUpdate, 16)
	watcher, err := NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	watcher.SetConfig(&config.Config{AuthDir: authDir})
	watcher.SetAuthUpdateQueue(updates)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = watcher.Stop()
		watcher.stopDispatch()
	})
	if err := watcher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(authDir, "shared")
	if err := os.Symlink(externalDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	// A later event in the same root confirms the symlink's Create was processed.
	sentinel := filepath.Join(authDir, "ready.json")
	if err := os.WriteFile(sentinel, codexWatcherAuth(t, "ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	pending := make(map[string][]AuthUpdate)
	receiveAuthUpdate(t, updates, "ready.json", pending)
	assertAuthDirectoryNotWatched(t, watcher, linkedDir)
	assertAuthDirectoryNotWatched(t, watcher, externalDir)
	if watcher.isWatchedAuthDirectory(linkedDir) {
		t.Fatal("symlinked directory entered the auth watch registry")
	}
}
