package watcher

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestWatcherPreservesRemotePublicationAcrossRescans(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			authDir := filepath.Join(root, "auths")
			var uploaded []byte
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if r.Method != http.MethodPut {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var err error
				uploaded, err = io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				staged, err := filepath.Glob(filepath.Join(authDir, ".auth-acquisition-*", "credential"))
				if err != nil || len(staged) != 1 {
					t.Errorf("staged credential: %v, %v", staged, err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if err := os.Remove(staged[0]); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("ETag", `"committed"`)
			}))
			defer remote.Close()
			tokenStore, err := store.NewObjectTokenStore(store.ObjectStoreConfig{
				Endpoint: strings.TrimPrefix(remote.URL, "http://"), Bucket: "credentials", AccessKey: "test-key",
				SecretKey: "test-secret", Region: "us-east-1", LocalRoot: root, PathStyle: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(authDir, "account.json")
			cfg := &config.Config{AuthDir: authDir}
			w := &Watcher{authDir: authDir, config: cfg, authQueue: make(chan AuthUpdate, 1)}
			var stale *coreauth.Auth
			if existing {
				if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"old"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				w.refreshAuthState(false)
				update, ok := takePublicationUpdate(w, "account.json")
				if !ok || update.Auth == nil {
					t.Fatal("initial watcher scan produced no auth")
				}
				stale = update.Auth
			}
			record := &coreauth.Auth{ID: "account.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "committed"}}
			if _, err := tokenStore.Save(coreauth.WithAuthCreationIntent(context.Background()), record); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(uploaded, []byte("committed")) {
				t.Fatal("remote did not receive the credential")
			}
			listed, err := tokenStore.List(context.Background())
			if err != nil || len(listed) != 1 || listed[0].ID != record.ID || listed[0].Metadata["access_token"] != "committed" {
				t.Fatalf("store listing lost the pending credential or exposed its journal: %#v, %v", listed, err)
			}
			manager := coreauth.NewManager(nil, nil, nil)
			if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), record); err != nil {
				t.Fatal(err)
			}
			if stale != nil {
				if _, err := manager.Update(coreauth.WithSkipPersist(context.Background()), stale); err != nil {
					t.Fatal(err)
				}
			}
			apply := func(stage string, scan *Watcher) {
				t.Helper()
				update, ok := takePublicationUpdate(scan, record.ID)
				if !ok || update.Auth == nil || update.Action == AuthUpdateActionDelete {
					t.Fatalf("%s lost committed credential: %#v", stage, update)
				}
				if _, err := manager.Update(coreauth.WithSkipPersist(context.Background()), update.Auth); err != nil {
					t.Fatal(err)
				}
				active, ok := manager.GetByID(record.ID)
				if !ok || active.Metadata["access_token"] != "committed" {
					t.Fatalf("%s rolled back runtime: %#v", stage, active)
				}
			}
			w.refreshAuthState(true)
			apply("full rescan", w)
			w.lastAuthHashes = nil
			w.addOrUpdateClient(path)
			if update, ok := takePublicationUpdate(w, record.ID); ok && (update.Auth == nil || update.Auth.Metadata["access_token"] != "committed") {
				t.Fatalf("incremental read used stale mirror: %#v", update)
			}
			w.removeClient(path)
			if update, ok := takePublicationUpdate(w, record.ID); ok && update.Action == AuthUpdateActionDelete {
				t.Fatal("mirror removal deleted a remotely committed credential")
			}
			fresh := &Watcher{authDir: authDir, config: cfg, authQueue: make(chan AuthUpdate, 1)}
			fresh.refreshAuthState(false)
			apply("fresh watcher", fresh)
			if err := authpublication.Repair(path); err != nil {
				t.Fatal(err)
			}
			if authpublication.HasPending(path) {
				t.Fatal("repair left publication record active")
			}
			if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"edited"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			fresh.addOrUpdateClient(path)
			update, ok := takePublicationUpdate(fresh, record.ID)
			if !ok || update.Auth == nil {
				t.Fatal("ordinary edits stayed blocked after mirror repair")
			}
			if _, err := manager.Update(coreauth.WithSkipPersist(context.Background()), update.Auth); err != nil {
				t.Fatal(err)
			}
			active, _ := manager.GetByID(record.ID)
			if active.Metadata["access_token"] != "edited" {
				t.Fatal("repaired mirror did not resume normal file updates")
			}
			if _, err := tokenStore.Save(coreauth.WithAuthCreationIntent(context.Background()), record); err != nil {
				t.Fatal(err)
			}
			if !authpublication.HasPending(path) {
				t.Fatal("second publication did not leave a pending overlay")
			}
			if err := tokenStore.Delete(context.Background(), record.ID); err != nil {
				t.Fatal(err)
			}
			if authpublication.HasPending(path) {
				t.Fatal("explicit deletion left a credential overlay active")
			}
			fresh.removeClient(path)
			if update, ok := takePublicationUpdate(fresh, record.ID); !ok || update.Action != AuthUpdateActionDelete {
				t.Fatal("explicit deletion stayed hidden by the retired publication")
			}

		})
	}
}

func takePublicationUpdate(w *Watcher, id string) (AuthUpdate, bool) {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	update, ok := w.pendingUpdates[id]
	w.pendingUpdates, w.pendingOrder = nil, nil
	return update, ok
}
