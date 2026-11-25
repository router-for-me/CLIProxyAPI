package auth

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7727TokenStorage struct {
	slot int
}

func (*issue7727TokenStorage) SaveTokenToFile(string) error { return nil }

type issue7727FuncTokenStorage func() string

func (issue7727FuncTokenStorage) SaveTokenToFile(string) error { return nil }

func makeIssue7727RuntimeClosure(v int) func() int {
	return func() int { return v }
}

func makeIssue7727FuncStorage(tag string) issue7727FuncTokenStorage {
	return func() string { return tag }
}

type issue7727Executor struct {
	provider string

	refreshStarted chan struct{}
	releaseRefresh chan struct{}
	refreshOnce    sync.Once
	inRefresh      atomic.Bool
	refreshSetsPID bool

	prepareStarted chan struct{}
	releasePrepare chan struct{}
	prepareOnce    sync.Once

	prepareCalls           atomic.Int32
	prepareOverlapDetected atomic.Bool

	mu                    sync.Mutex
	prepareObservedTokens []string
	prepareObservedIDs    []string
	prepareFn             func(context.Context, *Auth) (*Auth, error)
}

func (e *issue7727Executor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "antigravity"
}

func (e *issue7727Executor) ShouldPrepareRequestAuth(auth *Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return true
	}
	return testStringValue(auth.Metadata["project_id"]) == ""
}

