package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type metadataMergeStore struct {
	mu      sync.Mutex
	saves   []*Auth
	saveErr error
}

type blockingMetadataMergeStore struct {
	store   *metadataMergeStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type orderedMetadataMergeStore struct {
	store         *metadataMergeStore
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
	mu            sync.Mutex
	calls         int
}

func (s *orderedMetadataMergeStore) List(ctx context.Context) ([]*Auth, error) {
	return s.store.List(ctx)
}

func (s *orderedMetadataMergeStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	switch call {
	case 1:
		close(s.firstStarted)
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	case 2:
		close(s.secondStarted)
	}
	return s.store.Save(ctx, auth)
}

func (s *orderedMetadataMergeStore) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

func (s *blockingMetadataMergeStore) List(ctx context.Context) ([]*Auth, error) {
	return s.store.List(ctx)
}

func (s *blockingMetadataMergeStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.once.Do(func() {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	return s.store.Save(ctx, auth)
}

func (s *blockingMetadataMergeStore) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

func (s *metadataMergeStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *metadataMergeStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves = append(s.saves, cloneAuthForMetadataMerge(auth))
	if s.saveErr != nil {
		return "", s.saveErr
	}
	return auth.ID, nil
}

func (s *metadataMergeStore) Delete(context.Context, string) error { return nil }

func (s *metadataMergeStore) setSaveError(err error) {
	s.mu.Lock()
	s.saveErr = err
	s.mu.Unlock()
}

func (s *metadataMergeStore) saved() []*Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*Auth, len(s.saves))
	for i, auth := range s.saves {
		result[i] = cloneAuthForMetadataMerge(auth)
	}
	return result
}

type metadataMergeTokenStorage struct{}

func (*metadataMergeTokenStorage) SaveTokenToFile(string) error { return nil }

func TestManagerMergeMetadataByIndexPreservesLatestMetadata(t *testing.T) {
	ctx := context.Background()
	store := &metadataMergeStore{}
	manager := NewManager(store, nil, nil)
	storage := &metadataMergeTokenStorage{}
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Status:   StatusActive,
		Storage:  storage,
		Metadata: map[string]any{
			"access_token":  "latest-access-token",
			"refresh_token": "latest-refresh-token",
			"id_token":      "latest-id-token",
			"other": map[string]any{
				"preserved": true,
			},
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	agentIdentity := map[string]any{
		"runtime_id": "runtime-1",
		"key": map[string]any{
			"private_key": "private-key",
		},
	}
	updated, errMerge := manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{
		"agent_identity": agentIdentity,
		"task_id":        "task-1",
	})
	if errMerge != nil {
		t.Fatalf("MergeMetadataByIndex() error = %v", errMerge)
	}
	if updated == nil {
		t.Fatal("MergeMetadataByIndex() returned nil auth")
	}

	assertMetadataMergeValues(t, updated)
	saves := store.saved()
	if len(saves) != 1 {
		t.Fatalf("Save() calls = %d, want 1", len(saves))
	}
	if saves[0].Storage != nil {
		t.Fatal("persist snapshot retained stale TokenStorage")
	}
	assertMetadataMergeValues(t, saves[0])

	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatal("manager auth missing after metadata merge")
	}
	if current.Storage != storage {
		t.Fatal("manager auth did not preserve runtime TokenStorage")
	}
	assertMetadataMergeValues(t, current)

	// Caller-owned nested maps must not become mutable manager state.
	agentIdentity["runtime_id"] = "mutated-runtime"
	agentIdentity["key"].(map[string]any)["private_key"] = "mutated-key"
	current, _ = manager.GetByID(auth.ID)
	identity, _ := current.Metadata["agent_identity"].(map[string]any)
	if got := identity["runtime_id"]; got != "runtime-1" {
		t.Fatalf("runtime_id = %#v after caller mutation, want runtime-1", got)
	}
	key, _ := identity["key"].(map[string]any)
	if got := key["private_key"]; got != "private-key" {
		t.Fatalf("private_key = %#v after caller mutation, want private-key", got)
	}
}

