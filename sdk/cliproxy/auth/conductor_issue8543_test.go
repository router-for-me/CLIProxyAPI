package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue8543AuthStore struct {
	mu           sync.Mutex
	savedTokens  map[string]string
	blockByID    map[string]chan struct{}
	enteredByID  map[string]chan struct{}
	enteredOnce  map[string]*sync.Once
	finishedByID map[string]chan struct{}
	finishOnce   map[string]*sync.Once
}

func newIssue8543AuthStore(blockIDs ...string) *issue8543AuthStore {
	s := &issue8543AuthStore{
		savedTokens:  make(map[string]string),
		blockByID:    make(map[string]chan struct{}),
		enteredByID:  make(map[string]chan struct{}),
		enteredOnce:  make(map[string]*sync.Once),
		finishedByID: make(map[string]chan struct{}),
		finishOnce:   make(map[string]*sync.Once),
	}
	for _, id := range blockIDs {
		s.blockByID[id] = make(chan struct{})
		s.enteredByID[id] = make(chan struct{})
		s.enteredOnce[id] = &sync.Once{}
		s.finishedByID[id] = make(chan struct{})
		s.finishOnce[id] = &sync.Once{}
	}
	return s
}

func (s *issue8543AuthStore) List(_ context.Context) ([]*Auth, error) { return nil, nil }

func (s *issue8543AuthStore) Save(_ context.Context, auth *Auth) (string, error) {
	token, _ := auth.Metadata["refresh_token"].(string)
	if token == "rt-rotated" {
		if releaseCh, ok := s.blockByID[auth.ID]; ok {
			s.enteredOnce[auth.ID].Do(func() { close(s.enteredByID[auth.ID]) })
			<-releaseCh
		}
	}
	s.mu.Lock()
	s.savedTokens[auth.ID] = token
	s.mu.Unlock()
	if token == "rt-rotated" {
		if finishCh, ok := s.finishedByID[auth.ID]; ok {
			s.finishOnce[auth.ID].Do(func() { close(finishCh) })
		}
	}
	return auth.ID, nil
}

func (s *issue8543AuthStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.savedTokens, id)
	s.mu.Unlock()
	return nil
}

func (s *issue8543AuthStore) savedToken(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.savedTokens[id]
}

type issue8543AuthExecutor struct{}

func (*issue8543AuthExecutor) Identifier() string { return "issue8543-auth" }