func (e *issue7727Executor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.inRefresh.Load() {
		e.prepareOverlapDetected.Store(true)
	}
	e.prepareCalls.Add(1)
	if auth != nil {
		e.mu.Lock()
		e.prepareObservedTokens = append(e.prepareObservedTokens, testStringValue(auth.Metadata["access_token"]))
		e.prepareObservedIDs = append(e.prepareObservedIDs, auth.ID)
		e.mu.Unlock()
	}
	if e.prepareFn != nil {
		return e.prepareFn(ctx, auth)
	}
	if e.prepareStarted != nil {
		e.prepareOnce.Do(func() { close(e.prepareStarted) })
	}
	if e.releasePrepare != nil {
		select {
		case <-e.releasePrepare:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = "prepared-project-" + testStringValue(updated.Metadata["access_token"])
	return updated, nil
}

func (e *issue7727Executor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.inRefresh.Store(true)
	defer e.inRefresh.Store(false)
	if e.refreshStarted != nil {
		e.refreshOnce.Do(func() { close(e.refreshStarted) })
	}
	if e.releaseRefresh != nil {
		select {
		case <-e.releaseRefresh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = "refreshed-token"
	if e.refreshSetsPID {
		updated.Metadata["project_id"] = "project-from-refresh"
	}
	return updated, nil
}

func (e *issue7727Executor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *issue7727Executor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *issue7727Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *issue7727Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func waitForRefreshLockWaiters(t *testing.T, manager *Manager, id string, want int32, abortCh <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if abortCh != nil {
			select {
			case <-abortCh:
				t.Fatalf("caller finished without blocking on refreshLocks[%q]", id)
			default:
			}
		}
		if lockValue, ok := manager.refreshLocks.Load(id); ok {
			if lock, okLock := lockValue.(*authRefreshLock); okLock && lock != nil && lock.waiters.Load() >= want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d waiters on refreshLocks[%q]", want, id)
		}
		runtime.Gosched()
	}
}

func waitForHomePrepareFlightWaiters(t *testing.T, manager *Manager, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		manager.homePrepareMu.Lock()
		flight := manager.homePrepareFlights[key]
		waiters := 0
		if flight != nil {
			waiters = flight.waiters
		}
		manager.homePrepareMu.Unlock()
		if waiters >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d home prepare flight waiters (got %d)", want, waiters)
		}
		runtime.Gosched()
	}
}

func TestIssue7727_AntigravityPrepareRequestAuthSerializesWithRefreshLock(t *testing.T) {
	t.Run("waits for refresh and skips prepare when refresh populates project_id", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		exec := &issue7727Executor{
			provider:       "antigravity",
			refreshStarted: make(chan struct{}),
			releaseRefresh: make(chan struct{}),
			refreshSetsPID: true,
		}
		manager.RegisterExecutor(exec)

		auth := &Auth{
			ID:       "ag-serialize-skip",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "old-token",
				"refresh_token": "rt-1",
			},
		}
		if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		refreshDone := make(chan error, 1)
		go func() {
			_, errRef := manager.refreshAuthForRequest(ctx, auth.ID, "")
			refreshDone <- errRef
		}()
		<-exec.refreshStarted

		type prepareOutcome struct {
			auth *Auth
			err  error
		}
		prepareDone := make(chan prepareOutcome, 1)
		prepareFinished := make(chan struct{})
		go func() {
			prepared, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
			prepareDone <- prepareOutcome{auth: prepared, err: errPrep}
			close(prepareFinished)
		}()

		// Deterministically confirm PrepareRequestAuth is blocked on m.refreshLocks[id] before releasing Refresh.
		waitForRefreshLockWaiters(t, manager, auth.ID, 1, prepareFinished)

		close(exec.releaseRefresh)
		if errRef := <-refreshDone; errRef != nil {
			t.Fatalf("refreshAuthForRequest error: %v", errRef)
		}
		outcome := <-prepareDone
		if outcome.err != nil {
			t.Fatalf("PrepareRequestAuth error: %v", outcome.err)
		}
		if exec.prepareOverlapDetected.Load() {
			t.Fatal("PrepareRequestAuth ran concurrently with Refresh")
		}
		if got := exec.prepareCalls.Load(); got != 0 {
			t.Fatalf("PrepareRequestAuth calls = %d, want 0 (Refresh already populated project_id)", got)
		}
		if got := testStringValue(outcome.auth.Metadata["access_token"]); got != "refreshed-token" {
			t.Fatalf("access_token = %q, want refreshed-token", got)
		}
		if got := testStringValue(outcome.auth.Metadata["project_id"]); got != "project-from-refresh" {
			t.Fatalf("project_id = %q, want project-from-refresh", got)
		}
	})

	t.Run("waits for refresh and sees refreshed token before preparing", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		exec := &issue7727Executor{
			provider:       "antigravity",
			refreshStarted: make(chan struct{}),
			releaseRefresh: make(chan struct{}),
			refreshSetsPID: false,
		}
		manager.RegisterExecutor(exec)

		auth := &Auth{
			ID:       "ag-serialize-token",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "old-token",
				"refresh_token": "rt-2",
			},
		}
		if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
			t.Fatalf("Register error: %v", errReg)
		}

		refreshDone := make(chan error, 1)
		go func() {
			_, errRef := manager.refreshAuthForRequest(ctx, auth.ID, "")
			refreshDone <- errRef
		}()
		<-exec.refreshStarted

		type prepareOutcome struct {
			auth *Auth
			err  error
		}
		prepareDone := make(chan prepareOutcome, 1)
		prepareFinished := make(chan struct{})
		go func() {
			prepared, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
			prepareDone <- prepareOutcome{auth: prepared, err: errPrep}
			close(prepareFinished)
		}()

		// Deterministically confirm PrepareRequestAuth is blocked on m.refreshLocks[id] before releasing Refresh.
		waitForRefreshLockWaiters(t, manager, auth.ID, 1, prepareFinished)

		close(exec.releaseRefresh)
		if errRef := <-refreshDone; errRef != nil {
			t.Fatalf("refreshAuthForRequest error: %v", errRef)
		}
		outcome := <-prepareDone
		if outcome.err != nil {
			t.Fatalf("PrepareRequestAuth error: %v", outcome.err)
		}
		if exec.prepareOverlapDetected.Load() {
			t.Fatal("PrepareRequestAuth overlapped with Refresh")
		}
		exec.mu.Lock()
		tokens := append([]string(nil), exec.prepareObservedTokens...)
		exec.mu.Unlock()
		if len(tokens) != 1 || tokens[0] != "refreshed-token" {
			t.Fatalf("PrepareRequestAuth observed tokens = %v, want [refreshed-token]", tokens)
		}
	})
}