func assertMetadataMergeValues(t *testing.T, auth *Auth) {
	t.Helper()
	if got := auth.Metadata["access_token"]; got != "latest-access-token" {
		t.Fatalf("access_token = %#v, want latest-access-token", got)
	}
	if got := auth.Metadata["refresh_token"]; got != "latest-refresh-token" {
		t.Fatalf("refresh_token = %#v, want latest-refresh-token", got)
	}
	if got := auth.Metadata["id_token"]; got != "latest-id-token" {
		t.Fatalf("id_token = %#v, want latest-id-token", got)
	}
	other, _ := auth.Metadata["other"].(map[string]any)
	if got := other["preserved"]; got != true {
		t.Fatalf("other.preserved = %#v, want true", got)
	}
	identity, _ := auth.Metadata["agent_identity"].(map[string]any)
	if got := identity["runtime_id"]; got != "runtime-1" {
		t.Fatalf("agent_identity.runtime_id = %#v, want runtime-1", got)
	}
	if got := auth.Metadata["task_id"]; got != "task-1" {
		t.Fatalf("task_id = %#v, want task-1", got)
	}
}

func TestManagerMergeMetadataByIndexUnknownIndex(t *testing.T) {
	store := &metadataMergeStore{}
	manager := NewManager(store, nil, nil)

	updated, errMerge := manager.MergeMetadataByIndex(context.Background(), "missing-index", map[string]any{"task_id": "task-1"})
	if updated != nil {
		t.Fatalf("MergeMetadataByIndex() auth = %#v, want nil", updated)
	}
	if !errors.Is(errMerge, ErrAuthIndexNotFound) {
		t.Fatalf("MergeMetadataByIndex() error = %v, want ErrAuthIndexNotFound", errMerge)
	}
	if saves := store.saved(); len(saves) != 0 {
		t.Fatalf("Save() calls = %d, want 0", len(saves))
	}
}

func TestManagerMergeMetadataByIndexRejectsDuplicateIndex(t *testing.T) {
	ctx := context.Background()
	store := &metadataMergeStore{}
	manager := NewManager(store, nil, nil)
	for _, id := range []string{"auth-a", "auth-b"} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), &Auth{
			ID:       id,
			Index:    "duplicate-index",
			Provider: "codex",
			Metadata: map[string]any{"access_token": id + "-token"},
		}); errRegister != nil {
			t.Fatalf("Register(%q) error = %v", id, errRegister)
		}
	}

	updated, errMerge := manager.MergeMetadataByIndex(ctx, "duplicate-index", map[string]any{"task_id": "task-1"})
	if updated != nil {
		t.Fatalf("MergeMetadataByIndex() auth = %#v, want nil", updated)
	}
	if !errors.Is(errMerge, ErrAuthIndexAmbiguous) {
		t.Fatalf("MergeMetadataByIndex() error = %v, want ErrAuthIndexAmbiguous", errMerge)
	}
	if saves := store.saved(); len(saves) != 0 {
		t.Fatalf("Save() calls = %d, want 0", len(saves))
	}
}

func TestManagerMergeMetadataByIndexRollsBackOnSaveFailure(t *testing.T) {
	ctx := context.Background()
	store := &metadataMergeStore{}
	manager := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "current-access-token",
			"refresh_token": "current-refresh-token",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	errSave := errors.New("save failed")
	store.setSaveError(errSave)
	updated, errMerge := manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{
		"agent_identity": map[string]any{"runtime_id": "runtime-1"},
	})
	if updated != nil {
		t.Fatalf("MergeMetadataByIndex() auth = %#v, want nil", updated)
	}
	if !errors.Is(errMerge, errSave) {
		t.Fatalf("MergeMetadataByIndex() error = %v, want wrapped save error", errMerge)
	}

	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatal("manager auth missing after failed save")
	}
	if _, exists := current.Metadata["agent_identity"]; exists {
		t.Fatal("failed persistence made merged metadata observable")
	}
	if got := current.Metadata["access_token"]; got != "current-access-token" {
		t.Fatalf("access_token = %#v after failed save, want current-access-token", got)
	}
	if saves := store.saved(); len(saves) != 1 {
		t.Fatalf("Save() calls = %d, want 1", len(saves))
	}
}

