package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// An external edit is read and queued; when the queue applies it, an in-place rewrite of the
// file (O_TRUNC) is under way, so the manager discards the snapshot and the service tells the
// watcher (RereadDiscardedFileSnapshot). The rewrite ends with the edit's bytes, or the same
// content reformatted, so neither the hash of the file nor the snapshot's digest changed: the
// watcher must still resend the file and the manager must end with the edit.
func TestAddOrUpdateClientDiscardedSnapshotResentAfterInPlaceRewrite(t *testing.T) {
	const initial = `{"type":"codex","email":"a@b.c","disabled":false,"prefix":"pfx-0"}`
	const edit = `{"type":"codex","email":"a@b.c","disabled":false,"prefix":"pfx-1"}`
	for _, tc := range []struct {
		name, final string
		// eventBeforeReread processes the rewrite's file event before the service's call.
		eventBeforeReread bool
	}{
		{name: "identical bytes", final: edit},
		{name: "reformatted", final: "{\n \"prefix\": \"pfx-1\",\n \"disabled\": false,\n \"email\": \"a@b.c\",\n \"type\": \"codex\"\n}\n"},
		{name: "file event before the reread", final: edit, eventBeforeReread: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "acct.json")
			if errWrite := os.WriteFile(path, []byte(initial), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 1)}
			apply := func(update AuthUpdate) bool {
				t.Helper()
				before, _ := manager.GetByID("acct.json")
				if _, errUpdate := manager.Update(coreauth.WithSkipPersist(ctx), update.Auth); errUpdate != nil {
					t.Fatalf("update: %v", errUpdate)
				}
				after, _ := manager.GetByID("acct.json")
				return after.Generation > before.Generation
			}
			w.addOrUpdateClient(path)
			add, ok := takeQueuedAuthUpdate(w, "acct.json")
			if !ok {
				t.Fatal("initial read queued nothing")
			}
			if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), add.Auth); errRegister != nil {
				t.Fatalf("register: %v", errRegister)
			}

			if errWrite := os.WriteFile(path, []byte(edit), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w.addOrUpdateClient(path)
			snapshot, ok := takeQueuedAuthUpdate(w, "acct.json")
			if !ok {
				t.Fatal("the edit was not queued")
			}
			file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
			if errOpen != nil {
				t.Fatal(errOpen)
			}
			if apply(snapshot) {
				t.Fatal("snapshot applied while its file was being rewritten")
			}
			finish := func() {
				t.Helper()
				if _, errWrite := file.WriteString(tc.final); errWrite != nil {
					t.Fatal(errWrite)
				}
				if errClose := file.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				w.addOrUpdateClient(path)
			}
			if tc.eventBeforeReread {
				finish()
				if update, queued := takeQueuedAuthUpdate(w, "acct.json"); queued {
					t.Fatalf("the rewrite's file event queued %v; the case needs it suppressed", update.Action)
				}
			}
			w.RereadDiscardedFileSnapshot(snapshot.Auth)
			if !tc.eventBeforeReread {
				finish()
			}
			resent, ok := takeQueuedAuthUpdate(w, "acct.json")
			if !ok || !apply(resent) {
				t.Fatalf("after the rewrite queued=%v, want the file resent and applied", ok)
			}
			if got, _ := manager.GetByID("acct.json"); got.Prefix != "pfx-1" {
				t.Fatalf("runtime prefix = %q, want the edit's pfx-1 on disk", got.Prefix)
			}
			w.refreshAuthState(false)
			if update, queued := takeQueuedAuthUpdate(w, "acct.json"); queued {
				t.Fatalf("next scan queued %v for the auth", update.Action)
			}
		})
	}
}

func TestRereadDiscardedFileSnapshotPreservesKnownFileForRemoveEvent(t *testing.T) {
	const initial = `{"type":"codex","email":"a@b.c","disabled":false,"prefix":"pfx-0"}`
	const edit = `{"type":"codex","email":"a@b.c","disabled":false,"prefix":"pfx-1"}`

	ctx := coreauth.WithSkipPersist(context.Background())
	dir := t.TempDir()
	path := filepath.Join(dir, "acct.json")
	if errWrite := os.WriteFile(path, []byte(initial), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}, authQueue: make(chan AuthUpdate, 1)}

	w.addOrUpdateClient(path)
	add, ok := takeQueuedAuthUpdate(w, "acct.json")
	if !ok {
		t.Fatal("initial read queued nothing")
	}
	if _, errRegister := manager.Register(ctx, add.Auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}

	if errWrite := os.WriteFile(path, []byte(edit), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w.addOrUpdateClient(path)
	snapshot, ok := takeQueuedAuthUpdate(w, "acct.json")
	if !ok {
		t.Fatal("the edit was not queued")
	}

	// Remove the file before the queued snapshot is applied so the digest fence discards it.
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	updated, errUpdate := manager.Update(ctx, snapshot.Auth)
	if errUpdate != nil {
		t.Fatalf("update discarded snapshot: %v", errUpdate)
	}
	if updated.Generation != 1 {
		t.Fatalf("expected discarded snapshot to leave Generation at 1, got %d", updated.Generation)
	}

	w.RereadDiscardedFileSnapshot(snapshot.Auth)

	// A subsequent fsnotify.Remove event must still recognize the path as a known auth file.
	w.handleEvent(fsnotify.Event{Name: path, Op: fsnotify.Remove})
	deleteUpdate, ok := takeQueuedAuthUpdate(w, "acct.json")
	if !ok || deleteUpdate.Action != AuthUpdateActionDelete || deleteUpdate.ID != "acct.json" {
		t.Fatalf("delete update = (%+v, %v), want delete for acct.json", deleteUpdate, ok)
	}
}