func TestIssue7727_PrepareRequestAuthAbortsWhenCredentialUnregistered(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	exec := &issue7727Executor{
		provider:       "antigravity",
		refreshStarted: make(chan struct{}),
		releaseRefresh: make(chan struct{}),
	}
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "ag-unregistered-while-waiting",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "rt-unreg",
		},
	}
	if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, errRef := manager.refreshAuthForRequest(ctx, auth.ID, "")
		refreshDone <- errRef
	}()
	<-exec.refreshStarted

	type prepareOutcome struct {
		auth *Auth
		err  error
	}
	prepareDone := make(chan prepareOutcome, 1)
	prepareFinished := make(chan struct{})
	go func() {
		prepared, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
		prepareDone <- prepareOutcome{auth: prepared, err: errPrep}
		close(prepareFinished)
	}()

	// Ensure PrepareRequestAuth is already waiting on the lock before removing the credential.
	waitForRefreshLockWaiters(t, manager, auth.ID, 1, prepareFinished)

	manager.Remove(ctx, auth.ID)

	close(exec.releaseRefresh)
	<-refreshDone

	outcome := <-prepareDone
	if outcome.err == nil {
		t.Fatalf("PrepareRequestAuth succeeded for unregistered credential: auth=%#v", outcome.auth)
	}
	if got := exec.prepareCalls.Load(); got != 0 {
		t.Fatalf("preparer.PrepareRequestAuth calls = %d, want 0 for unregistered credential", got)
	}
}

func TestIssue7727_ExecutePathsDoNotPanicWhenCredentialRemovedDuringPrepare(t *testing.T) {
	const model = "issue7727-model"
	paths := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{
			name: "Execute",
			run: func(m *Manager, ctx context.Context) error {
				_, err := m.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "ExecuteCount",
			run: func(m *Manager, ctx context.Context) error {
				_, err := m.ExecuteCount(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "ExecuteStream",
			run: func(m *Manager, ctx context.Context) error {
				res, err := m.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if err != nil {
					return err
				}
				for range res.Chunks {
				}
				return nil
			},
		},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			exec := &issue7727Executor{
				provider: "antigravity",
				prepareFn: func(_ context.Context, a *Auth) (*Auth, error) {
					// Simulate credential deletion mid-preparation so UpdatePreparedAuth returns (nil, nil).
					manager.Remove(ctx, a.ID)
					updated := a.Clone()
					updated.Metadata["project_id"] = "prepared-project"
					return updated, nil
				},
			}
			manager.RegisterExecutor(exec)

			auth := &Auth{
				ID:       "ag-removed-mid-prepare-" + tc.name,
				Provider: "antigravity",
				Status:   StatusActive,
				Metadata: map[string]any{"access_token": "tok"},
			}
			if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
				t.Fatalf("Register error: %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

			errRun := tc.run(manager, ctx)
			if errRun == nil {
				t.Fatalf("%s expected error when credential removed during preparation, got nil", tc.name)
			}
		})
	}
}

func TestIssue7727_PrepareRequestAuthHonorsContextCancellationWhileWaitingForLock(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	exec := &issue7727Executor{
		provider:       "antigravity",
		prepareStarted: make(chan struct{}),
		releasePrepare: make(chan struct{}),
	}
	manager.RegisterExecutor(exec)

	auth := &Auth{
		ID:       "ag-cancel-lock-wait",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "tok"},
	}
	if _, errReg := manager.Register(WithSkipPersist(ctx), auth); errReg != nil {
		t.Fatalf("Register error: %v", errReg)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, errPrep := manager.PrepareRequestAuth(ctx, exec, auth.Clone())
		firstDone <- errPrep
	}()
	<-exec.prepareStarted

	waitCtx, cancelWait := context.WithCancel(ctx)
	secondDone := make(chan error, 1)
	secondFinished := make(chan struct{})
	go func() {
		_, errPrep := manager.PrepareRequestAuth(waitCtx, exec, auth.Clone())
		secondDone <- errPrep
		close(secondFinished)
	}()
	waitForRefreshLockWaiters(t, manager, auth.ID, 1, secondFinished)
	cancelWait()

	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case errSecond := <-secondDone:
		if !errors.Is(errSecond, context.Canceled) {
			close(exec.releasePrepare)
			<-firstDone
			t.Fatalf("second PrepareRequestAuth error = %v, want context.Canceled", errSecond)
		}
	case <-timer.C:
		close(exec.releasePrepare)
		<-firstDone
		<-secondDone
		t.Fatal("canceled PrepareRequestAuth remained blocked waiting on refresh lock")
	}

	close(exec.releasePrepare)
	if errFirst := <-firstDone; errFirst != nil {
		t.Fatalf("first PrepareRequestAuth error: %v", errFirst)
	}
}