func TestManagerMergeMetadataByIndexWaitsForInFlightUpdatePersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	recordingStore := &metadataMergeStore{}
	store := &orderedMetadataMergeStore{
		store:         recordingStore,
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	manager := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token": "original-access-token",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	updated := auth.Clone()
	updated.Metadata["access_token"] = "updated-access-token"
	updateDone := make(chan error, 1)
	go func() {
		_, errUpdate := manager.Update(ctx, updated)
		updateDone <- errUpdate
	}()
	select {
	case <-store.firstStarted:
	case <-ctx.Done():
		t.Fatalf("Update() did not reach persistence: %v", ctx.Err())
	}

	mergeAttempted := make(chan struct{})
	mergeDone := make(chan error, 1)
	go func() {
		close(mergeAttempted)
		_, errMerge := manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{
			"agent_identity": map[string]any{"runtime_id": "runtime-1"},
		})
		mergeDone <- errMerge
	}()
	<-mergeAttempted
	select {
	case <-store.secondStarted:
		t.Fatal("metadata merge reached persistence before the in-flight Update completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(store.releaseFirst)
	select {
	case errUpdate := <-updateDone:
		if errUpdate != nil {
			t.Fatalf("Update() error = %v", errUpdate)
		}
	case <-ctx.Done():
		t.Fatalf("Update() did not finish: %v", ctx.Err())
	}
	select {
	case errMerge := <-mergeDone:
		if errMerge != nil {
			t.Fatalf("MergeMetadataByIndex() error = %v", errMerge)
		}
	case <-ctx.Done():
		t.Fatalf("metadata merge did not finish: %v", ctx.Err())
	}

	saves := recordingStore.saved()
	if len(saves) != 2 {
		t.Fatalf("Save() calls = %d, want Update then merge", len(saves))
	}
	last := saves[len(saves)-1]
	if got := last.Metadata["access_token"]; got != "updated-access-token" {
		t.Fatalf("last persisted access_token = %#v, want updated-access-token", got)
	}
	if _, ok := last.Metadata["agent_identity"].(map[string]any); !ok {
		t.Fatalf("last persisted agent_identity = %#v, want object", last.Metadata["agent_identity"])
	}
}

func TestManagerMetadataTransactionRequiresDurableStore(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "access-token"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	called := false
	result, errTransaction := manager.WithMetadataTransactionByIndex(context.Background(), auth.Index, func(*MetadataTransaction) error {
		called = true
		return nil
	})
	if result != nil {
		t.Fatalf("WithMetadataTransactionByIndex() auth = %#v, want nil", result)
	}
	if !errors.Is(errTransaction, ErrAuthStoreUnavailable) {
		t.Fatalf("WithMetadataTransactionByIndex() error = %v, want ErrAuthStoreUnavailable", errTransaction)
	}
	if called {
		t.Fatal("transaction callback ran without a durable store")
	}
}

type reentrantMetadataMergeHook struct {
	NoopHook
	manager *Manager
	mu      sync.Mutex
	called  bool
	done    chan error
}

func (hook *reentrantMetadataMergeHook) OnAuthUpdated(ctx context.Context, auth *Auth) {
	hook.mu.Lock()
	if hook.called {
		hook.mu.Unlock()
		return
	}
	hook.called = true
	hook.mu.Unlock()
	_, errMerge := hook.manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{"hook_marker": "set"})
	hook.done <- errMerge
}

func TestManagerMetadataTransactionHookCanReenterManager(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := &metadataMergeStore{}
	hook := &reentrantMetadataMergeHook{done: make(chan error, 1)}
	manager := NewManager(store, nil, hook)
	hook.manager = manager
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "access-token"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	if _, errMerge := manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{"agent_identity": "ready"}); errMerge != nil {
		t.Fatalf("MergeMetadataByIndex() error = %v", errMerge)
	}
	select {
	case errHook := <-hook.done:
		if errHook != nil {
			t.Fatalf("reentrant MergeMetadataByIndex() error = %v", errHook)
		}
	case <-ctx.Done():
		t.Fatalf("OnAuthUpdated hook deadlocked: %v", ctx.Err())
	}
	updated, _ := manager.GetByID(auth.ID)
	if got := updated.Metadata["hook_marker"]; got != "set" {
		t.Fatalf("hook_marker = %#v, want set", got)
	}
}

type metadataMergeRefreshExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*metadataMergeRefreshExecutor) Identifier() string { return "codex" }

func (*metadataMergeRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*metadataMergeRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *metadataMergeRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-access-token"
	auth.Metadata["refresh_token"] = "rotated-refresh-token"
	return auth, nil
}

