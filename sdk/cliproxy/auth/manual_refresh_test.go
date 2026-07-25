package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type manualRefreshExecutor struct {
	provider string
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	calls    atomic.Int32
}

func (e *manualRefreshExecutor) Identifier() string { return e.provider }

func (e *manualRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *manualRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *manualRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	call := e.calls.Add(1)
	if e.started != nil {
		e.once.Do(func() { close(e.started) })
	}
	if e.release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.release:
		}
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "fresh-token"
	auth.Metadata["refresh_token"] = "rotated-refresh-token"
	auth.Metadata["refresh_call"] = call
	return auth, nil
}

func (e *manualRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *manualRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type failingRefreshStore struct {
	err error
}

func (s *failingRefreshStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *failingRefreshStore) Save(context.Context, *Auth) (string, error) {
	return "", s.err
}
func (s *failingRefreshStore) Delete(context.Context, string) error { return nil }

func registerManualRefreshAuth(t *testing.T, manager *Manager) *Auth {
	t.Helper()
	auth := &Auth{
		ID:       "manual-refresh-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "refresh-token",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return auth
}

func TestManagerRefreshNowForcesAndPersistsCredentialRefresh(t *testing.T) {
	store := &countingStore{}
	manager := NewManager(store, nil, nil)
	executor := &manualRefreshExecutor{provider: "codex"}
	manager.RegisterExecutor(executor)
	auth := registerManualRefreshAuth(t, manager)

	updated, errRefresh := manager.RefreshNow(context.Background(), auth.ID)
	if errRefresh != nil {
		t.Fatalf("RefreshNow() error = %v", errRefresh)
	}
	if got := authAccessToken(updated); got != "fresh-token" {
		t.Fatalf("access_token = %q, want fresh-token", got)
	}
	if updated.LastRefreshedAt.IsZero() {
		t.Fatal("LastRefreshedAt is zero")
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("Refresh() calls = %d, want 1", got)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("Save() calls = %d, want 1", got)
	}
}

func TestManagerRefreshNowReportsPersistenceFailure(t *testing.T) {
	persistErr := errors.New("disk unavailable")
	manager := NewManager(&failingRefreshStore{err: persistErr}, nil, nil)
	manager.RegisterExecutor(&manualRefreshExecutor{provider: "codex"})
	auth := registerManualRefreshAuth(t, manager)

	updated, errRefresh := manager.RefreshNow(context.Background(), auth.ID)
	if !errors.Is(errRefresh, ErrRefreshPersistFailed) {
		t.Fatalf("RefreshNow() error = %v, want ErrRefreshPersistFailed", errRefresh)
	}
	if updated == nil || authAccessToken(updated) != "fresh-token" {
		t.Fatalf("updated auth = %#v, want refreshed in-memory credential", updated)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || authAccessToken(current) != "fresh-token" {
		t.Fatalf("runtime auth = %#v, want refreshed credential", current)
	}
}

func TestRefreshAuthForRequestCoalescesConcurrentTokenRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	executor := &manualRefreshExecutor{
		provider: "codex",
		started:  started,
		release:  release,
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := registerManualRefreshAuth(t, manager)

	type result struct {
		auth *Auth
		err  error
	}
	results := make(chan result, 2)
	refresh := func() {
		updated, errRefresh := manager.refreshAuthForRequest(
			context.Background(),
			auth.ID,
			"stale-token",
			true,
		)
		results <- result{auth: updated, err: errRefresh}
	}

	go refresh()
	<-started
	go refresh()
	close(release)

	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("refresh error = %v", got.err)
		}
		if got.auth == nil || authAccessToken(got.auth) != "fresh-token" {
			t.Fatalf("refreshed auth = %#v", got.auth)
		}
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("Refresh() calls = %d, want 1", got)
	}
}
