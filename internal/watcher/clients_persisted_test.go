package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type pausedAuthParser struct {
	read   chan struct{}
	resume chan struct{}
}

func (p *pausedAuthParser) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	close(p.read)
	<-p.resume
	// Leave the captured bytes to the real provider synthesizer.
	return nil, false, nil
}

func TestAddOrUpdateClientPreservesNewerPersistedState(t *testing.T) {
	for _, targetDisabled := range []bool{false, true} {
		name := "resume"
		if targetDisabled {
			name = "manual_disable"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			initial, errMarshal := json.Marshal(map[string]any{
				"type": "codex", "disabled": !targetDisabled, "note": "old note",
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if errWrite := os.WriteFile(path, initial, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w := &Watcher{authDir: dir, config: &config.Config{AuthDir: dir}}
			w.addOrUpdateClient(path)
			auth := w.currentAuths["account.json"].Clone()
			initialIndex := auth.EnsureIndex()
			// Keep updates pending for inspection, as in the full-scan regressions.
			w.authQueue = make(chan AuthUpdate, 10)
			// A semantic no-op edit bypasses the initial hash cache.
			staleBytes := append(initial, '\n')
			if errWrite := os.WriteFile(path, staleBytes, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			parser := &pausedAuthParser{read: make(chan struct{}), resume: make(chan struct{})}
			w.SetPluginAuthParser(parser)
			finished := make(chan struct{})
			finishRead := sync.OnceFunc(func() {
				close(parser.resume)
				<-finished
			})
			t.Cleanup(finishRead)
			go func() {
				defer close(finished)
				w.addOrUpdateClient(path)
			}()
			<-parser.read

			// Persist and dispatch while the watcher holds the older file bytes.
			auth.Disabled = targetDisabled
			auth.Status = coreauth.StatusActive
			if targetDisabled {
				auth.Status = coreauth.StatusDisabled
			}
			auth.Metadata["disabled"] = targetDisabled
			auth.Metadata["note"] = "new note"
			auth.Attributes["note"] = "new note"
			store := sdkAuth.NewFileTokenStore()
			store.SetBaseDir(dir)
			if _, errSave := store.Save(t.Context(), auth); errSave != nil {
				t.Fatal(errSave)
			}
			update := AuthUpdate{Action: AuthUpdateActionModify, ID: auth.ID, Auth: auth}
			queued, revision := w.DispatchPersistedAuthUpdateWithRevision(&update)
			if !queued || revision == 0 {
				t.Fatal("persisted update was not queued with a revision")
			}
			finishRead()

			current := w.currentAuths[auth.ID]
			if current == nil || current.Disabled != targetDisabled || current.Metadata["note"] != "new note" || current.EnsureIndex() != initialIndex {
				t.Error("stale file read replaced the newer persisted auth")
			}
			pending := w.pendingUpdates[auth.ID]
			if pending.Revision() != revision || pending.Auth == nil || pending.Auth.Disabled != targetDisabled || pending.Auth.Metadata["note"] != "new note" {
				t.Error("stale file read replaced the newer pending update")
			}

			// Restoring the discarded bytes later is a legitimate manual edit.
			w.SetPluginAuthParser(nil)
			if errWrite := os.WriteFile(path, staleBytes, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w.addOrUpdateClient(path)
			restored := w.currentAuths[auth.ID]
			if restored == nil || restored.Disabled != !targetDisabled || restored.Metadata["note"] != "old note" {
				t.Error("cached hash suppressed a later edit restoring the discarded bytes")
			}
			pending = w.pendingUpdates[auth.ID]
			if pending.Revision() <= revision || pending.Auth == nil || pending.Auth.Disabled != !targetDisabled || pending.Auth.Metadata["note"] != "old note" {
				t.Error("later manual edit was not queued with a newer revision")
			}
		})
	}
}
