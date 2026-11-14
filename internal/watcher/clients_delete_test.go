package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAuthDeleteFileRemovalClassification(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	w := &Watcher{
		authDir:          dir,
		config:           &config.Config{AuthDir: dir},
		authQueue:        make(chan AuthUpdate, 8),
		pluginAuthParser: &fileDeleteTestParser{},
	}
	write := func(data string) {
		t.Helper()
		if errWriteFile := os.WriteFile(path, []byte(data), 0o600); errWriteFile != nil {
			t.Fatal(errWriteFile)
		}
		w.addOrUpdateClient(path)
	}
	assertDelete := func(id string, fileRemoval bool) {
		t.Helper()
		update := w.pendingUpdates[id]
		if update.Action != AuthUpdateActionDelete || update.Auth != nil || update.IsFileRemoval() != fileRemoval || update.Revision() == 0 {
			t.Fatalf("delete %s: got %+v, want file removal=%t", id, update, fileRemoval)
		}
		if w.currentAuths[id] != nil {
			t.Fatalf("deleted auth %s is still in watcher state", id)
		}
	}

	write(`{"type":"plugin","ids":["a","b"]}`)
	if !coreauth.IsPluginVirtualAuth(w.currentAuths["a"]) {
		t.Fatal("expected multi-auth synthesis to mark plugin virtual auths")
	}
	initialRevision := w.pendingUpdates["a"].Revision()
	// Removing one ID from an existing file is not a whole-file removal.
	write(`{"type":"plugin","ids":["b","c"]}`)
	assertDelete("a", false)
	if w.pendingUpdates["a"].Revision() <= initialRevision {
		t.Fatal("content delete did not advance the revision")
	}
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatalf("source file must still exist: %v", errStat)
	}

	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	w.removeClient(path)
	assertDelete("b", true)
	assertDelete("c", true)
	removalRevision := w.pendingUpdates["b"].Revision()

	// Re-create only B without a create event. Both snapshot APIs must read the
	// source file rather than the cleared watcher state or queued tombstones.
	if errWriteFile := os.WriteFile(path, []byte(`{"type":"plugin","ids":["b"]}`), 0o600); errWriteFile != nil {
		t.Fatal(errWriteFile)
	}
	fullSnapshot := w.SnapshotCoreAuths()
	if len(fullSnapshot) != 1 || fullSnapshot[0].ID != "b" || fullSnapshot[0].Attributes[coreauth.AttributePath] != path {
		t.Fatalf("expected only B from the re-created file, got %+v", fullSnapshot)
	}
	fileSnapshot, errSnapshotAuthFile := w.SnapshotAuthFile(path)
	if errSnapshotAuthFile != nil || len(fileSnapshot) != 1 || fileSnapshot[0].ID != "b" || fileSnapshot[0].Attributes[coreauth.AttributePath] != path {
		t.Fatalf("expected only B in the single-file snapshot, got %+v, err=%v", fileSnapshot, errSnapshotAuthFile)
	}

	// Pending updates are keyed by ID: a newer add replaces the marked delete,
	// and a later content delete must not inherit its file-removal mark.
	write(`{"type":"plugin","ids":["a","b"]}`)
	addition := w.pendingUpdates["b"]
	if addition.Action != AuthUpdateActionAdd || addition.IsFileRemoval() || addition.Revision() <= removalRevision {
		t.Fatalf("expected newer unmarked add, got %+v", addition)
	}
	write(`{"type":"plugin","ids":["a"]}`)
	assertDelete("b", false)
}

func TestAuthDeleteFullScanIsNotFileRemoval(t *testing.T) {
	for _, filePresent := range []bool{true, false} {
		name := "file_absent"
		if filePresent {
			name = "file_present"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "accounts.json")
			if filePresent {
				if errWriteFile := os.WriteFile(path, []byte(`{"type":"plugin"}`), 0o600); errWriteFile != nil {
					t.Fatal(errWriteFile)
				}
			}
			auth := &coreauth.Auth{ID: "virtual", Attributes: map[string]string{coreauth.AttributePath: path}}
			coreauth.MarkPluginVirtualAuth(auth, path, 0)
			w := &Watcher{currentAuths: map[string]*coreauth.Auth{auth.ID: auth}, authQueue: make(chan AuthUpdate, 1)}
			updates := w.prepareAuthUpdatesLocked(nil, false)
			if len(updates) != 1 || updates[0].Action != AuthUpdateActionDelete || updates[0].IsFileRemoval() || updates[0].Auth != nil {
				t.Fatalf("expected unmarked full-scan delete, got %+v", updates)
			}
		})
	}
}

type fileDeleteTestParser struct {
	err error
}

func (p *fileDeleteTestParser) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	auths, handled, errParseAuths := p.ParseAuths(ctx, req)
	if errParseAuths != nil || len(auths) == 0 {
		return nil, handled, errParseAuths
	}
	return auths[0], handled, nil
}

func (p *fileDeleteTestParser) ParseAuths(_ context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	if p.err != nil {
		return nil, true, p.err
	}
	var payload struct {
		IDs []string `json:"ids"`
	}
	if errUnmarshal := json.Unmarshal(req.RawJSON, &payload); errUnmarshal != nil {
		return nil, true, errUnmarshal
	}
	auths := make([]*coreauth.Auth, 0, len(payload.IDs))
	for _, id := range payload.IDs {
		auths = append(auths, &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive})
	}
	return auths, true, nil
}
