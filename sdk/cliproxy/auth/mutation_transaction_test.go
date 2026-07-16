package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type transactionTestStore struct {
	mu    sync.Mutex
	fail  bool
	saved map[string]*Auth
}

func (s *transactionTestStore) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Auth, 0, len(s.saved))
	for _, auth := range s.saved {
		out = append(out, auth.Clone())
	}
	return out, nil
}

func (s *transactionTestStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", errors.New("synthetic persistence failure")
	}
	if s.saved == nil {
		s.saved = make(map[string]*Auth)
	}
	s.saved[auth.ID] = auth.Clone()
	return auth.ID, nil
}

func (s *transactionTestStore) Delete(context.Context, string) error { return nil }

func (s *transactionTestStore) PersistMutation(_ context.Context, before, after *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", errors.New("synthetic persistence failure")
	}
	current := s.saved[before.ID]
	if current == nil || current.Revision() != before.Revision() {
		return "", ErrAuthSourceConflict
	}
	s.saved[after.ID] = after.Clone()
	return after.ID, nil
}

type transactionTestHook struct {
	mu        sync.Mutex
	registers int
	updates   int
}

func (h *transactionTestHook) OnAuthRegistered(context.Context, *Auth) {
	h.mu.Lock()
	h.registers++
	h.mu.Unlock()
}

func (h *transactionTestHook) OnAuthUpdated(context.Context, *Auth) {
	h.mu.Lock()
	h.updates++
	h.mu.Unlock()
}

func (*transactionTestHook) OnResult(context.Context, Result) {}

func (h *transactionTestHook) updateCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.updates
}

func (h *transactionTestHook) registerCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.registers
}

func TestManagerUpdatePersistenceFailureLeavesRuntimeAndRevisionUnchanged(t *testing.T) {
	store := &transactionTestStore{}
	hook := &transactionTestHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)

	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	originalRevision := registered.Revision()

	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()

	updated := registered.Clone()
	updated.Attributes["priority"] = "101"
	updated.Metadata["priority"] = float64(101)
	if _, err = manager.Update(context.Background(), updated); err == nil {
		t.Fatal("Update() error = nil, want persistence failure")
	}

	current, ok := manager.GetByID(registered.ID)
	if !ok {
		t.Fatal("GetByID() missing registered auth")
	}
	if got := current.Attributes["priority"]; got != "10" {
		t.Fatalf("runtime priority = %q, want unchanged 10", got)
	}
	if got := current.Revision(); got != originalRevision {
		t.Fatalf("revision = %q, want unchanged %q", got, originalRevision)
	}
	if got := hook.updateCount(); got != 0 {
		t.Fatalf("update hook count = %d, want 0", got)
	}
}

func TestManagerMutatePrioritySetsExactFieldAndRotatesRevision(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)

	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10", "note": "keep"},
		Metadata: map[string]any{
			"type":         "codex",
			"priority":     float64(10),
			"note":         "keep",
			"access_token": "synthetic-token",
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result, err := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if err != nil {
		t.Fatalf("MutatePriority() error = %v", err)
	}
	if result == nil || result.Auth == nil {
		t.Fatal("MutatePriority() returned nil result")
	}
	if result.Revision == registered.Revision() {
		t.Fatalf("revision did not rotate: %q", result.Revision)
	}
	if !result.Priority.Present || result.Priority.Value != 101 {
		t.Fatalf("priority result = %#v, want present value 101", result.Priority)
	}
	if got := result.Auth.Attributes["priority"]; got != "101" {
		t.Fatalf("priority attribute = %q, want 101", got)
	}
	if got := result.Auth.Metadata["priority"]; got != float64(101) {
		t.Fatalf("priority metadata = %#v, want exact numeric value 101", got)
	}
	if got := result.Auth.Metadata["access_token"]; got != "synthetic-token" {
		t.Fatalf("access token changed = %#v", got)
	}
	if got := result.Auth.Metadata["note"]; got != "keep" {
		t.Fatalf("note changed = %#v", got)
	}
}

