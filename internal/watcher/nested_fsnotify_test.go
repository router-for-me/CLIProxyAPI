package watcher

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestWatcherProcessesChangesToNestedAuthFiles(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	nestedDir := filepath.Join(authDir, "team")
	if err := os.MkdirAll(nestedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nestedDir, "account.json")
	if err := os.WriteFile(path, codexWatcherAuth(t, "initial"), 0o600); err != nil {
		t.Fatal(err)
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

	id := filepath.Join("team", "account.json")
	pending := make(map[string][]AuthUpdate)
	initial := receiveAuthUpdate(t, updates, id, pending)
	if initial.Action != AuthUpdateActionAdd || initial.Auth == nil || initial.Auth.Metadata["access_token"] != "initial" {
		t.Fatalf("initial nested auth = %#v, want add with the nested credential", initial)
	}

	if err := os.WriteFile(path, codexWatcherAuth(t, "updated"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := receiveAuthUpdate(t, updates, id, pending)
	if updated.Action != AuthUpdateActionModify || updated.Auth == nil || updated.Auth.Metadata["access_token"] != "updated" {
		t.Fatalf("updated nested auth = %#v, want modify with the new credential", updated)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	removed := receiveAuthUpdate(t, updates, id, pending)
	if removed.Action != AuthUpdateActionDelete || removed.ID != id || !removed.IsFileRemoval() {
		t.Fatalf("removed nested auth = %#v, want file deletion", removed)
	}
}

func TestWatcherKeepsRootJSONEligibleWithoutDirectoryWatchMetadata(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(authDir, "account.json")
	if err := os.WriteFile(path, codexWatcherAuth(t, "root"), 0o600); err != nil {
		t.Fatal(err)
	}
	updates := make(chan AuthUpdate, 1)
	watcher, err := NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	watcher.SetConfig(&config.Config{AuthDir: authDir})
	watcher.SetAuthUpdateQueue(updates)
	t.Cleanup(func() {
		_ = watcher.Stop()
		watcher.stopDispatch()
	})
	if len(watcher.watchedAuthDirs) != 0 {
		t.Fatal("fixture unexpectedly has directory watch metadata")
	}

	watcher.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
	update := receiveAuthUpdate(t, updates, "account.json", make(map[string][]AuthUpdate))
	if update.Action != AuthUpdateActionAdd || update.Auth == nil || update.Auth.Metadata["access_token"] != "root" {
		t.Fatalf("root auth update = %#v, want add with its credential", update)
	}
}

func TestWatcherWatchesCreatedAuthDirectoriesAndRemovesDeletedTrees(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
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
	pending := make(map[string][]AuthUpdate)

	nestedDir := filepath.Join(authDir, "team")
	if err := os.Mkdir(nestedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nestedDir, "account.json")
	if err := os.WriteFile(path, codexWatcherAuth(t, "created"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := filepath.Join("team", "account.json")
	created := receiveAuthUpdate(t, updates, id, pending)
	if created.Action != AuthUpdateActionAdd || created.Auth == nil || created.Auth.Metadata["access_token"] != "created" {
		t.Fatalf("created nested auth = %#v, want add with its credential", created)
	}
	assertAuthDirectoryWatched(t, watcher, nestedDir)

	if err := os.WriteFile(path, codexWatcherAuth(t, "updated"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := receiveAuthUpdate(t, updates, id, pending)
	if updated.Action != AuthUpdateActionModify || updated.Auth == nil || updated.Auth.Metadata["access_token"] != "updated" {
		t.Fatalf("updated nested auth = %#v, want modify with its credential", updated)
	}

	if err := os.RemoveAll(nestedDir); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(authDir, "ready-after-remove.json")
	if err := os.WriteFile(readyPath, codexWatcherAuth(t, "ready-after-remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready := receiveAuthUpdate(t, updates, "ready-after-remove.json", pending)
	if ready.Action != AuthUpdateActionAdd {
		t.Fatalf("ready auth after directory removal = %#v, want add", ready)
	}
	// Rescans can queue this nested deletion before the unrelated sentinel update.
	// Keep it so waiting for the sentinel first cannot consume and lose it.
	removed := receiveAuthUpdate(t, updates, id, pending)
	if removed.Action != AuthUpdateActionDelete {
		t.Fatalf("removed nested auth = %#v, want file deletion", removed)
	}
	assertAuthDirectoryNotWatched(t, watcher, nestedDir)
	if err := os.Mkdir(nestedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	recreatedPath := filepath.Join(nestedDir, "recreated.json")
	if err := os.WriteFile(recreatedPath, codexWatcherAuth(t, "recreated"), 0o600); err != nil {
		t.Fatal(err)
	}
	recreatedID := filepath.Join("team", "recreated.json")
	if update := receiveAuthUpdate(t, updates, recreatedID, pending); update.Action != AuthUpdateActionAdd {
		t.Fatalf("auth after directory recreate = %#v, want add", update)
	}
	assertAuthDirectoryWatched(t, watcher, nestedDir)
	if err := os.WriteFile(recreatedPath, codexWatcherAuth(t, "recreated-update"), 0o600); err != nil {
		t.Fatal(err)
	}
	if update := receiveAuthUpdate(t, updates, recreatedID, pending); update.Action != AuthUpdateActionModify {
		t.Fatalf("auth update under recreated directory = %#v, want modify", update)
	}
}

func TestWatcherFindsAuthFilesMovedIntoNestedDirectory(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	sourceDir := filepath.Join(tmp, "incoming")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
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
	pending := make(map[string][]AuthUpdate)

	sourcePath := filepath.Join(sourceDir, "account.json")
	if err := os.WriteFile(sourcePath, codexWatcherAuth(t, "moved"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondSourcePath := filepath.Join(sourceDir, "secondary.json")
	if err := os.WriteFile(secondSourcePath, codexWatcherAuth(t, "also-moved"), 0o600); err != nil {
		t.Fatal(err)
	}
	nestedDir := filepath.Join(authDir, "team")
	if err := os.Rename(sourceDir, nestedDir); err != nil {
		t.Fatal(err)
	}
	id := filepath.Join("team", "account.json")
	added := receiveAuthUpdate(t, updates, id, pending)
	if added.Action != AuthUpdateActionAdd || added.Auth == nil || added.Auth.Metadata["access_token"] != "moved" {
		t.Fatalf("moved auth = %#v, want add with its credential", added)
	}
	secondID := filepath.Join("team", "secondary.json")
	secondAdded := receiveAuthUpdate(t, updates, secondID, pending)
	if secondAdded.Action != AuthUpdateActionAdd || secondAdded.Auth == nil || secondAdded.Auth.Metadata["access_token"] != "also-moved" {
		t.Fatalf("second moved auth = %#v, want add with its credential", secondAdded)
	}
	assertAuthDirectoryWatched(t, watcher, nestedDir)
	if err := os.WriteFile(filepath.Join(nestedDir, "account.json"), codexWatcherAuth(t, "moved-update"), 0o600); err != nil {
		t.Fatal(err)
	}
	if update := receiveAuthUpdate(t, updates, id, pending); update.Action != AuthUpdateActionModify || update.Auth == nil || update.Auth.Metadata["access_token"] != "moved-update" {
		t.Fatalf("updated moved auth = %#v, want modify with its credential", update)
	}

	removedDir := filepath.Join(tmp, "archived")
	if err := os.Rename(nestedDir, removedDir); err != nil {
		t.Fatal(err)
	}
	deleted := receiveAuthUpdate(t, updates, id, pending)
	if deleted.Action != AuthUpdateActionDelete {
		t.Fatalf("auth moved out of its watched tree = %#v, want file deletion", deleted)
	}
	secondDeleted := receiveAuthUpdate(t, updates, secondID, pending)
	if secondDeleted.Action != AuthUpdateActionDelete {
		t.Fatalf("second auth moved out of its watched tree = %#v, want file deletion", secondDeleted)
	}
	assertAuthDirectoryNotWatched(t, watcher, nestedDir)
}

func TestWatcherDoesNotWatchPrivateAuthDirectories(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	nestedDir := filepath.Join(authDir, "team")
	privateDirs := []string{
		filepath.Join(nestedDir, ".git"),
		filepath.Join(nestedDir, ".auth-acquisition-orphan"),
		filepath.Join(nestedDir, ".auth-publications"),
	}
	for _, dir := range privateDirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	watcher.SetConfig(&config.Config{AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = watcher.Stop()
	})
	if err := watcher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, dir := range privateDirs {
		assertAuthDirectoryNotWatched(t, watcher, dir)
	}
}

func TestWatcherPrivateDirectoryMatchingFollowsAuthDiscoveryOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("auth discovery's private path checks are case-sensitive on Linux")
	}
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	uppercaseDirs := []string{
		filepath.Join(authDir, "team", ".GIT"),
		filepath.Join(authDir, "team", ".AUTH-PUBLICATIONS"),
		filepath.Join(authDir, "team", ".AUTH-ACQUISITION-orphan"),
	}
	for _, dir := range uppercaseDirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "account.json"), codexWatcherAuth(t, "visible"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := authpublication.Files(authDir)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	watcher.SetConfig(&config.Config{AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = watcher.Stop()
	})
	if err := watcher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, dir := range uppercaseDirs {
		path := filepath.Join(dir, "account.json")
		if !containsNormalizedPath(paths, path) {
			t.Fatalf("auth discovery excluded %s on Linux", path)
		}
		if isPrivateAuthPath(authDir, dir) {
			t.Fatalf("watcher marked discoverable auth directory private: %s", dir)
		}
		assertAuthDirectoryWatched(t, watcher, dir)
	}
}

func TestWatcherRewatchesRecreatedDirectoryAfterQueuedRemoval(t *testing.T) {
	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auth")
	oldDir := filepath.Join(authDir, "team")
	oldPath := filepath.Join(oldDir, "old.json")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, codexWatcherAuth(t, "old"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewWatcher(configPath, authDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watcher.Stop() })
	watcher.SetConfig(&config.Config{AuthDir: authDir})
	if err := watcher.watchAuthDirectoryTree(authDir); err != nil {
		t.Fatal(err)
	}
	watcher.reloadClients(true, nil, false)

	if err := os.RemoveAll(oldDir); err != nil {
		t.Fatal(err)
	}
	newNestedDir := filepath.Join(oldDir, "project")
	newPath := filepath.Join(newNestedDir, "new.json")
	if err := os.MkdirAll(newNestedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, codexWatcherAuth(t, "new"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Deliver the old inode's queued removal only after a replacement tree exists.
	watcher.handleEvent(fsnotify.Event{Name: oldDir, Op: fsnotify.Remove})
	assertAuthDirectoryWatched(t, watcher, oldDir)
	assertAuthDirectoryWatched(t, watcher, newNestedDir)
	if !watcher.isKnownAuthFile(newPath) {
		t.Fatalf("replacement auth file %s was not picked up by the removal rescan", newPath)
	}
}

func containsNormalizedPath(paths []string, want string) bool {
	want = filepath.Clean(want)
	for _, path := range paths {
		if filepath.Clean(path) == want {
			return true
		}
	}
	return false
}

func codexWatcherAuth(t *testing.T, accessToken string) []byte {
	t.Helper()
	doc := ojson.NewDocString(`{"type":"codex","email":"watcher@example.com"}`)
	if err := doc.Set("access_token", accessToken); err != nil {
		t.Fatal(err)
	}
	return doc.Bytes()
}

func receiveAuthUpdate(t *testing.T, updates <-chan AuthUpdate, id string, pending map[string][]AuthUpdate) AuthUpdate {
	t.Helper()
	if queued := pending[id]; len(queued) > 0 {
		update := queued[0]
		pending[id] = queued[1:]
		return update
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case update := <-updates:
			updateID := update.ID
			if update.Auth != nil && update.Auth.ID != "" {
				updateID = update.Auth.ID
			}
			if updateID == id {
				return update
			}
			// Rescans can queue another file's deletion before the requested sentinel.
			pending[updateID] = append(pending[updateID], update)
		case <-timer.C:
			t.Fatalf("timed out waiting for auth update for %s", id)
		}
	}
}

func assertAuthDirectoryWatched(t *testing.T, watcher *Watcher, path string) {
	t.Helper()
	for _, watched := range watcher.watcher.WatchList() {
		if watcher.normalizeAuthPath(watched) == watcher.normalizeAuthPath(path) {
			return
		}
	}
	t.Fatalf("nested auth directory %s is not watched", path)
}

func assertAuthDirectoryNotWatched(t *testing.T, watcher *Watcher, path string) {
	t.Helper()
	for _, watched := range watcher.watcher.WatchList() {
		if watcher.normalizeAuthPath(watched) == watcher.normalizeAuthPath(path) {
			t.Fatalf("private or removed auth directory %s remains watched", path)
		}
	}
}