func (*issue8543AuthExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*issue8543AuthExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (*issue8543AuthExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["refresh_token"] = "rt-rotated"
	updated.Metadata["access_token"] = "at-rotated"
	return updated, nil
}

func (*issue8543AuthExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*issue8543AuthExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type issue8543AuthEvaluator struct{}

func (issue8543AuthEvaluator) ShouldRefresh(_ time.Time, auth *Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	tok, _ := auth.Metadata["refresh_token"].(string)
	return tok != "rt-rotated"
}

type issue8543AuthHook struct {
	NoopHook
	matchID string
	onMatch func()
	once    sync.Once
}

func (h *issue8543AuthHook) OnAuthUpdated(_ context.Context, auth *Auth) {
	if auth == nil || auth.ID != h.matchID || auth.Metadata == nil {
		return
	}
	tok, _ := auth.Metadata["refresh_token"].(string)
	if tok == "rt-rotated" {
		h.once.Do(h.onMatch)
	}
}

func TestStopAutoRefreshContextIdleReturnsNilWithCanceledContext(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := m.StopAutoRefreshContext(ctx); err != nil {
		t.Fatalf("StopAutoRefreshContext on idle manager = %v, want nil", err)
	}
}

func TestStopAutoRefreshContextWaitsForPreviousLoopWorkerAfterRestart(t *testing.T) {
	const authID = "cred-restart"
	store := newIssue8543AuthStore(authID)
	defer func() {
		select {
		case <-store.blockByID[authID]:
		default:
			close(store.blockByID[authID])
		}
	}()

	m := NewManager(store, nil, nil)
	m.SetConfig(&internalconfig.Config{AuthAutoRefreshWorkers: 1})
	m.RegisterExecutor(&issue8543AuthExecutor{})

	if _, err := m.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "issue8543-auth",
		Status:   StatusActive,
		Runtime:  issue8543AuthEvaluator{},
		Metadata: map[string]any{
			"access_token":  "at-old",
			"refresh_token": "rt-old",
		},
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	m.StartAutoRefresh(context.Background(), time.Millisecond)

	select {
	case <-store.enteredByID[authID]:
	case <-time.After(5 * time.Second):
		t.Fatal("first loop did not reach store.Save")
	}

	// Restart auto-refresh while first loop's worker is still saving.
	m.StartAutoRefresh(context.Background(), time.Millisecond)

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- m.StopAutoRefreshContext(context.Background())
	}()

	select {
	case err := <-stopDone:
		t.Fatalf("StopAutoRefreshContext returned %v while previous loop worker was still saving", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(store.blockByID[authID])

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopAutoRefreshContext error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopAutoRefreshContext did not return after previous loop save finished")
	}

	if got := store.savedToken(authID); got != "rt-rotated" {
		t.Fatalf("saved refresh_token = %q, want %q", got, "rt-rotated")
	}
}

func TestStopAutoRefreshContextFromHookWaitsForOtherWorkerWithoutDeadlock(t *testing.T) {
	const (
		authHooked = "cred-hooked"
		authSlow   = "cred-slow"
	)
	store := newIssue8543AuthStore(authSlow)
	defer func() {
		select {
		case <-store.blockByID[authSlow]:
		default:
			close(store.blockByID[authSlow])
		}
	}()

	var m *Manager
	hookStarted := make(chan struct{})
	hookStopErr := make(chan error, 1)
	hook := &issue8543AuthHook{
		matchID: authHooked,
		onMatch: func() {
			// Wait until authSlow is confirmed inside store.Save so stopping
			// auto-refresh cannot cancel authSlow before it begins saving.
			<-store.enteredByID[authSlow]
			close(hookStarted)
			hookStopErr <- m.StopAutoRefreshContext(context.Background())
		},
	}

	m = NewManager(store, nil, hook)
	m.SetConfig(&internalconfig.Config{AuthAutoRefreshWorkers: 2})
	m.RegisterExecutor(&issue8543AuthExecutor{})

	for _, id := range []string{authSlow, authHooked} {
		if _, err := m.Register(context.Background(), &Auth{
			ID:       id,
			Provider: "issue8543-auth",
			Status:   StatusActive,
			Runtime:  issue8543AuthEvaluator{},
			Metadata: map[string]any{
				"access_token":  "at-old",
				"refresh_token": "rt-old",
			},
		}); err != nil {
			t.Fatalf("Register(%s) error: %v", id, err)
		}
	}

	m.StartAutoRefresh(context.Background(), time.Millisecond)

	select {
	case <-store.enteredByID[authSlow]:
	case <-time.After(5 * time.Second):
		t.Fatal("slow worker did not reach store.Save")
	}

	select {
	case <-hookStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("hooked worker did not enter OnAuthUpdated")
	}

	// Hook called StopAutoRefreshContext; it must wait for authSlow without deadlocking on authHooked.
	select {
	case err := <-hookStopErr:
		t.Fatalf("StopAutoRefreshContext from hook returned %v while sibling worker was still saving", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(store.blockByID[authSlow])

	select {
	case err := <-hookStopErr:
		if err != nil {
			t.Fatalf("StopAutoRefreshContext from hook error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopAutoRefreshContext from hook did not unblock after sibling save completed")
	}

	if got := store.savedToken(authSlow); got != "rt-rotated" {
		t.Fatalf("saved refresh_token(%s) = %q, want %q", authSlow, got, "rt-rotated")
	}
	if got := store.savedToken(authHooked); got != "rt-rotated" {
		t.Fatalf("saved refresh_token(%s) = %q, want %q", authHooked, got, "rt-rotated")
	}
}

func TestStopAutoRefreshContextCanceledWhileInFlightReturnsWrappedError(t *testing.T) {
	const authID = "cred-cancel"
	store := newIssue8543AuthStore(authID)
	defer func() {
		select {
		case <-store.blockByID[authID]:
		default:
			close(store.blockByID[authID])
		}
	}()

	m := NewManager(store, nil, nil)
	m.SetConfig(&internalconfig.Config{AuthAutoRefreshWorkers: 1})
	m.RegisterExecutor(&issue8543AuthExecutor{})

	if _, err := m.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "issue8543-auth",
		Status:   StatusActive,
		Runtime:  issue8543AuthEvaluator{},
		Metadata: map[string]any{
			"access_token":  "at-old",
			"refresh_token": "rt-old",
		},
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	m.StartAutoRefresh(context.Background(), time.Millisecond)

	select {
	case <-store.enteredByID[authID]:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach store.Save")
	}

	stopCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.StopAutoRefreshContext(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("StopAutoRefreshContext error = %v, want wrapped context.Canceled", err)
	}

	close(store.blockByID[authID])
	select {
	case <-store.finishedByID[authID]:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight save did not finish after release")
	}
}