func (*metadataMergeRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*metadataMergeRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerMetadataTransactionStartsFromRefreshedAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := &metadataMergeStore{}
	manager := NewManager(store, nil, nil)
	executor := &metadataMergeRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "original-refresh-token",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
		refreshDone <- errRefresh
	}()
	select {
	case <-executor.started:
	case <-ctx.Done():
		t.Fatalf("refresh did not start: %v", ctx.Err())
	}

	callbackStarted := make(chan *Auth, 1)
	transactionDone := make(chan error, 1)
	go func() {
		_, errTransaction := manager.WithMetadataTransactionByIndex(ctx, auth.Index, func(transaction *MetadataTransaction) error {
			callbackStarted <- transaction.Auth()
			return nil
		})
		transactionDone <- errTransaction
	}()
	select {
	case <-callbackStarted:
		t.Fatal("metadata transaction callback ran while refresh was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(executor.release)
	select {
	case errRefresh := <-refreshDone:
		if errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}
	case <-ctx.Done():
		t.Fatalf("refresh did not finish: %v", ctx.Err())
	}
	var fresh *Auth
	select {
	case fresh = <-callbackStarted:
	case <-ctx.Done():
		t.Fatalf("metadata transaction did not start: %v", ctx.Err())
	}
	if got := fresh.Metadata["access_token"]; got != "refreshed-access-token" {
		t.Fatalf("transaction access_token = %#v, want refreshed-access-token", got)
	}
	if got := fresh.Metadata["refresh_token"]; got != "rotated-refresh-token" {
		t.Fatalf("transaction refresh_token = %#v, want rotated-refresh-token", got)
	}
	select {
	case errTransaction := <-transactionDone:
		if errTransaction != nil {
			t.Fatalf("WithMetadataTransactionByIndex() error = %v", errTransaction)
		}
	case <-ctx.Done():
		t.Fatalf("metadata transaction did not finish: %v", ctx.Err())
	}
}

func TestManagerRefreshWaitsForMetadataTransactionCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := &metadataMergeStore{}
	manager := NewManager(store, nil, nil)
	executor := &metadataMergeRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "original-refresh-token",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	transactionDone := make(chan error, 1)
	go func() {
		_, errTransaction := manager.WithMetadataTransactionByIndex(ctx, auth.Index, func(*MetadataTransaction) error {
			close(callbackStarted)
			select {
			case <-releaseCallback:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		transactionDone <- errTransaction
	}()
	select {
	case <-callbackStarted:
	case <-ctx.Done():
		t.Fatalf("metadata transaction did not start: %v", ctx.Err())
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
		refreshDone <- errRefresh
	}()
	select {
	case <-executor.started:
		t.Fatal("refresh entered its executor while metadata transaction callback was running")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseCallback)
	select {
	case errTransaction := <-transactionDone:
		if errTransaction != nil {
			t.Fatalf("WithMetadataTransactionByIndex() error = %v", errTransaction)
		}
	case <-ctx.Done():
		t.Fatalf("metadata transaction did not finish: %v", ctx.Err())
	}
	select {
	case <-executor.started:
	case <-ctx.Done():
		t.Fatalf("refresh did not start after transaction: %v", ctx.Err())
	}
	close(executor.release)
	select {
	case errRefresh := <-refreshDone:
		if errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}
	case <-ctx.Done():
		t.Fatalf("refresh did not finish: %v", ctx.Err())
	}
}

func TestManagerMergeMetadataByIndexSerializesWithRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := &metadataMergeStore{}
	manager := NewManager(store, nil, nil)
	executor := &metadataMergeRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "original-refresh-token",
			"other":         "preserved",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
		refreshDone <- errRefresh
	}()
	select {
	case <-executor.started:
	case <-ctx.Done():
		t.Fatalf("refresh did not start: %v", ctx.Err())
	}

	type mergeResult struct {
		auth *Auth
		err  error
	}
	mergeStarted := make(chan struct{})
	mergeDone := make(chan mergeResult, 1)
	go func() {
		close(mergeStarted)
		updated, errMerge := manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{
			"agent_identity": map[string]any{"runtime_id": "runtime-1"},
		})
		mergeDone <- mergeResult{auth: updated, err: errMerge}
	}()
	<-mergeStarted

	var early *mergeResult
	select {
	case result := <-mergeDone:
		early = &result
	case <-time.After(50 * time.Millisecond):
	}
	close(executor.release)

	select {
	case errRefresh := <-refreshDone:
		if errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}
	case <-ctx.Done():
		t.Fatalf("refresh did not finish: %v", ctx.Err())
	}

	var merged mergeResult
	if early != nil {
		merged = *early
	} else {
		select {
		case merged = <-mergeDone:
		case <-ctx.Done():
			t.Fatalf("metadata merge did not finish: %v", ctx.Err())
		}
	}
	if early != nil {
		t.Fatal("metadata merge completed while refresh held the per-auth lock")
	}
	if merged.err != nil {
		t.Fatalf("MergeMetadataByIndex() error = %v", merged.err)
	}
	if got := merged.auth.Metadata["access_token"]; got != "refreshed-access-token" {
		t.Fatalf("merged access_token = %#v, want refreshed-access-token", got)
	}
	if got := merged.auth.Metadata["refresh_token"]; got != "rotated-refresh-token" {
		t.Fatalf("merged refresh_token = %#v, want rotated-refresh-token", got)
	}
	if got := merged.auth.Metadata["other"]; got != "preserved" {
		t.Fatalf("merged other metadata = %#v, want preserved", got)
	}
	if _, ok := merged.auth.Metadata["agent_identity"].(map[string]any); !ok {
		t.Fatalf("merged agent_identity = %#v, want object", merged.auth.Metadata["agent_identity"])
	}

	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatal("manager auth missing after refresh and merge")
	}
	if got := current.Metadata["access_token"]; got != "refreshed-access-token" {
		t.Fatalf("manager access_token = %#v, want refreshed-access-token", got)
	}
	if _, ok := current.Metadata["agent_identity"].(map[string]any); !ok {
		t.Fatalf("manager agent_identity = %#v, want object", current.Metadata["agent_identity"])
	}

	saves := store.saved()
	if len(saves) != 2 {
		t.Fatalf("Save() calls = %d, want refresh and merge saves", len(saves))
	}
	last := saves[len(saves)-1]
	if got := last.Metadata["access_token"]; got != "refreshed-access-token" {
		t.Fatalf("last persisted access_token = %#v, want refreshed-access-token", got)
	}
	if _, ok := last.Metadata["agent_identity"].(map[string]any); !ok {
		t.Fatalf("last persisted agent_identity = %#v, want object", last.Metadata["agent_identity"])
	}
}

