package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestHandleEventAtomicRewriteOfClaudeAuthFileIsModify covers the rewrite production auth
// files see all the time: a token refresh (new access_token) or an external script (new
// top-level weight) writes a temporary file and renames it over the credential. Whichever
// event the platform reports for the target, the watcher must publish one Modify for the
// same auth ID, never a Delete, so the core manager updates the credential in place and
// keeps its runtime state (quota observation, routing selector state).
func TestHandleEventAtomicRewriteOfClaudeAuthFileIsModify(t *testing.T) {
	for _, op := range []fsnotify.Op{fsnotify.Create, fsnotify.Write, fsnotify.Rename, fsnotify.Remove} {
		t.Run(op.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "claude-a@example.com.json")
			expired := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
			original := `{"type":"claude","email":"a@example.com","access_token":"token-1","expired":"` + expired + `","weight":1}`
			if errWrite := os.WriteFile(path, []byte(original), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			w := &Watcher{
				authDir:   dir,
				config:    &config.Config{AuthDir: dir},
				authQueue: make(chan AuthUpdate, 16),
			}
			w.addOrUpdateClient(path)
			added := drainPendingAuthUpdates(t, w)
			if len(added) != 1 || added[0].Action != AuthUpdateActionAdd || added[0].Auth == nil {
				t.Fatalf("initial updates = %+v, want one add", added)
			}
			id := added[0].ID

			// tmp + rename, as token refresh and external scripts do.
			rewritten := `{"type":"claude","email":"a@example.com","access_token":"token-2","expired":"` + expired + `","last_refresh":"` + time.Now().UTC().Format(time.RFC3339) + `","weight":7}`
			tmp := filepath.Join(dir, ".claude-a@example.com.json.tmp")
			if errWrite := os.WriteFile(tmp, []byte(rewritten), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errRename := os.Rename(tmp, path); errRename != nil {
				t.Fatal(errRename)
			}
			w.handleEvent(fsnotify.Event{Name: path, Op: op})

			updates := drainPendingAuthUpdates(t, w)
			if len(updates) != 1 {
				t.Fatalf("updates after rewrite = %+v, want exactly one", updates)
			}
			update := updates[0]
			if update.Action != AuthUpdateActionModify || update.ID != id || update.Auth == nil {
				t.Fatalf("update after rewrite = %+v, want modify of %s", update, id)
			}
			if got := update.Auth.Metadata["access_token"]; got != "token-2" {
				t.Fatalf("modified access_token = %v, want token-2", got)
			}
			if got := update.Auth.Attributes[coreauth.AttributeWeight]; got != "7" {
				t.Fatalf("modified weight = %q, want 7", got)
			}
		})
	}
}

func drainPendingAuthUpdates(t *testing.T, w *Watcher) []AuthUpdate {
	t.Helper()
	var updates []AuthUpdate
	for {
		w.dispatchMu.Lock()
		pending := len(w.pendingOrder)
		w.dispatchMu.Unlock()
		if pending == 0 {
			return updates
		}
		batch, ok := w.nextPendingBatch(context.Background())
		if !ok {
			return updates
		}
		updates = append(updates, batch...)
	}
}
