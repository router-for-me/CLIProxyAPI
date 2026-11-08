package store

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type journalRoundTripper func(*http.Request) (*http.Response, error)

func (r journalRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

func TestGitRefreshDoesNotDeadlockWithConcurrentSave(t *testing.T) {
	root := t.TempDir()
	remote := setupGitRemoteRepository(t, root, "main", testBranchSpec{name: "main", contents: "initial\n"})
	store := NewGitTokenStore(remote, "", "", "main")
	store.SetBaseDir(filepath.Join(root, "mirror", "auths"))
	if errEnsure := store.EnsureRepository(); errEnsure != nil {
		t.Fatal(errEnsure)
	}
	auth := &coreauth.Auth{ID: "journal.json", FileName: "journal.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "old-access", "refresh_token": "journal-lock-grant", "expired": "2020-01-01T00:00:00Z"}}
	if _, errSave := store.Save(context.Background(), auth); errSave != nil {
		t.Fatal(errSave)
	}
	transportEntered := make(chan struct{})
	allowResponse := make(chan struct{})
	originalTransport := http.DefaultTransport
	http.DefaultTransport = journalRoundTripper(func(req *http.Request) (*http.Response, error) {
		close(transportEntered)
		<-allowResponse
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-grant","expires_in":3600}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := executor.NewCodexExecutor(&config.Config{AuthDir: store.AuthDir()}).Refresh(codex.WithRefreshJournal(context.Background(), store), auth.Clone())
		refreshDone <- errRefresh
	}()
	<-transportEntered
	saveHoldingStoreMutex := make(chan struct{})
	var once sync.Once
	store.pullFn = func(context.Context, *git.Worktree, *git.PullOptions) error {
		once.Do(func() { close(saveHoldingStoreMutex) })
		return git.NoErrAlreadyUpToDate
	}
	saveDone := make(chan error, 1)
	go func() { _, errSave := store.Save(context.Background(), auth.Clone()); saveDone <- errSave }()
	<-saveHoldingStoreMutex
	close(allowResponse)
	if errRefresh := <-refreshDone; errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if errSave := <-saveDone; errSave != nil {
		t.Fatal(errSave)
	}
}