func TestManagerUpdateRejectsStaleRevisionWithoutOverwritingCurrentState(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)

	registered, err := manager.Register(context.Background(), &Auth{
		ID:       "synthetic-auth.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "note": "base"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	stale := registered.Clone()

	newer := registered.Clone()
	newer.Metadata["note"] = "newer"
	newer, err = manager.Update(context.Background(), newer)
	if err != nil {
		t.Fatalf("first Update() error = %v", err)
	}

	stale.Metadata["note"] = "stale"
	if _, err = manager.Update(context.Background(), stale); !errors.Is(err, ErrAuthRevisionConflict) {
		t.Fatalf("stale Update() error = %v, want ErrAuthRevisionConflict", err)
	}

	current, ok := manager.GetByID(registered.ID)
	if !ok {
		t.Fatal("GetByID() missing auth")
	}
	if got := current.Metadata["note"]; got != "newer" {
		t.Fatalf("current note = %#v, want newer", got)
	}
	if got := current.Revision(); got != newer.Revision() {
		t.Fatalf("current revision = %q, want %q", got, newer.Revision())
	}
}

type blockingRefreshExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingRefreshExecutor) Identifier() string { return "codex" }

func (*blockingRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*blockingRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *blockingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	auth.Metadata["access_token"] = "refreshed-token"
	return auth, nil
}

func (*blockingRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*blockingRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshOwnsPriorityMutationLockUntilCommit(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)
	executor := &blockingRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})}
	manager.RegisterExecutor(executor)

	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"type":          "codex",
			"priority":      float64(10),
			"access_token":  "old-token",
			"refresh_token": "synthetic-refresh",
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := manager.refreshAuthForRequest(context.Background(), registered.ID, "")
		refreshDone <- errRefresh
	}()
	<-executor.started

	priorityDone := make(chan error, 1)
	go func() {
		_, errPriority := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
			Operation: PriorityMutationSet,
			Priority:  101,
		})
		priorityDone <- errPriority
	}()

	select {
	case errPriority := <-priorityDone:
		t.Fatalf("priority mutation completed before refresh commit: %v", errPriority)
	case <-time.After(50 * time.Millisecond):
	}
	close(executor.release)

	if err = <-refreshDone; err != nil {
		t.Fatalf("refreshAuthForRequest() error = %v", err)
	}
	if err = <-priorityDone; !errors.Is(err, ErrAuthRevisionConflict) {
		t.Fatalf("MutatePriority() error = %v, want ErrAuthRevisionConflict", err)
	}

	current, ok := manager.GetByID(registered.ID)
	if !ok {
		t.Fatal("GetByID() missing auth")
	}
	if got := current.Metadata["access_token"]; got != "refreshed-token" {
		t.Fatalf("access_token = %#v, want refreshed-token", got)
	}
	if got := current.Attributes["priority"]; got != "10" {
		t.Fatalf("priority = %q, want unchanged 10", got)
	}
}

func TestManagerRegisterPersistenceFailureDoesNotPublishAuth(t *testing.T) {
	store := &transactionTestStore{fail: true}
	hook := &transactionTestHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)

	registered, err := manager.Register(context.Background(), &Auth{
		ID:       "synthetic-auth.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	})
	if err == nil {
		t.Fatal("Register() error = nil, want persistence failure")
	}
	if registered != nil {
		t.Fatalf("Register() auth = %#v, want nil", registered)
	}
	if _, ok := manager.GetByID("synthetic-auth.json"); ok {
		t.Fatal("failed registration published runtime auth")
	}
	if got := hook.registerCount(); got != 0 {
		t.Fatalf("register hook count = %d, want 0", got)
	}
}

type incompatiblePriorityScheduler struct{}

func (incompatiblePriorityScheduler) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "lower-priority-auth"}, true, nil
}

func TestManagerMutatePriorityRejectsActiveCustomScheduler(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)
	manager.SetPluginScheduler(incompatiblePriorityScheduler{})
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	_, err = manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if !errors.Is(err, ErrPriorityMutationUnsupported) {
		t.Fatalf("MutatePriority() error = %v, want ErrPriorityMutationUnsupported", err)
	}
	current, _ := manager.GetByID(registered.ID)
	if got := current.Attributes["priority"]; got != "10" {
		t.Fatalf("priority = %q, want unchanged 10", got)
	}
}

