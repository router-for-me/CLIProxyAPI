package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// pauseAuthSnapshot suspends a real scan after reading files, before publishing them.
func pauseAuthSnapshot(t *testing.T, w *Watcher, force bool) func() {
	t.Helper()
	originalSnapshot := snapshotCoreAuthsFunc
	scanned := make(chan struct{})
	resume := make(chan struct{})
	finished := make(chan struct{})
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
		auths, incomplete := originalSnapshot(cfg, authDir, parser)
		close(scanned)
		<-resume
		return auths, incomplete
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
			snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
				// Deliver two events during the scan, ending at the starting content.
				if errWrite := os.WriteFile(path, []byte(`{"type":"codex","proxy_url":"http://intermediate.proxy:8080"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
				auths, incomplete := originalSnapshot(cfg, authDir, parser)
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
				return auths, incomplete
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
			snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
				auths, incomplete := originalSnapshot(cfg, authDir, parser)
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
				return auths, incomplete
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
			snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
				auths, incomplete := originalSnapshot(cfg, authDir, parser)
				// reloadClients cached the new hash before publishing its matching auth.
				if phase == "after_scan" {
					// Suspend the event after observation, before content processing.
					w.observeAuthFile(path)
				} else {
					w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
				}
				return auths, incomplete
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
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
		auths, incomplete := originalSnapshot(cfg, authDir, parser)
		if errRemove := os.Remove(path); errRemove != nil {
			t.Fatal(errRemove)
		}
		w.removeClient(path)
		return auths, incomplete
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

type snapshotTestPluginParser func(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error)

func (f snapshotTestPluginParser) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return f(ctx, req)
}

func TestWatcherFileSnapshotReformatAndUnmappedChangeDoNotDropUpdate(t *testing.T) {
	t.Run("reformatted_before_consume", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "account.json")
		if errWrite := os.WriteFile(path, []byte(`{"type":"codex","prefix":"pfx-initial"}`), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 10)}
		w.addOrUpdateClient(path)
		initBatch, okInit := w.nextPendingBatch(context.Background())
		if !okInit || len(initBatch) != 1 {
			t.Fatalf("expected 1 initial update, got %v", initBatch)
		}
		ctx := coreauth.WithSkipPersist(context.Background())
		manager := coreauth.NewManager(nil, nil, nil)
		if _, errRegister := manager.Register(ctx, initBatch[0].Auth); errRegister != nil {
			t.Fatal(errRegister)
		}

		// Step 1: Edit file to B; Watcher queues snapshot B.
		if errWrite := os.WriteFile(path, []byte(`{"type":"codex","prefix":"pfx-edited"}`), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w.addOrUpdateClient(path)
		batchB, okB := w.nextPendingBatch(context.Background())
		if !okB || len(batchB) != 1 {
			t.Fatalf("expected 1 update for B, got %v", batchB)
		}

		// Step 2: Before B is consumed by manager.Update, reformat file on disk with
		// whitespace and reversed key order (B'). Watcher deduplicates B'.
		if errWrite := os.WriteFile(path, []byte("{\n  \"prefix\": \"pfx-edited\",\n  \"type\": \"codex\"\n}\n"), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w.addOrUpdateClient(path)
		if len(w.pendingOrder) != 0 {
			t.Fatalf("expected reformatted B' to be deduplicated, got pending=%v", w.pendingOrder)
		}

		// Step 3: Consume B into manager.Update; canonical digest matches B' on disk.
		if _, errUpdate := manager.Update(ctx, batchB[0].Auth); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		current, _ := manager.GetByID("account.json")
		if current == nil || current.Prefix != "pfx-edited" {
			t.Fatalf("reformatted file caused edit to be dropped: got=%#v, want prefix=pfx-edited", current)
		}
	})

	t.Run("plugin_unmapped_field_change_before_consume", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "plugin.json")
		parser := snapshotTestPluginParser(func(_ context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
			return &coreauth.Auth{
				ID:       req.FileName,
				Provider: "gemini-cli",
				Metadata: map[string]any{"type": "gemini-cli"},
			}, true, nil
		})
		if errWrite := os.WriteFile(path, []byte(`{"type":"gemini-cli","prefix":"pfx-initial","ignored":"v0"}`), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, pluginAuthParser: parser, authQueue: make(chan AuthUpdate, 10)}
		w.addOrUpdateClient(path)
		initBatch, okInit := w.nextPendingBatch(context.Background())
		if !okInit || len(initBatch) != 1 {
			t.Fatalf("expected 1 initial update, got %v", initBatch)
		}
		ctx := coreauth.WithSkipPersist(context.Background())
		manager := coreauth.NewManager(nil, nil, nil)
		if _, errRegister := manager.Register(ctx, initBatch[0].Auth); errRegister != nil {
			t.Fatal(errRegister)
		}

		// Step 1: Edit file to B (prefix=pfx-edited, ignored=v1) and pop batchB.
		if errWrite := os.WriteFile(path, []byte(`{"type":"gemini-cli","prefix":"pfx-edited","ignored":"v1"}`), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w.addOrUpdateClient(path)
		batchB, okB := w.nextPendingBatch(context.Background())
		if !okB || len(batchB) != 1 {
			t.Fatalf("expected 1 update for B, got %v", batchB)
		}

		// Step 2: Before batchB is consumed, change only the unmapped JSON field (ignored=v2).
		// Because the file snapshot digest changed, Watcher must emit a follow-up update.
		if errWrite := os.WriteFile(path, []byte(`{"type":"gemini-cli","prefix":"pfx-edited","ignored":"v2"}`), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w.addOrUpdateClient(path)
		batchB2, okB2 := w.nextPendingBatch(context.Background())
		if !okB2 || len(batchB2) != 1 {
			t.Fatalf("expected follow-up update when file snapshot digest changed, got %v", batchB2)
		}

		// Step 3: Applying batchB (superseded on disk) followed by batchB2 commits pfx-edited.
		if _, errUpdate := manager.Update(ctx, batchB[0].Auth); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		if _, errUpdate := manager.Update(ctx, batchB2[0].Auth); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		current, _ := manager.GetByID("plugin.json")
		if current == nil || current.Prefix != "pfx-edited" {
			t.Fatalf("unmapped field change caused edit to be dropped: got=%#v, want prefix=pfx-edited", current)
		}
	})
}

func TestRefreshAuthStatePreservesIncompleteFileDuringInPlaceWrite(t *testing.T) {
	for _, tc := range []struct {
		name       string
		midContent string
		fullReload bool
	}{
		{name: "truncated_empty_refresh", midContent: ""},
		{name: "partial_json_refresh", midContent: `{"type":"plugin","ids":["b"`},
		{name: "truncated_empty_reload_clients", midContent: "", fullReload: true},
		{name: "partial_json_reload_clients", midContent: `{"type":"plugin","ids":["b"`, fullReload: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "accounts.json")
			w := &Watcher{
				authDir:          dir,
				config:           &config.Config{AuthDir: dir},
				authQueue:        make(chan AuthUpdate, 16),
				pluginAuthParser: &fileDeleteTestParser{},
			}
			if errWrite := os.WriteFile(path, []byte(`{"type":"plugin","ids":["a","b"]}`), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w.addOrUpdateClient(path)
			if _, okBatch := w.nextPendingBatch(context.Background()); !okBatch {
				t.Fatal("expected initial add batch")
			}

			// Simulate an in-place writer opening with O_WRONLY|O_TRUNC and optionally flushing partial JSON.
			file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
			if errOpen != nil {
				t.Fatal(errOpen)
			}
			if tc.midContent != "" {
				if _, errWrite := file.WriteString(tc.midContent); errWrite != nil {
					_ = file.Close()
					t.Fatal(errWrite)
				}
			}

			if tc.fullReload {
				w.reloadClients(true, nil, false)
			} else {
				w.refreshAuthState(false)
			}

			if w.currentAuths["a"] == nil || w.currentAuths["b"] == nil {
				_ = file.Close()
				t.Fatalf("full scan unregistered credentials during in-place write: currentAuths=%#v, pending=%#v", w.currentAuths, w.pendingUpdates)
			}
			if len(w.pendingOrder) != 0 {
				_ = file.Close()
				t.Fatalf("expected no queued deletes during in-place write, got %#v", w.pendingUpdates)
			}

			// Finish the in-place write with ["b","c"] (dropping "a" and adding "c").
			if _, errSeek := file.Seek(0, 0); errSeek != nil {
				_ = file.Close()
				t.Fatal(errSeek)
			}
			if errTrunc := file.Truncate(0); errTrunc != nil {
				_ = file.Close()
				t.Fatal(errTrunc)
			}
			if _, errWrite := file.WriteString(`{"type":"plugin","ids":["b","c"]}`); errWrite != nil {
				_ = file.Close()
				t.Fatal(errWrite)
			}
			if errClose := file.Close(); errClose != nil {
				t.Fatal(errClose)
			}

			w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})

			if w.currentAuths["a"] != nil {
				t.Fatalf("expected writer's final event to remove dropped child a, got %#v", w.currentAuths["a"])
			}
			if w.currentAuths["b"] == nil || w.currentAuths["c"] == nil {
				t.Fatalf("expected b and c in currentAuths after write finished, got %#v", w.currentAuths)
			}
			if gotDelete := w.pendingUpdates["a"]; gotDelete.Action != AuthUpdateActionDelete {
				t.Fatalf("expected delete update for dropped child a, got %+v", gotDelete)
			}
			if gotAdd := w.pendingUpdates["c"]; gotAdd.Action != AuthUpdateActionAdd {
				t.Fatalf("expected add update for new child c, got %+v", gotAdd)
			}
		})
	}

	t.Run("reload_clients_pass_divergence_retains_file_auth_index", func(t *testing.T) {
		for _, mode := range []string{"without_early_event", "with_early_truncated_write_event", "final_write_event_during_scan"} {
			t.Run(mode, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "accounts.json")
				w := &Watcher{
					authDir:          dir,
					config:           &config.Config{AuthDir: dir},
					authQueue:        make(chan AuthUpdate, 16),
					pluginAuthParser: &fileDeleteTestParser{},
				}
				if errWrite := os.WriteFile(path, []byte(`{"type":"plugin","ids":["a","b"]}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
				if _, okBatch := w.nextPendingBatch(context.Background()); !okBatch {
					t.Fatal("expected initial add batch")
				}

				// reloadClients pass 1 reads complete ["b","c"], then an in-place write truncates
				// the file right before refreshAuthState runs snapshotCoreAuthsFunc.
				if errWrite := os.WriteFile(path, []byte(`{"type":"plugin","ids":["b","c"]}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				originalSnapshot := snapshotCoreAuthsFunc
				t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })
				var file *os.File
				snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
					var errOpen error
					file, errOpen = os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
					if errOpen != nil {
						t.Fatal(errOpen)
					}
					auths, incomplete := originalSnapshot(cfg, authDir, parser)
					switch mode {
					case "with_early_truncated_write_event":
						// An early write event for the truncated file increments fileObservations
						// during the scan but returns early without updating fileAuthsByPath.
						w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
					case "final_write_event_during_scan":
						if _, errWrite := file.WriteString(`{"type":"plugin","ids":["b","c"]}`); errWrite != nil {
							_ = file.Close()
							t.Fatal(errWrite)
						}
						if errClose := file.Close(); errClose != nil {
							t.Fatal(errClose)
						}
						file = nil
						w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
					}
					return auths, incomplete
				}

				w.reloadClients(true, nil, false)
				snapshotCoreAuthsFunc = originalSnapshot

				if file != nil {
					if w.currentAuths["a"] == nil || w.currentAuths["b"] == nil {
						_ = file.Close()
						t.Fatalf("expected a and b to survive incomplete refresh scan, got %#v", w.currentAuths)
					}

					if _, errWrite := file.WriteString(`{"type":"plugin","ids":["b","c"]}`); errWrite != nil {
						_ = file.Close()
						t.Fatal(errWrite)
					}
					if errClose := file.Close(); errClose != nil {
						t.Fatal(errClose)
					}

					w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Write})
				}

				if w.currentAuths["a"] != nil {
					t.Fatalf("expected writer's final event to remove retained child a, got %#v", w.currentAuths["a"])
				}
				if w.currentAuths["b"] == nil || w.currentAuths["c"] == nil {
					t.Fatalf("expected b and c in currentAuths after write finished, got %#v", w.currentAuths)
				}
				if gotDelete := w.pendingUpdates["a"]; gotDelete.Action != AuthUpdateActionDelete {
					t.Fatalf("expected delete update for dropped child a, got %+v", gotDelete)
				}
				if gotAdd := w.pendingUpdates["c"]; gotAdd.Action != AuthUpdateActionAdd {
					t.Fatalf("expected add update for new child c, got %+v", gotAdd)
				}
			})
		}
	})

	t.Run("affected_oauth_provider_reload_during_in_place_write", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "account.json")
		w := &Watcher{
			authDir:   dir,
			config:    &config.Config{AuthDir: dir},
			authQueue: make(chan AuthUpdate, 16),
		}
		if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w.addOrUpdateClient(path)
		if _, okBatch := w.nextPendingBatch(context.Background()); !okBatch {
			t.Fatal("expected initial add batch")
		}

		file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		_ = file.Close()

		w.reloadClients(false, []string{"codex"}, false)

		if w.currentAuths["account.json"] == nil {
			t.Fatalf("expected codex auth to survive incomplete scan during provider reload, got %#v", w.currentAuths)
		}
		if errRemove := os.Remove(path); errRemove != nil {
			t.Fatal(errRemove)
		}
		w.removeClient(path)
		if gotDelete := w.pendingUpdates["account.json"]; gotDelete.Action != AuthUpdateActionDelete {
			t.Fatalf("expected removeClient to emit delete after incomplete provider reload, got %+v", gotDelete)
		}
	})

	t.Run("affected_oauth_provider_reload_does_not_resurrect_deleted_auth", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "account.json")
		w := &Watcher{
			authDir:   dir,
			config:    &config.Config{AuthDir: dir},
			authQueue: make(chan AuthUpdate, 16),
		}
		if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		w.addOrUpdateClient(path)
		w.dispatchRuntimeAuthUpdate(AuthUpdate{
			Action: AuthUpdateActionAdd,
			ID:     "ws-codex",
			Auth:   &coreauth.Auth{ID: "ws-codex", Provider: "codex", Status: coreauth.StatusActive},
		})
		if _, okBatch := w.nextPendingBatch(context.Background()); !okBatch {
			t.Fatal("expected initial add batch")
		}

		file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		_ = file.Close()

		originalSnapshot := snapshotCoreAuthsFunc
		t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })
		snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
			auths, incomplete := originalSnapshot(cfg, authDir, parser)
			w.dispatchRuntimeAuthUpdate(AuthUpdate{Action: AuthUpdateActionDelete, ID: "ws-codex"})
			if errRemove := os.Remove(path); errRemove != nil {
				t.Fatal(errRemove)
			}
			w.removeClient(path)
			return auths, incomplete
		}

		w.reloadClients(false, []string{"codex"}, false)

		if w.currentAuths["ws-codex"] != nil || w.pendingUpdates["ws-codex"].Action != AuthUpdateActionDelete {
			t.Fatalf("provider reload resurrected deleted runtime auth: current=%#v, pending=%+v", w.currentAuths["ws-codex"], w.pendingUpdates["ws-codex"])
		}
		if w.currentAuths["account.json"] != nil || w.pendingUpdates["account.json"].Action != AuthUpdateActionDelete {
			t.Fatalf("provider reload resurrected deleted file auth: current=%#v, pending=%+v", w.currentAuths["account.json"], w.pendingUpdates["account.json"])
		}
	})

	t.Run("affected_oauth_provider_reload_deletes_removed_or_zero_auth_file", func(t *testing.T) {
		for _, mode := range []string{"removed_file", "valid_empty_object"} {
			t.Run(mode, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "account.json")
				w := &Watcher{
					authDir:   dir,
					config:    &config.Config{AuthDir: dir},
					authQueue: make(chan AuthUpdate, 16),
				}
				if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
				if _, okBatch := w.nextPendingBatch(context.Background()); !okBatch {
					t.Fatal("expected initial add batch")
				}

				if mode == "removed_file" {
					if errRemove := os.Remove(path); errRemove != nil {
						t.Fatal(errRemove)
					}
				} else {
					if errWrite := os.WriteFile(path, []byte(`{}`), 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
				}

				w.reloadClients(false, []string{"codex"}, false)

				if w.currentAuths["account.json"] != nil {
					t.Fatalf("expected %s to remove codex auth during provider reload, got %#v", mode, w.currentAuths["account.json"])
				}
				if gotDelete := w.pendingUpdates["account.json"]; gotDelete.Action != AuthUpdateActionDelete {
					t.Fatalf("expected delete update for %s during provider reload, got %+v", mode, gotDelete)
				}
			})
		}
	})

	t.Run("control_valid_zero_auth_and_removed_file_still_delete", func(t *testing.T) {
		for _, mode := range []string{"valid_empty_object", "valid_plugin_empty", "removed_file"} {
			t.Run(mode, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "accounts.json")
				w := &Watcher{
					authDir:          dir,
					config:           &config.Config{AuthDir: dir},
					authQueue:        make(chan AuthUpdate, 16),
					pluginAuthParser: &fileDeleteTestParser{},
				}
				if errWrite := os.WriteFile(path, []byte(`{"type":"plugin","ids":["a"]}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				w.addOrUpdateClient(path)
				if _, okBatch := w.nextPendingBatch(context.Background()); !okBatch {
					t.Fatal("expected initial add batch")
				}

				switch mode {
				case "valid_empty_object":
					if errWrite := os.WriteFile(path, []byte(`{}`), 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
				case "valid_plugin_empty":
					if errWrite := os.WriteFile(path, []byte(`{"type":"plugin","ids":[]}`), 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
				case "removed_file":
					if errRemove := os.Remove(path); errRemove != nil {
						t.Fatal(errRemove)
					}
				}

				w.refreshAuthState(false)
				if w.currentAuths["a"] != nil {
					t.Fatalf("expected %s to remove auth a, got %#v", mode, w.currentAuths["a"])
				}
				if got := w.pendingUpdates["a"]; got.Action != AuthUpdateActionDelete {
					t.Fatalf("expected delete update for %s, got %+v", mode, got)
				}
			})
		}
	})
}
