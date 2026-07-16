package watcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestProcessEventsReconcilesAuthsAfterWatcherError(t *testing.T) {
	originalSnapshot := snapshotCoreAuthsFunc
	snapshotCoreAuthsFunc = func(*config.Config, string, synthesizer.PluginAuthParser) []*coreauth.Auth {
		return []*coreauth.Auth{{ID: "synthetic-auth.json", Provider: "codex", Metadata: map[string]any{"type": "codex"}}}
	}
	t.Cleanup(func() { snapshotCoreAuthsFunc = originalSnapshot })

	queue := make(chan AuthUpdate, 2)
	w := &Watcher{
		config:          &config.Config{},
		authDir:         t.TempDir(),
		currentAuths:    make(map[string]*coreauth.Auth),
		runtimeAuths:    make(map[string]*coreauth.Auth),
		fileAuthsByPath: make(map[string]map[string]*coreauth.Auth),
		lastAuthHashes:  make(map[string]string),
		pendingUpdates:  make(map[string]AuthUpdate),
	}
	w.dispatchCond = sync.NewCond(&w.dispatchMu)
	w.setAuthUpdateQueue(queue)
	t.Cleanup(w.stopDispatch)

	w.handleWatcherError(errors.New("synthetic watcher overflow"))

	select {
	case update := <-queue:
		if update.Action != AuthUpdateActionAdd || update.Auth == nil || update.Auth.ID != "synthetic-auth.json" {
			t.Fatalf("unexpected reconciliation update: %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher error did not trigger bounded auth reconciliation")
	}
}

func TestAuthEqualIgnoresProcessLocalRevision(t *testing.T) {
	firstManager := coreauth.NewManager(nil, nil, nil)
	first, err := firstManager.Register(context.Background(), &coreauth.Auth{
		ID:       "synthetic-auth.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	})
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	secondManager := coreauth.NewManager(nil, nil, nil)
	second, err := secondManager.Register(context.Background(), &coreauth.Auth{
		ID:       "synthetic-auth.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	})
	if err != nil {
		t.Fatalf("second Register() error = %v", err)
	}
	if first.Revision() == second.Revision() {
		t.Fatal("test setup produced equal revisions")
	}
	if !authEqual(first, second) {
		t.Fatal("authEqual treated process-local revision as durable file change")
	}
}