func TestManagerRefreshWaitsForMetadataMergePersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	recordingStore := &metadataMergeStore{}
	store := &blockingMetadataMergeStore{
		store:   recordingStore,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager := NewManager(store, nil, nil)
	executorRelease := make(chan struct{})
	executor := &metadataMergeRefreshExecutor{
		started: make(chan struct{}),
		release: executorRelease,
	}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-auth",
		Index:    "codex-index",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "original-refresh-token",
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	mergeDone := make(chan error, 1)
	go func() {
		_, errMerge := manager.MergeMetadataByIndex(ctx, auth.Index, map[string]any{
			"agent_identity": map[string]any{"runtime_id": "runtime-1"},
		})
		mergeDone <- errMerge
	}()
	select {
	case <-store.started:
	case <-ctx.Done():
		t.Fatalf("metadata merge did not reach persistence: %v", ctx.Err())
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
		refreshDone <- errRefresh
	}()
	select {
	case <-executor.started:
		t.Fatal("refresh entered the executor while metadata merge held the per-auth lock")
	case <-time.After(50 * time.Millisecond):
	}

	close(store.release)
	select {
	case errMerge := <-mergeDone:
		if errMerge != nil {
			t.Fatalf("MergeMetadataByIndex() error = %v", errMerge)
		}
	case <-ctx.Done():
		t.Fatalf("metadata merge did not finish: %v", ctx.Err())
	}
	select {
	case <-executor.started:
	case <-ctx.Done():
		t.Fatalf("refresh did not start after metadata merge: %v", ctx.Err())
	}
	close(executorRelease)
	select {
	case errRefresh := <-refreshDone:
		if errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}
	case <-ctx.Done():
		t.Fatalf("refresh did not finish: %v", ctx.Err())
	}

	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatal("manager auth missing after merge and refresh")
	}
	if got := current.Metadata["access_token"]; got != "refreshed-access-token" {
		t.Fatalf("manager access_token = %#v, want refreshed-access-token", got)
	}
	if _, ok := current.Metadata["agent_identity"].(map[string]any); !ok {
		t.Fatalf("manager agent_identity = %#v, want object", current.Metadata["agent_identity"])
	}

	saves := recordingStore.saved()
	if len(saves) != 2 {
		t.Fatalf("Save() calls = %d, want merge and refresh saves", len(saves))
	}
	last := saves[len(saves)-1]
	if got := last.Metadata["access_token"]; got != "refreshed-access-token" {
		t.Fatalf("last persisted access_token = %#v, want refreshed-access-token", got)
	}
	if _, ok := last.Metadata["agent_identity"].(map[string]any); !ok {
		t.Fatalf("last persisted agent_identity = %#v, want object", last.Metadata["agent_identity"])
	}
}
