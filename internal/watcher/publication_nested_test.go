package watcher

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestWatcherPreservesNestedAuthWhenDirectoryUnreadable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "auths")
	path := filepath.Join(dir, "team", "account.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"committed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id := filepath.Join("team", "account.json")
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 1)}
	w.refreshAuthState(false)
	if update, ok := takePublicationUpdate(w, id); !ok || update.Auth == nil {
		t.Fatal("initial scan did not load nested credential")
	}
	// A non-directory mount target fails traversal even when tests run as root.
	if err := os.Rename(dir, filepath.Join(root, "unavailable-auths")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("temporarily unavailable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadDir(dir); err == nil || os.IsNotExist(err) {
		t.Fatalf("fixture did not make directory traversal fail: %v", err)
	}
	w.refreshAuthState(true)
	if update, ok := takePublicationUpdate(w, id); ok && update.Action == AuthUpdateActionDelete {
		t.Fatal("unreadable parent directory deleted a nested credential")
	}
	if w.currentAuths[id] == nil {
		t.Fatal("incomplete scan discarded the nested runtime credential")
	}
}

func TestWatcherScansSymlinkAuthDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	alias := filepath.Join(root, "auths")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "account.json"), []byte(`{"type":"codex","access_token":"committed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{authDir: alias, config: &config.Config{AuthDir: alias}, authQueue: make(chan AuthUpdate, 1)}
	w.refreshAuthState(false)
	if update, ok := takePublicationUpdate(w, "account.json"); !ok || update.Auth == nil || update.Auth.Metadata["access_token"] != "committed" {
		t.Fatal("scan lost credential under a symlinked auth directory")
	}
}
