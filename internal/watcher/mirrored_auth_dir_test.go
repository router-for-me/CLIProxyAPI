package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestStartUsesMirroredAuthDirForWatching(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	mirroredAuthDir := filepath.Join(tmpDir, "mirrored-auth")
	if err := os.MkdirAll(mirroredAuthDir, 0o755); err != nil {
		t.Fatalf("failed to create mirrored auth dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("auth_dir: "+mirroredAuthDir+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	w, err := NewWatcher(configPath, filepath.Join(tmpDir, "missing-auth"), nil)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer w.Stop()
	w.mirroredAuthDir = mirroredAuthDir
	w.SetConfig(&config.Config{AuthDir: mirroredAuthDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := w.Start(ctx); err != nil {
		t.Fatalf("expected Start to watch mirrored auth dir, got error: %v", err)
	}
}

func TestHandleEventUsesMirroredAuthDir(t *testing.T) {
	tmpDir := t.TempDir()
	originalAuthDir := filepath.Join(tmpDir, "auth")
	mirroredAuthDir := filepath.Join(tmpDir, "mirror")
	if err := os.MkdirAll(mirroredAuthDir, 0o755); err != nil {
		t.Fatalf("failed to create mirrored auth dir: %v", err)
	}
	authFile := filepath.Join(mirroredAuthDir, "demo.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo","email":"demo@example.com"}`), 0o644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	w := &Watcher{
		authDir:         originalAuthDir,
		mirroredAuthDir: mirroredAuthDir,
		lastAuthHashes:  make(map[string]string),
		fileAuthsByPath: make(map[string]map[string]*coreauth.Auth),
	}
	w.SetConfig(&config.Config{AuthDir: mirroredAuthDir})

	w.handleEvent(fsnotify.Event{Name: authFile, Op: fsnotify.Write})

	normalized := w.normalizeAuthPath(authFile)
	w.clientsMutex.RLock()
	_, ok := w.lastAuthHashes[normalized]
	w.clientsMutex.RUnlock()
	if !ok {
		t.Fatal("expected mirrored auth file event to update watcher state")
	}
}

func TestRefreshAuthStateUsesMirroredAuthDir(t *testing.T) {
	oldSnapshot := snapshotCoreAuthsFunc
	defer func() { snapshotCoreAuthsFunc = oldSnapshot }()

	mirroredAuthDir := t.TempDir()
	calledDir := ""
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string) []*coreauth.Auth {
		calledDir = authDir
		return nil
	}

	w := &Watcher{authDir: filepath.Join(t.TempDir(), "auth")}
	w.mirroredAuthDir = mirroredAuthDir
	w.SetConfig(&config.Config{AuthDir: mirroredAuthDir})

	w.refreshAuthState(false)

	if calledDir != mirroredAuthDir {
		t.Fatalf("expected refreshAuthState to use mirrored auth dir %s, got %s", mirroredAuthDir, calledDir)
	}
}

func TestSnapshotCoreAuthsUsesMirroredAuthDir(t *testing.T) {
	originalAuthDir := filepath.Join(t.TempDir(), "original-auth")
	if err := os.MkdirAll(originalAuthDir, 0o755); err != nil {
		t.Fatalf("failed to create original auth dir: %v", err)
	}
	mirroredAuthDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mirroredAuthDir, "demo.json"), []byte(`{"type":"demo","email":"mirror@example.com"}`), 0o644); err != nil {
		t.Fatalf("failed to write mirrored auth file: %v", err)
	}

	w := &Watcher{
		authDir:         originalAuthDir,
		mirroredAuthDir: mirroredAuthDir,
	}
	w.SetConfig(&config.Config{AuthDir: originalAuthDir})

	auths := w.SnapshotCoreAuths()
	if len(auths) != 1 {
		t.Fatalf("expected mirrored auth dir to provide 1 auth, got %d", len(auths))
	}
	if auths[0] == nil || auths[0].Provider != "demo" {
		t.Fatalf("unexpected auths from mirrored dir: %+v", auths)
	}
}

func TestReloadClientsUsesMirroredAuthDirForFileAuthCache(t *testing.T) {
	originalAuthDir := filepath.Join(t.TempDir(), "original-auth")
	if err := os.MkdirAll(originalAuthDir, 0o755); err != nil {
		t.Fatalf("failed to create original auth dir: %v", err)
	}
	mirroredAuthDir := t.TempDir()
	authFile := filepath.Join(mirroredAuthDir, "demo.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"demo","email":"mirror@example.com"}`), 0o644); err != nil {
		t.Fatalf("failed to write mirrored auth file: %v", err)
	}

	w := &Watcher{
		authDir:          originalAuthDir,
		mirroredAuthDir:  mirroredAuthDir,
		lastAuthHashes:   make(map[string]string),
		lastAuthContents: make(map[string]*coreauth.Auth),
		fileAuthsByPath:  make(map[string]map[string]*coreauth.Auth),
	}
	w.SetConfig(&config.Config{AuthDir: originalAuthDir})

	w.reloadClients(true, nil, false)

	normalized := w.normalizeAuthPath(authFile)
	w.clientsMutex.RLock()
	if len(w.fileAuthsByPath[normalized]) == 0 {
		w.clientsMutex.RUnlock()
		t.Fatalf("expected mirrored auth file %s to populate fileAuthsByPath", authFile)
	}
	if len(w.currentAuths) == 0 {
		w.clientsMutex.RUnlock()
		t.Fatal("expected mirrored auth file to populate currentAuths")
	}
	w.clientsMutex.RUnlock()

	w.removeClient(authFile)

	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	if _, ok := w.fileAuthsByPath[normalized]; ok {
		t.Fatalf("expected mirrored auth file %s to be removed from fileAuthsByPath", authFile)
	}
	if len(w.currentAuths) != 0 {
		t.Fatalf("expected currentAuths to be cleared after mirrored auth removal, got %d entries", len(w.currentAuths))
	}
}

func TestCanonicalizeMirroredSynthesizedAuths_PreservesMirrorStoreIDFormat(t *testing.T) {
	w := &Watcher{
		authDir:         filepath.Join(t.TempDir(), "configured-auth"),
		mirroredAuthDir: t.TempDir(),
	}

	authPath := filepath.Join(w.mirroredAuthDir, "Team", "Auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatalf("failed to create mirrored auth path: %v", err)
	}

	auths := []*coreauth.Auth{
		{
			ID:       `team\auth.json`,
			Provider: "gemini-cli",
			Attributes: map[string]string{
				"path":                   authPath,
				"gemini_virtual_parent":  `team\auth.json`,
				"gemini_virtual_project": "proj-a",
			},
			Metadata: map[string]any{
				"virtual_parent_id": `team\auth.json`,
			},
		},
		{
			ID:       `team\auth.json::proj-a`,
			Provider: "gemini-cli",
			Attributes: map[string]string{
				"path":                   authPath,
				"gemini_virtual_parent":  `team\auth.json`,
				"gemini_virtual_project": "proj-a",
			},
			Metadata: map[string]any{
				"virtual_parent_id": `team\auth.json`,
			},
		},
	}

	w.canonicalizeMirroredSynthesizedAuths(auths)

	wantPrimary := "Team/Auth.json"
	if got := auths[0].ID; got != wantPrimary {
		t.Fatalf("primary auth ID = %q, want %q", got, wantPrimary)
	}
	if got := auths[1].ID; got != wantPrimary+"::proj-a" {
		t.Fatalf("virtual auth ID = %q, want %q", got, wantPrimary+"::proj-a")
	}
	if got := auths[1].Attributes["gemini_virtual_parent"]; got != wantPrimary {
		t.Fatalf("virtual parent attribute = %q, want %q", got, wantPrimary)
	}
	if got, _ := auths[1].Metadata["virtual_parent_id"].(string); got != wantPrimary {
		t.Fatalf("virtual parent metadata = %q, want %q", got, wantPrimary)
	}
}
