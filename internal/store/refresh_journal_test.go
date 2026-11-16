package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestGitRefreshJournalCoordinatesIndependentMirrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "owner@local")
	if errMkdir := os.Mkdir(root, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	remote := setupGitRemoteRepository(t, root, "main", testBranchSpec{name: "main", contents: "initial\n"})
	var journals []codex.RefreshJournal
	for _, name := range []string{"first", "second"} {
		store := NewGitTokenStore(remote, "", "", "main")
		store.SetBaseDir(filepath.Join(root, name, "auths"))
		if errEnsure := store.EnsureRepository(); errEnsure != nil {
			t.Fatal(errEnsure)
		}
		journal, ok := any(store).(codex.RefreshJournal)
		if !ok {
			t.Fatal("remote store lacks an authoritative refresh journal")
		}
		journals = append(journals, journal)
	}
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	var winners atomic.Int32
	var owner codex.RefreshJournal
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, journal := range journals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, errClaim := journal.RefreshClaim(ctx, key)
			if errClaim != nil {
				t.Errorf("claim: %v", errClaim)
				return
			}
			if claimed {
				winners.Add(1)
				mu.Lock()
				owner = journal
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("claim winners = %d, want 1", winners.Load())
	}
	for _, journal := range journals {
		claimed, errClaim := journal.RefreshClaim(ctx, key)
		if errClaim != nil || claimed {
			t.Fatalf("unresolved claim was reclaimed: %v, %v", claimed, errClaim)
		}
	}
	receipt := []byte(`{"value":{"access_token":"synthetic-new"}}`)
	if errComplete := owner.RefreshComplete(ctx, key, receipt); errComplete != nil {
		t.Fatal(errComplete)
	}
	for _, journal := range journals {
		data, errRead := journal.RefreshRead(ctx, key)
		if errRead != nil || string(data) != string(receipt) {
			t.Fatalf("shared receipt = %s, error %v", data, errRead)
		}
	}
}

func TestRemoteStoresProvideAuthoritativeRefreshJournal(t *testing.T) {
	for name, store := range map[string]any{
		"postgres": &PostgresStore{},
		"git":      &GitTokenStore{},
		"object":   &ObjectTokenStore{},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := store.(codex.RefreshJournal); !ok {
				t.Fatal("remote credential store cannot preserve refresh claims across mirror rebuilds")
			}
		})
	}
}
