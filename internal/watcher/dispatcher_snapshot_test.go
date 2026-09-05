package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// pauseAuthSnapshot suspends a real scan after reading files, before publishing them.
func pauseAuthSnapshot(t *testing.T, w *Watcher, force bool) func() {
	t.Helper()
	originalSnapshot := snapshotCoreAuthsFunc
	scanned := make(chan struct{})
	resume := make(chan struct{})
	finished := make(chan struct{})
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) []*coreauth.Auth {
		auths := originalSnapshot(cfg, authDir, parser)
		close(scanned)
		<-resume
		return auths
	}
	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			close(resume)
			<-finished
			snapshotCoreAuthsFunc = originalSnapshot
		})
	}
	t.Cleanup(finish)
	go func() {
		defer close(finished)
		w.refreshAuthState(force)
	}()
	<-scanned
	return finish
}

func TestRefreshAuthStatePreservesConcurrentFileLifecycle(t *testing.T) {
	for _, action := range []string{"add", "delete", "persisted"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			otherPath := filepath.Join(dir, "other.json")
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}}
			if action != "add" {
				if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
			}
			if errWrite := os.WriteFile(otherPath, []byte(`{"type":"codex","note":"old"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w.addOrUpdateClient(otherPath)
			w.authQueue = make(chan AuthUpdate, 10)
			// The full scan must still publish changes to unrelated files.
			if errWrite := os.WriteFile(otherPath, []byte(`{"type":"codex","note":"scanned"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			finishScan := pauseAuthSnapshot(t, w, true)
			if action == "delete" {
				if errRemove := os.Remove(path); errRemove != nil {
					t.Fatal(errRemove)
				}
				w.removeClient(path)
			} else {
				if errWrite := os.WriteFile(path, []byte(`{"type":"codex","proxy_url":"http://new.proxy:8080"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				if action == "persisted" {
					auths := snapshotCoreAuths(w.config, dir, nil)
					for _, auth := range auths {
						if auth.ID == "account.json" {
							w.dispatchPersistedAuthUpdate(AuthUpdate{Action: AuthUpdateActionModify, ID: auth.ID, Auth: auth})
						}
					}
				} else {
					w.addOrUpdateClient(path)
				}
			}
			finishScan()
			current := w.currentAuths["account.json"]
			pending := w.pendingUpdates["account.json"]
			if action == "delete" {
				if current != nil || pending.Action != AuthUpdateActionDelete {
					t.Fatalf("scan resurrected deleted auth: current=%#v, action=%s", current, pending.Action)
				}
			} else if current == nil || current.ProxyURL != "http://new.proxy:8080" || pending.Auth == nil || pending.Auth.ProxyURL != "http://new.proxy:8080" {
				t.Fatalf("scan lost concurrent %s: current=%#v, pending=%#v", action, current, pending.Auth)
			}
			other := w.currentAuths["other.json"]
			if other == nil || other.Metadata["note"] != "scanned" {
				t.Fatalf("scan discarded unrelated file change: %#v", other)
			}
			otherUpdate := w.pendingUpdates["other.json"]
			if otherUpdate.Auth == nil || otherUpdate.Auth.Metadata["note"] != "scanned" {
				t.Fatalf("missing unrelated file update: %#v", otherUpdate.Auth)
			}
		})
	}
}

func TestRefreshAuthStatePreservesConcurrentRoundTrip(t *testing.T) {
	for _, initiallyPresent := range []bool{true, false} {
		name := "add_then_delete"
		if initiallyPresent {
			name = "change_then_restore"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}}
			if initiallyPresent {
				if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
			}
			w.authQueue = make(chan AuthUpdate, 10)
			originalSnapshot := snapshotCoreAuthsFunc
			t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })
			snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) []*coreauth.Auth {
				// Deliver two events during the scan, ending at the starting content.
				if errWrite := os.WriteFile(path, []byte(`{"type":"codex","proxy_url":"http://intermediate.proxy:8080"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
				auths := originalSnapshot(cfg, authDir, parser)
				if initiallyPresent {
					if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
					w.addOrUpdateClient(path)
				} else {
					if errRemove := os.Remove(path); errRemove != nil {
						t.Fatal(errRemove)
					}
					w.removeClient(path)
				}
				return auths
			}
			w.refreshAuthState(false)
			current := w.currentAuths["account.json"]
			pending := w.pendingUpdates["account.json"]
			if initiallyPresent {
				if current == nil || current.ProxyURL != "" || pending.Auth == nil || pending.Auth.ProxyURL != "" {
					t.Fatalf("scan restored intermediate proxy: current=%#v, pending=%#v", current, pending.Auth)
				}
			} else if current != nil || pending.Action != AuthUpdateActionDelete {
				t.Fatalf("scan resurrected transient auth: current=%#v, action=%s", current, pending.Action)
			}
		})
	}
}

func TestRefreshAuthStatePreservesUnchangedFileObservation(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		formatted bool
		event     bool
	}{
		{name: "semantic_equal", formatted: true},
		{name: "hash_equal"},
		{name: "event_semantic_equal", formatted: true, event: true},
		{name: "event_hash_equal", event: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			original := []byte(`{"type":"codex","proxy_url":"http://latest.proxy:8080"}`)
			if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 10)}
			w.addOrUpdateClient(path)
			// The intermediate content is seen only by the full scan, never by an event.
			if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			originalSnapshot := snapshotCoreAuthsFunc
			t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })
			snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) []*coreauth.Auth {
				auths := originalSnapshot(cfg, authDir, parser)
				latest := original
				if testCase.formatted {
					latest = []byte(`{ "type": "codex", "proxy_url": "http://latest.proxy:8080" }`)
				}
				if errWrite := os.WriteFile(path, latest, 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				if testCase.event {
					w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
				} else {
					w.addOrUpdateClient(path)
				}
				return auths
			}
			w.refreshAuthState(false)
			current := w.currentAuths["account.json"]
			if current == nil || current.ProxyURL != "http://latest.proxy:8080" {
				t.Fatal("scan overrode newer unchanged-file observation")
			}
			batch, okBatch := w.nextPendingBatch(context.Background())
			if !okBatch || len(batch) != 1 || batch[0].Auth == nil || batch[0].Auth.ProxyURL != "http://latest.proxy:8080" {
				t.Fatal("unchanged-file observation lost the valid pending auth update")
			}
		})
	}
}