func TestManagerUpdateSkipsIdenticalPersistedWatcherReplay(t *testing.T) {
	store := &transactionTestStore{}
	hook := &transactionTestHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "101"},
		Metadata: map[string]any{
			"type":         "codex",
			"priority":     101,
			"access_token": "synthetic-token",
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	replay := registered.Clone()
	replay.revision = ""

	updated, err := manager.Update(WithSkipPersist(context.Background()), replay)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Revision() != registered.Revision() {
		t.Fatalf("revision = %q, want unchanged %q", updated.Revision(), registered.Revision())
	}
	if got := hook.updateCount(); got != 0 {
		t.Fatalf("update hook count = %d, want 0", got)
	}
}

func TestManagerMutatePriorityRejectsCodexWebsocketRouting(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10", "websockets": "true"},
		Metadata: map[string]any{
			"type":       "codex",
			"priority":   float64(10),
			"websockets": true,
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	_, err = manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if !errors.Is(err, ErrPriorityMutationRoutingIncompatible) {
		t.Fatalf("MutatePriority() error = %v, want ErrPriorityMutationRoutingIncompatible", err)
	}
}

func TestManagerMutatePriorityChangesFutureSelectionWithoutChangingSelectedClone(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(&blockingRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})})
	first, err := manager.Register(context.Background(), &Auth{
		ID:         "auth-a",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register(auth-a) error = %v", err)
	}
	second, err := manager.Register(context.Background(), &Auth{
		ID:         "auth-b",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "5"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(5)},
	})
	if err != nil {
		t.Fatalf("Register(auth-b) error = %v", err)
	}

	selectedBefore, _, err := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatalf("pickNext() before mutation error = %v", err)
	}
	if selectedBefore.ID != first.ID {
		t.Fatalf("selected before = %q, want %q", selectedBefore.ID, first.ID)
	}
	result, err := manager.MutatePriority(context.Background(), second.ID, second.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if err != nil {
		t.Fatalf("MutatePriority() error = %v", err)
	}
	selectedAfter, _, err := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatalf("pickNext() after mutation error = %v", err)
	}
	if selectedAfter.ID != second.ID {
		t.Fatalf("selected after = %q, want %q", selectedAfter.ID, second.ID)
	}
	if selectedAfter.Revision() != result.Revision {
		t.Fatalf("scheduler revision = %q, want %q", selectedAfter.Revision(), result.Revision)
	}
	if got := selectedBefore.Attributes["priority"]; got != "10" {
		t.Fatalf("already-selected clone priority changed to %q", got)
	}
}

type immediateRefreshExecutor struct{}

func (*immediateRefreshExecutor) Identifier() string { return "codex" }
func (*immediateRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*immediateRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*immediateRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	auth.Metadata["access_token"] = "uncommitted-refreshed-token"
	return auth, nil
}
func (*immediateRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*immediateRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshPersistenceFailureReturnsErrorAndKeepsCommittedCredentials(t *testing.T) {
	const model = "synthetic-persistence-failure-model"
	const provider = "synthetic-persistence-failure-provider"
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(&immediateRefreshExecutor{})
	registered, err := manager.Register(context.Background(), &Auth{
		ID:       "synthetic-auth.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "committed-token",
			"refresh_token": "synthetic-refresh",
		},
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable: true,
				LastError:   &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(registered.ID, provider, []*registry.ModelInfo{{ID: model}})
	modelRegistry.SuspendClientModel(registered.ID, model, "unauthorized")
	t.Cleanup(func() { modelRegistry.UnregisterClient(registered.ID) })

	refreshed, err := manager.refreshAuthForRequest(context.Background(), registered.ID, "")
	if err == nil {
		t.Fatalf("refreshAuthForRequest() auth=%#v error=nil, want persistence failure", refreshed)
	}
	current, _ := manager.GetByID(registered.ID)
	if got := current.Metadata["access_token"]; got != "committed-token" {
		t.Fatalf("committed access_token = %#v, want unchanged", got)
	}
	if available := modelRegistry.GetAvailableModels(provider); len(available) != 0 {
		t.Fatalf("available models = %#v, want failed refresh to keep model suspended", available)
	}
}

func TestManagerMutatePriorityPersistenceFailureLeavesRuntimeRevisionAndHookUnchanged(t *testing.T) {
	store := &transactionTestStore{}
	hook := &transactionTestHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()

	_, err = manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if err == nil {
		t.Fatal("MutatePriority() error = nil, want persistence failure")
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Attributes["priority"] != "10" {
		t.Fatalf("failed mutation changed runtime: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
	if got := hook.updateCount(); got != 0 {
		t.Fatalf("update hook count = %d, want 0", got)
	}
}