func TestIssue7727_HomePrepareAuthSnapshotCoalescesAndHonorsContextCancellation(t *testing.T) {
	t.Run("coalesces concurrent identical snapshots and deeply isolates returned clones", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		started := make(chan struct{})
		release := make(chan struct{})
		var startOnce sync.Once
		exec := &issue7727Executor{
			provider: "antigravity",
			prepareFn: func(_ context.Context, a *Auth) (*Auth, error) {
				startOnce.Do(func() { close(started) })
				<-release
				updated := a.Clone()
				updated.Metadata["project_id"] = "prepared-project-home-token"
				updated.Metadata["nested_items"] = []any{
					map[string]any{"k": "original", "arr": []any{map[string]any{"inner": "v1"}}},
				}
				return updated, nil
			},
		}

		// Register same ID locally to verify Home snapshot preparation never mutates local state.
		localAuth := &Auth{
			ID:       "home-coalesce-id",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "local-token"},
		}
		if _, errReg := manager.Register(WithSkipPersist(ctx), localAuth); errReg != nil {
			t.Fatalf("Register local auth error: %v", errReg)
		}

		homeAuth := &Auth{
			ID:       "home-coalesce-id",
			Index:    "home-idx-1",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token": "home-token",
				"existing_arr": []any{map[string]any{"tag": "base"}},
			},
		}
		key := homeAuthPrepareKey(ctx, homeAuth)

		const concurrency = 4
		results := make([]*Auth, concurrency)
		errs := make([]error, concurrency)
		var wg sync.WaitGroup
		wg.Add(concurrency)

		callerStorages := make([]*issue7727TokenStorage, concurrency)
		for i := 0; i < concurrency; i++ {
			callerStorages[i] = &issue7727TokenStorage{slot: i}
		}

		go func() {
			defer wg.Done()
			snap := homeAuth.Clone()
			snap.UpdatedAt = time.Unix(100, 0)
			snap.Runtime = func() int { return 0 }
			snap.Storage = callerStorages[0]
			results[0], errs[0] = manager.prepareHomeAuthSnapshot(ctx, exec, snap, false)
		}()
		<-started

		for i := 1; i < concurrency; i++ {
			idx := i
			go func() {
				defer wg.Done()
				snap := homeAuth.Clone()
				snap.UpdatedAt = time.Unix(int64(100+idx), 0)
				snap.Runtime = func() int { return idx }
				snap.Storage = callerStorages[idx]
				results[idx], errs[idx] = manager.prepareHomeAuthSnapshot(ctx, exec, snap, false)
			}()
		}

		waitForHomePrepareFlightWaiters(t, manager, key, concurrency)
		close(release)
		wg.Wait()

		if got := exec.prepareCalls.Load(); got != 1 {
			t.Fatalf("Home prepare calls = %d, want 1 (coalesced across %d concurrent callers)", got, concurrency)
		}
		for i := 0; i < concurrency; i++ {
			if errs[i] != nil {
				t.Fatalf("caller %d error: %v", i, errs[i])
			}
			if results[i] == nil {
				t.Fatalf("caller %d got nil auth", i)
			}
			if got := testStringValue(results[i].Metadata["project_id"]); got != "prepared-project-home-token" {
				t.Fatalf("caller %d project_id = %q, want prepared-project-home-token", i, got)
			}
			if wantUpdated := time.Unix(int64(100+i), 0); !results[i].UpdatedAt.Equal(wantUpdated) {
				t.Fatalf("caller %d UpdatedAt = %v, want %v (caller snapshot fields must be preserved)", i, results[i].UpdatedAt, wantUpdated)
			}
			gotRuntimeFn, okRuntime := results[i].Runtime.(func() int)
			if !okRuntime || gotRuntimeFn == nil || gotRuntimeFn() != i {
				t.Fatalf("caller %d Runtime func did not preserve caller's own runtime (ok=%t)", i, okRuntime)
			}
			if gotStorage, okStorage := results[i].Storage.(*issue7727TokenStorage); !okStorage || gotStorage != callerStorages[i] {
				t.Fatalf("caller %d Storage = %#v, want caller's own storage pointer", i, results[i].Storage)
			}
		}
		// Ensure top-level and nested objects inside []any are deeply cloned and isolated across callers.
		results[0].Metadata["project_id"] = "mutated"
		nested0 := results[0].Metadata["nested_items"].([]any)[0].(map[string]any)
		nested0["k"] = "mutated-nested"
		nested0["arr"].([]any)[0].(map[string]any)["inner"] = "mutated-deep"
		results[0].Metadata["existing_arr"].([]any)[0].(map[string]any)["tag"] = "mutated-existing"

		if got := testStringValue(results[1].Metadata["project_id"]); got != "prepared-project-home-token" {
			t.Fatalf("top-level mutation leaked across callers: got %q", got)
		}
		nested1 := results[1].Metadata["nested_items"].([]any)[0].(map[string]any)
		if got := testStringValue(nested1["k"]); got != "original" {
			t.Fatalf("nested []any map mutation leaked across callers: got %q, want original", got)
		}
		if got := testStringValue(nested1["arr"].([]any)[0].(map[string]any)["inner"]); got != "v1" {
			t.Fatalf("deeply nested []any map mutation leaked across callers: got %q, want v1", got)
		}
		if got := testStringValue(results[1].Metadata["existing_arr"].([]any)[0].(map[string]any)["tag"]); got != "base" {
			t.Fatalf("existing []any map mutation leaked across callers: got %q, want base", got)
		}

		// Verify local auth remained untouched.
		currentLocal, ok := manager.GetByID(localAuth.ID)
		if !ok || testStringValue(currentLocal.Metadata["access_token"]) != "local-token" || testStringValue(currentLocal.Metadata["project_id"]) != "" {
			t.Fatalf("Home prepare mutated local auth: %#v", currentLocal)
		}
	})

	t.Run("distinct Home auth Index values for same ID and token do not coalesce", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		startedA := make(chan struct{})
		releaseA := make(chan struct{})
		exec := &issue7727Executor{
			provider: "antigravity",
			prepareFn: func(_ context.Context, a *Auth) (*Auth, error) {
				if a.Index == "home-idx-a" {
					close(startedA)
					<-releaseA
				}
				updated := a.Clone()
				updated.Metadata["project_id"] = "project-" + a.Index
				return updated, nil
			},
		}

		authA := &Auth{ID: "same-home-id", Index: "home-idx-a", Provider: "antigravity", Status: StatusActive, Metadata: map[string]any{"access_token": "same-token"}}
		authB := &Auth{ID: "same-home-id", Index: "home-idx-b", Provider: "antigravity", Status: StatusActive, Metadata: map[string]any{"access_token": "same-token"}}

		doneA := make(chan *Auth, 1)
		go func() {
			prepared, errA := manager.prepareHomeAuthSnapshot(ctx, exec, authA, false)
			if errA != nil {
				t.Errorf("prepare A error: %v", errA)
			}
			doneA <- prepared
		}()
		<-startedA

		preparedB, errB := manager.prepareHomeAuthSnapshot(ctx, exec, authB, false)
		if errB != nil {
			close(releaseA)
			<-doneA
			t.Fatalf("prepare B error: %v", errB)
		}
		close(releaseA)
		preparedA := <-doneA

		if got := exec.prepareCalls.Load(); got != 2 {
			t.Fatalf("Home prepare calls = %d, want 2 for distinct Home indexes", got)
		}
		if preparedA.Index != "home-idx-a" || testStringValue(preparedA.Metadata["project_id"]) != "project-home-idx-a" {
			t.Fatalf("preparedA = (index=%q, project_id=%q), want (home-idx-a, project-home-idx-a)", preparedA.Index, testStringValue(preparedA.Metadata["project_id"]))
		}
		if preparedB.Index != "home-idx-b" || testStringValue(preparedB.Metadata["project_id"]) != "project-home-idx-b" {
			t.Fatalf("preparedB = (index=%q, project_id=%q), want (home-idx-b, project-home-idx-b)", preparedB.Index, testStringValue(preparedB.Metadata["project_id"]))
		}
	})

	t.Run("distinct snapshots for same ID do not coalesce", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		startedA := make(chan struct{})
		releaseA := make(chan struct{})
		exec := &issue7727Executor{
			provider: "antigravity",
			prepareFn: func(_ context.Context, a *Auth) (*Auth, error) {
				token := testStringValue(a.Metadata["access_token"])
				if token == "token-a" {
					close(startedA)
					<-releaseA
				}
				updated := a.Clone()
				updated.Metadata["project_id"] = "project-" + token
				return updated, nil
			},
		}

		authA := &Auth{ID: "same-home-id", Provider: "antigravity", Status: StatusActive, Metadata: map[string]any{"access_token": "token-a"}}
		authB := &Auth{ID: "same-home-id", Provider: "antigravity", Status: StatusActive, Metadata: map[string]any{"access_token": "token-b"}}

		doneA := make(chan *Auth, 1)
		go func() {
			prepared, errA := manager.prepareHomeAuthSnapshot(ctx, exec, authA, false)
			if errA != nil {
				t.Errorf("prepare A error: %v", errA)
			}
			doneA <- prepared
		}()
		<-startedA

		preparedB, errB := manager.prepareHomeAuthSnapshot(ctx, exec, authB, false)
		if errB != nil {
			close(releaseA)
			<-doneA
			t.Fatalf("prepare B error: %v", errB)
		}
		close(releaseA)
		preparedA := <-doneA

		if got := exec.prepareCalls.Load(); got != 2 {
			t.Fatalf("Home prepare calls = %d, want 2 for distinct snapshots", got)
		}
		if got := testStringValue(preparedA.Metadata["project_id"]); got != "project-token-a" {
			t.Fatalf("preparedA project_id = %q, want project-token-a", got)
		}
		if got := testStringValue(preparedB.Metadata["project_id"]); got != "project-token-b" {
			t.Fatalf("preparedB project_id = %q, want project-token-b", got)
		}
	})

	t.Run("recovers panic inside flight goroutine without leaking credentials or crashing process", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		started := make(chan struct{})
		release := make(chan struct{})
		const secretToken = "super-secret-oauth-token-value"
		exec := &issue7727Executor{
			provider: "antigravity",
			prepareFn: func(_ context.Context, _ *Auth) (*Auth, error) {
				close(started)
				<-release
				panic("boom with " + secretToken)
			},
		}

		homeAuth := &Auth{
			ID:       "home-panic-id",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": secretToken},
		}
		key := homeAuthPrepareKey(ctx, homeAuth)

		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, errs[0] = manager.prepareHomeAuthSnapshot(ctx, exec, homeAuth.Clone(), false)
		}()
		<-started
		go func() {
			defer wg.Done()
			_, errs[1] = manager.prepareHomeAuthSnapshot(ctx, exec, homeAuth.Clone(), false)
		}()
		waitForHomePrepareFlightWaiters(t, manager, key, 2)
		close(release)
		wg.Wait()

		for i, errItem := range errs {
			if errItem == nil {
				t.Fatalf("caller %d error = nil, want recovered panic error", i)
			}
			if strings.Contains(errItem.Error(), secretToken) {
				t.Fatalf("caller %d error leaked credential material: %q", i, errItem.Error())
			}
		}
		manager.homePrepareMu.Lock()
		_, stillPresent := manager.homePrepareFlights[key]
		manager.homePrepareMu.Unlock()
		if stillPresent {
			t.Fatal("panicked flight was not cleaned up from homePrepareFlights")
		}
	})

	t.Run("canceled waiter exits promptly without aborting active leader", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		exec := &issue7727Executor{
			provider:       "antigravity",
			prepareStarted: make(chan struct{}),
			releasePrepare: make(chan struct{}),
		}

		homeAuth := &Auth{
			ID:       "home-cancel-id",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "home-token"},
		}
		key := homeAuthPrepareKey(ctx, homeAuth)

		leaderDone := make(chan error, 1)
		go func() {
			_, errPrep := manager.prepareHomeAuthSnapshot(ctx, exec, homeAuth.Clone(), false)
			leaderDone <- errPrep
		}()
		<-exec.prepareStarted

		waitCtx, cancelWait := context.WithCancel(ctx)
		waiterDone := make(chan error, 1)
		go func() {
			_, errPrep := manager.prepareHomeAuthSnapshot(waitCtx, exec, homeAuth.Clone(), false)
			waiterDone <- errPrep
		}()
		waitForHomePrepareFlightWaiters(t, manager, key, 2)
		cancelWait()

		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case errWaiter := <-waiterDone:
			if !errors.Is(errWaiter, context.Canceled) {
				close(exec.releasePrepare)
				<-leaderDone
				t.Fatalf("canceled waiter error = %v, want context.Canceled", errWaiter)
			}
		case <-timer.C:
			close(exec.releasePrepare)
			<-leaderDone
			<-waiterDone
			t.Fatal("canceled prepareHomeAuthSnapshot waiter remained blocked behind leader")
		}

		close(exec.releasePrepare)
		if errLeader := <-leaderDone; errLeader != nil {
			t.Fatalf("leader error = %v, want nil (waiter cancel must not abort active leader)", errLeader)
		}
	})

	t.Run("canceling all callers cancels underlying flight context", func(t *testing.T) {
		ctx := context.Background()
		manager := NewManager(nil, nil, nil)
		started := make(chan struct{})
		leaderCanceled := make(chan struct{})
		exec := &issue7727Executor{
			provider: "antigravity",
			prepareFn: func(callCtx context.Context, _ *Auth) (*Auth, error) {
				close(started)
				<-callCtx.Done()
				close(leaderCanceled)
				return nil, callCtx.Err()
			},
		}

		homeAuth := &Auth{
			ID:       "home-cancel-all-id",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "home-token"},
		}
		key := homeAuthPrepareKey(ctx, homeAuth)

		ctx1, cancel1 := context.WithCancel(ctx)
		ctx2, cancel2 := context.WithCancel(ctx)
		done1 := make(chan error, 1)
		done2 := make(chan error, 1)

		go func() {
			_, err1 := manager.prepareHomeAuthSnapshot(ctx1, exec, homeAuth.Clone(), false)
			done1 <- err1
		}()
		<-started

		go func() {
			_, err2 := manager.prepareHomeAuthSnapshot(ctx2, exec, homeAuth.Clone(), false)
			done2 <- err2
		}()
		waitForHomePrepareFlightWaiters(t, manager, key, 2)

		cancel1()
		if err1 := <-done1; !errors.Is(err1, context.Canceled) {
			cancel2()
			t.Fatalf("caller 1 error = %v, want context.Canceled", err1)
		}
		select {
		case <-leaderCanceled:
			cancel2()
			t.Fatal("underlying flight canceled while caller 2 was still active")
		default:
		}

		cancel2()
		if err2 := <-done2; !errors.Is(err2, context.Canceled) {
			t.Fatalf("caller 2 error = %v, want context.Canceled", err2)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-leaderCanceled:
		case <-timer.C:
			t.Fatal("underlying flight context was not canceled after all callers canceled")
		}
	})

	t.Run("adopts replaced closures from same literal and handles comparable structs with uncomparable interface fields", func(t *testing.T) {
		type dynamicBox struct {
			X any
		}

		// Same function literal capturing different variables must be detected as replaced
		// for both Runtime and func-typed Storage.
		baseRuntime := makeIssue7727RuntimeClosure(1)
		replacedRuntime := makeIssue7727RuntimeClosure(2)
		callerRuntime := makeIssue7727RuntimeClosure(99)

		baseStorage := makeIssue7727FuncStorage("leader-base")
		replacedStorage := makeIssue7727FuncStorage("prepared-new")
		callerStorage := makeIssue7727FuncStorage("caller-own")

		base := &Auth{
			ID:       "home-closure-replace",
			Provider: "antigravity",
			Metadata: map[string]any{"access_token": "tok"},
			Runtime:  baseRuntime,
			Storage:  baseStorage,
		}
		current := base.Clone()
		current.Runtime = callerRuntime
		current.Storage = callerStorage

		preparedReplaced := base.Clone()
		preparedReplaced.Metadata["project_id"] = "p-new"
		preparedReplaced.Runtime = replacedRuntime
		preparedReplaced.Storage = replacedStorage

		mergedReplaced := mergeHomePreparedSnapshot(base, current, preparedReplaced)
		gotRT, okRT := mergedReplaced.Runtime.(func() int)
		if !okRT || gotRT == nil || gotRT() != 2 {
			t.Fatalf("replaced Runtime closure was not adopted: ok=%t val=%v", okRT, func() int {
				if gotRT == nil {
					return -1
				}
				return gotRT()
			}())
		}
		gotST, okST := mergedReplaced.Storage.(issue7727FuncTokenStorage)
		if !okST || gotST == nil || gotST() != "prepared-new" {
			t.Fatalf("replaced func Storage closure was not adopted: ok=%t", okST)
		}

		// Unchanged func-typed Storage must preserve each caller's own closure.
		preparedUnchanged := base.Clone()
		preparedUnchanged.Metadata["project_id"] = "p-same"
		mergedUnchanged := mergeHomePreparedSnapshot(base, current, preparedUnchanged)
		gotUnchangedRT, okURT := mergedUnchanged.Runtime.(func() int)
		if !okURT || gotUnchangedRT == nil || gotUnchangedRT() != 99 {
			t.Fatalf("unchanged Runtime closure did not preserve caller runtime: ok=%t", okURT)
		}
		gotUnchangedST, okUST := mergedUnchanged.Storage.(issue7727FuncTokenStorage)
		if !okUST || gotUnchangedST == nil || gotUnchangedST() != "caller-own" {
			t.Fatalf("unchanged func Storage closure did not preserve caller storage: ok=%t", okUST)
		}

		// Struct whose static type is comparable but contains an uncomparable dynamic value
		// in an interface field must not panic on comparison.
		baseBox := dynamicBox{X: []int{1}}
		callerBox := dynamicBox{X: []int{9}}
		replacedBox := dynamicBox{X: []int{2}}

		baseWithBox := &Auth{
			ID:       "home-box-runtime",
			Provider: "antigravity",
			Metadata: map[string]any{"access_token": "tok"},
			Runtime:  baseBox,
		}
		currentWithBox := baseWithBox.Clone()
		currentWithBox.Runtime = callerBox

		preparedSameBox := baseWithBox.Clone()
		preparedSameBox.Metadata["project_id"] = "p-box-same"
		mergedSameBox := mergeHomePreparedSnapshot(baseWithBox, currentWithBox, preparedSameBox)
		gotSameBox, okSameBox := mergedSameBox.Runtime.(dynamicBox)
		if !okSameBox || len(gotSameBox.X.([]int)) != 1 || gotSameBox.X.([]int)[0] != 9 {
			t.Fatalf("unchanged dynamicBox Runtime did not preserve caller box: %#v", mergedSameBox.Runtime)
		}

		// Reconstructed struct with different slice must also not panic and must adopt replacement.
		preparedNewBox := baseWithBox.Clone()
		preparedNewBox.Runtime = replacedBox
		mergedNewBox := mergeHomePreparedSnapshot(baseWithBox, currentWithBox, preparedNewBox)
		gotNewBox, okNewBox := mergedNewBox.Runtime.(dynamicBox)
		if !okNewBox || len(gotNewBox.X.([]int)) != 1 || gotNewBox.X.([]int)[0] != 2 {
			t.Fatalf("replaced dynamicBox Runtime was not adopted: %#v", mergedNewBox.Runtime)
		}

		// Struct with an unexported interface field holding a closure, boxed separately into
		// base and prepared snapshots, must preserve caller Runtime when the closure is identical
		// and adopt prepared Runtime when replaced by a different closure from the same literal.
		type unexportedBox struct {
			x any
		}
		baseUnexported := &Auth{
			ID:       "home-unexported-box",
			Provider: "antigravity",
			Metadata: map[string]any{"access_token": "tok"},
			Runtime:  unexportedBox{x: baseRuntime},
		}
		currentUnexported := baseUnexported.Clone()
		currentUnexported.Runtime = unexportedBox{x: callerRuntime}

		// Re-box the exact same baseRuntime closure into a fresh unexportedBox interface value.
		preparedReboxedSame := baseUnexported.Clone()
		preparedReboxedSame.Runtime = unexportedBox{x: baseRuntime}
		mergedReboxedSame := mergeHomePreparedSnapshot(baseUnexported, currentUnexported, preparedReboxedSame)
		gotReboxedSame, okReboxedSame := mergedReboxedSame.Runtime.(unexportedBox)
		if !okReboxedSame || gotReboxedSame.x.(func() int)() != 99 {
			t.Fatalf("reboxed identical closure in unexported interface field did not preserve caller runtime: %#v", mergedReboxedSame.Runtime)
		}

		preparedReboxedNew := baseUnexported.Clone()
		preparedReboxedNew.Runtime = unexportedBox{x: replacedRuntime}
		mergedReboxedNew := mergeHomePreparedSnapshot(baseUnexported, currentUnexported, preparedReboxedNew)
		gotReboxedNew, okReboxedNew := mergedReboxedNew.Runtime.(unexportedBox)
		if !okReboxedNew || gotReboxedNew.x.(func() int)() != 2 {
			t.Fatalf("replaced closure in unexported interface field was not adopted: %#v", mergedReboxedNew.Runtime)
		}
	})
}