func TestRefreshAuthStateAppliesHashCachedFileObservation(t *testing.T) {
	for _, phase := range []string{"during_scan", "after_scan"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 10)}
			w.addOrUpdateClient(path)
			if errWrite := os.WriteFile(path, []byte(`{"type":"codex","proxy_url":"http://latest.proxy:8080"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			originalSnapshot := snapshotCoreAuthsFunc
			t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })
			snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) []*coreauth.Auth {
				auths := originalSnapshot(cfg, authDir, parser)
				// reloadClients cached the new hash before publishing its matching auth.
				if phase == "after_scan" {
					// Suspend the event after observation, before content processing.
					w.observeAuthFile(path)
				} else {
					w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
				}
				return auths
			}
			w.reloadClients(true, nil, false)
			if phase == "after_scan" {
				w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
			}
			current := w.currentAuths["account.json"]
			pending := w.pendingUpdates["account.json"]
			if current == nil || current.ProxyURL != "http://latest.proxy:8080" || pending.Auth == nil || pending.Auth.ProxyURL != "http://latest.proxy:8080" {
				t.Fatal("hash-cache warmup suppressed the newer auth update")
			}
		})
	}
}

func TestRefreshAuthStateDoesNotResurrectUnregisteredFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 10)}
	originalSnapshot := snapshotCoreAuthsFunc
	t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) []*coreauth.Auth {
		auths := originalSnapshot(cfg, authDir, parser)
		if errRemove := os.Remove(path); errRemove != nil {
			t.Fatal(errRemove)
		}
		w.removeClient(path)
		return auths
	}
	w.refreshAuthState(false)
	if w.currentAuths["account.json"] != nil || len(w.pendingOrder) != 0 {
		t.Fatal("scan registered a file already observed as deleted")
	}
}

func TestDispatchAuthUpdatesRejectsDelayedScan(t *testing.T) {
	for _, action := range []string{"modify", "delete", "already_dispatched"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			if errWrite := os.WriteFile(path, []byte(`{"type":"codex","proxy_url":"http://old.proxy:8080"}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}}
			w.addOrUpdateClient(path)
			w.authQueue = make(chan AuthUpdate, 10)
			// Suspend the full-scan pipeline between its state commit and dispatch.
			w.clientsMutex.Lock()
			delayed := w.prepareAuthUpdatesLocked([]*coreauth.Auth{w.currentAuths["account.json"].Clone()}, true)
			w.clientsMutex.Unlock()
			if action == "delete" {
				if errRemove := os.Remove(path); errRemove != nil {
					t.Fatal(errRemove)
				}
				w.removeClient(path)
			} else {
				if errWrite := os.WriteFile(path, []byte(`{"type":"codex","proxy_url":"http://latest.proxy:8080"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
				if action == "already_dispatched" {
					batch, okBatch := w.nextPendingBatch(context.Background())
					if !okBatch || len(batch) != 1 || batch[0].Auth.ProxyURL != "http://latest.proxy:8080" {
						t.Fatal("missing latest update in dispatched batch")
					}
				}
			}
			w.dispatchAuthUpdates(delayed)
			pending := w.pendingUpdates["account.json"]
			switch action {
			case "delete":
				if pending.Action != AuthUpdateActionDelete {
					t.Fatalf("delayed scan replaced deletion with %s", pending.Action)
				}
			case "already_dispatched":
				if len(w.pendingOrder) != 0 {
					t.Fatal("delayed scan was enqueued after the latest update was dispatched")
				}
			default:
				if pending.Auth == nil || pending.Auth.ProxyURL != "http://latest.proxy:8080" {
					t.Fatalf("delayed scan replaced newer queued proxy: %#v", pending.Auth)
				}
			}
		})
	}
}

func TestRefreshAuthStatePreservesConcurrentProxyUpdate(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		oldProxy string
		newProxy string
	}{
		{name: "add", newProxy: "http://new.proxy:8080"},
		{name: "change", oldProxy: "http://old.proxy:8080", newProxy: "http://new.proxy:8080"},
		{name: "clear", oldProxy: "http://old.proxy:8080"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			store := sdkAuth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			initial := &coreauth.Auth{
				ID: "account.json", FileName: "account.json", Provider: "codex",
				Status: coreauth.StatusActive, ProxyURL: testCase.oldProxy,
				Metadata: map[string]any{"type": "codex", "access_token": "old-token", "note": "keep"},
			}
			if testCase.oldProxy != "" {
				initial.Metadata["proxy_url"] = testCase.oldProxy
			}
			registered, errRegister := manager.Register(context.Background(), initial)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}}
			w.addOrUpdateClient(path)
			w.authQueue = make(chan AuthUpdate, 10)

			finishScan := pauseAuthSnapshot(t, w, false)

			// Match the management fields handler's runtime update and file persistence.
			patched := registered.Clone()
			patched.ProxyURL = testCase.newProxy
			if testCase.newProxy == "" {
				delete(patched.Metadata, "proxy_url")
			} else {
				patched.Metadata["proxy_url"] = testCase.newProxy
			}
			updated, errUpdate := manager.Update(context.Background(), patched)
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			w.addOrUpdateClient(path)
			finishScan()

			if updated.Generation <= registered.Generation || updated.RegistrationEpoch != registered.RegistrationEpoch {
				t.Fatalf("unexpected patch version: generation %d -> %d, epoch %d -> %d", registered.Generation, updated.Generation, registered.RegistrationEpoch, updated.RegistrationEpoch)
			}
			if got := w.currentAuths[initial.ID].ProxyURL; got != testCase.newProxy {
				t.Errorf("watcher proxy_url = %q, want %q after concurrent patch", got, testCase.newProxy)
			}
			// Consume the final queued snapshot as the service does, without writing it back yet.
			pending, okPending := w.pendingUpdates[initial.ID]
			if !okPending || pending.Auth == nil {
				t.Fatal("missing queued auth update")
			}
			if _, errApply := manager.Update(coreauth.WithSkipPersist(context.Background()), pending.Auth); errApply != nil {
				t.Fatal(errApply)
			}
			base, _ := manager.GetByID(initial.ID)
			refreshed := base.Clone()
			refreshed.Metadata["access_token"] = "new-token"
			if _, errRefresh := manager.UpdateRefreshedAuth(context.Background(), base, refreshed); errRefresh != nil {
				t.Fatal(errRefresh)
			}
			current, _ := manager.GetByID(initial.ID)
			if current.ProxyURL != testCase.newProxy {
				t.Errorf("runtime proxy_url = %q, want %q after refresh", current.ProxyURL, testCase.newProxy)
			}
			raw, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			var metadata map[string]any
			if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			proxy, _ := metadata["proxy_url"].(string)
			if proxy != testCase.newProxy {
				t.Errorf("persisted proxy_url = %q, want %q after refresh", proxy, testCase.newProxy)
			}
			if metadata["access_token"] != "new-token" || metadata["note"] != "keep" {
				t.Errorf("refresh lost token or unrelated metadata: %#v", metadata)
			}
		})
	}
}
