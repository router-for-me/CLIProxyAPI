package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type cancelAfterPersistStore struct {
	*transactionTestStore

	mu          sync.Mutex
	cancel      context.CancelFunc
	rollbackErr error
	calls       int
}

func (s *cancelAfterPersistStore) PersistMutation(ctx context.Context, before, after *Auth) (string, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	cancel := s.cancel
	rollbackErr := s.rollbackErr
	s.mu.Unlock()
	if call > 1 && rollbackErr != nil {
		return "", rollbackErr
	}
	path, err := s.transactionTestStore.PersistMutation(ctx, before, after)
	if call == 1 && err == nil && cancel != nil {
		cancel()
	}
	return path, err
}

func TestManagerMutatePriorityCancellationAfterPersistenceRollsBackDurableState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelAfterPersistStore{transactionTestStore: &transactionTestStore{}, cancel: cancel}
	hook := &transactionTestHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)
	manager.RegisterExecutor(&immediateRefreshExecutor{})
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result, err := manager.MutatePriority(ctx, registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MutatePriority() error = %v, want context.Canceled", err)
	}
	if result != nil {
		t.Fatalf("MutatePriority() result = %#v, want nil", result)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Attributes["priority"] != "10" {
		t.Fatalf("runtime changed after rollback: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
	store.transactionTestStore.mu.Lock()
	persisted := store.saved[registered.ID].Clone()
	store.transactionTestStore.mu.Unlock()
	if persisted.Revision() != registered.Revision() || persisted.Attributes["priority"] != "10" {
		t.Fatalf("durable state not rolled back: revision=%q priority=%q", persisted.Revision(), persisted.Attributes["priority"])
	}
	if got := hook.updateCount(); got != 0 {
		t.Fatalf("update hook count = %d, want 0", got)
	}
}

func TestManagerMutatePriorityRollbackFailureReconcilesPersistedState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelAfterPersistStore{
		transactionTestStore: &transactionTestStore{},
		cancel:               cancel,
		rollbackErr:          errors.New("synthetic rollback failure"),
	}
	hook := &transactionTestHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)
	manager.RegisterExecutor(&immediateRefreshExecutor{})
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result, err := manager.MutatePriority(ctx, registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MutatePriority() error = %v, want context.Canceled", err)
	}
	if result != nil {
		t.Fatalf("MutatePriority() result = %#v, want nil", result)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Attributes["priority"] != "101" || current.Metadata["priority"] != float64(101) {
		t.Fatalf("runtime not reconciled to durable mutation: attributes=%q metadata=%#v", current.Attributes["priority"], current.Metadata["priority"])
	}
	if current.Revision() == registered.Revision() {
		t.Fatalf("reconciled revision did not rotate: %q", current.Revision())
	}
	selected, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if selected.Revision() != current.Revision() || selected.Attributes["priority"] != "101" {
		t.Fatalf("scheduler not reconciled: revision=%q priority=%q", selected.Revision(), selected.Attributes["priority"])
	}
	if got := hook.updateCount(); got != 1 {
		t.Fatalf("update hook count = %d, want 1 reconciliation update", got)
	}
}

type panicUpdateHook struct{}

func (panicUpdateHook) OnAuthRegistered(context.Context, *Auth) {}
func (panicUpdateHook) OnAuthUpdated(context.Context, *Auth) {
	panic("synthetic update hook panic")
}
func (panicUpdateHook) OnResult(context.Context, Result) {}

type panicOnceUpdateHook struct {
	mu      sync.Mutex
	updates int
}

func (*panicOnceUpdateHook) OnAuthRegistered(context.Context, *Auth) {}
func (h *panicOnceUpdateHook) OnAuthUpdated(context.Context, *Auth) {
	h.mu.Lock()
	h.updates++
	updates := h.updates
	h.mu.Unlock()
	if updates == 1 {
		panic("synthetic first update hook panic")
	}
}
func (*panicOnceUpdateHook) OnResult(context.Context, Result) {}
func (h *panicOnceUpdateHook) updateCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.updates
}

func TestManagerMutatePriorityPublicationPanicRollsBackDurableAndRuntimeState(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, panicUpdateHook{})
	manager.RegisterExecutor(&immediateRefreshExecutor{})
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result, err := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if err == nil {
		t.Fatal("MutatePriority() error = nil, want publication failure")
	}
	if result != nil {
		t.Fatalf("MutatePriority() result = %#v, want nil", result)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Attributes["priority"] != "10" {
		t.Fatalf("runtime changed after publication rollback: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
	store.mu.Lock()
	persisted := store.saved[registered.ID].Clone()
	store.mu.Unlock()
	if persisted.Revision() != registered.Revision() || persisted.Attributes["priority"] != "10" {
		t.Fatalf("durable state changed after publication rollback: revision=%q priority=%q", persisted.Revision(), persisted.Attributes["priority"])
	}
	selected, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if selected.Revision() != registered.Revision() || selected.Attributes["priority"] != "10" {
		t.Fatalf("scheduler changed after publication rollback: revision=%q priority=%q", selected.Revision(), selected.Attributes["priority"])
	}
}

func TestManagerMutatePriorityPublicationAndRollbackFailureReconcilesAllConsumers(t *testing.T) {
	store := &cancelAfterPersistStore{
		transactionTestStore: &transactionTestStore{},
		rollbackErr:          errors.New("synthetic rollback failure"),
	}
	hook := &panicOnceUpdateHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)
	manager.RegisterExecutor(&immediateRefreshExecutor{})
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result, err := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if err == nil {
		t.Fatal("MutatePriority() error = nil, want publication failure")
	}
	if result != nil {
		t.Fatalf("MutatePriority() result = %#v, want nil", result)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Attributes["priority"] != "101" || current.Metadata["priority"] != float64(101) {
		t.Fatalf("runtime not reconciled: attributes=%q metadata=%#v", current.Attributes["priority"], current.Metadata["priority"])
	}
	selected, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if selected.Revision() != current.Revision() || selected.Attributes["priority"] != "101" {
		t.Fatalf("scheduler not reconciled: revision=%q priority=%q", selected.Revision(), selected.Attributes["priority"])
	}
	if got := hook.updateCount(); got != 2 {
		t.Fatalf("update hook count = %d, want failed publication plus reconciliation", got)
	}
}

func TestManagerUpdateCancellationAfterPersistenceRollsBackDurableState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelAfterPersistStore{transactionTestStore: &transactionTestStore{}, cancel: cancel}
	hook := &transactionTestHook{}
	manager := NewManager(store, &FillFirstSelector{}, hook)
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "claude", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	updated := registered.Clone()
	updated.Attributes["priority"] = "101"
	updated.Metadata["priority"] = float64(101)

	result, err := manager.Update(ctx, updated)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Update() error = %v, want context.Canceled", err)
	}
	if result != nil {
		t.Fatalf("Update() result = %#v, want nil", result)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Attributes["priority"] != "10" {
		t.Fatalf("runtime changed after rollback: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
	store.transactionTestStore.mu.Lock()
	persisted := store.saved[registered.ID].Clone()
	store.transactionTestStore.mu.Unlock()
	if persisted.Revision() != registered.Revision() || persisted.Attributes["priority"] != "10" {
		t.Fatalf("durable state not rolled back: revision=%q priority=%q", persisted.Revision(), persisted.Attributes["priority"])
	}
	if got := hook.updateCount(); got != 0 {
		t.Fatalf("update hook count = %d, want 0", got)
	}
}

func TestManagerUpdatePublicationPanicRollsBackDurableAndRuntimeState(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, panicUpdateHook{})
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "synthetic-auth.json",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "claude", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	updated := registered.Clone()
	updated.Attributes["priority"] = "101"
	updated.Metadata["priority"] = float64(101)

	result, err := manager.Update(context.Background(), updated)
	if err == nil {
		t.Fatal("Update() error = nil, want publication failure")
	}
	if result != nil {
		t.Fatalf("Update() result = %#v, want nil", result)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Attributes["priority"] != "10" {
		t.Fatalf("runtime changed after publication rollback: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
	store.mu.Lock()
	persisted := store.saved[registered.ID].Clone()
	store.mu.Unlock()
	if persisted.Revision() != registered.Revision() || persisted.Attributes["priority"] != "10" {
		t.Fatalf("durable state changed after publication rollback: revision=%q priority=%q", persisted.Revision(), persisted.Attributes["priority"])
	}
}

type blockingPriorityPersistStore struct {
	*transactionTestStore

	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingPriorityPersistStore) PersistMutation(ctx context.Context, before, after *Auth) (string, error) {
	if before.Attributes["priority"] != after.Attributes["priority"] {
		s.once.Do(func() {
			close(s.started)
			<-s.release
		})
	}
	return s.transactionTestStore.PersistMutation(ctx, before, after)
}

func TestPriorityMutationBeforeConcurrentRefreshPreservesCredentialsAndPriorityOutcome(t *testing.T) {
	tests := []struct {
		name          string
		mutation      PriorityMutation
		wantPresent   bool
		wantAttribute string
	}{
		{name: "set", mutation: PriorityMutation{Operation: PriorityMutationSet, Priority: 101}, wantPresent: true, wantAttribute: "101"},
		{name: "unset", mutation: PriorityMutation{Operation: PriorityMutationUnset}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &blockingPriorityPersistStore{
				transactionTestStore: &transactionTestStore{},
				started:              make(chan struct{}),
				release:              make(chan struct{}),
			}
			manager := NewManager(store, &FillFirstSelector{}, nil)
			manager.RegisterExecutor(&immediateRefreshExecutor{})
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

			priorityDone := make(chan error, 1)
			go func() {
				_, errPriority := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), test.mutation)
				priorityDone <- errPriority
			}()
			<-store.started

			refreshDone := make(chan error, 1)
			go func() {
				_, errRefresh := manager.refreshAuthForRequest(context.Background(), registered.ID, "")
				refreshDone <- errRefresh
			}()
			select {
			case errRefresh := <-refreshDone:
				t.Fatalf("refresh completed before priority commit: %v", errRefresh)
			case <-time.After(50 * time.Millisecond):
			}
			close(store.release)
			if err = <-priorityDone; err != nil {
				t.Fatalf("MutatePriority() error = %v", err)
			}
			if err = <-refreshDone; err != nil {
				t.Fatalf("refreshAuthForRequest() error = %v", err)
			}

			current, _ := manager.GetByID(registered.ID)
			if got := current.Metadata["access_token"]; got != "uncommitted-refreshed-token" {
				t.Fatalf("access_token = %#v, want refreshed token", got)
			}
			_, priorityPresent := current.Metadata["priority"]
			if priorityPresent != test.wantPresent || current.Attributes["priority"] != test.wantAttribute {
				t.Fatalf("priority outcome: present=%v attribute=%q", priorityPresent, current.Attributes["priority"])
			}
		})
	}
}

func TestRefreshBeforeConcurrentPriorityUnsetPreservesRefreshedCredentialsAndAllowsExactRetry(t *testing.T) {
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
	unsetDone := make(chan error, 1)
	go func() {
		_, errUnset := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{Operation: PriorityMutationUnset})
		unsetDone <- errUnset
	}()
	select {
	case errUnset := <-unsetDone:
		t.Fatalf("unset completed before refresh commit: %v", errUnset)
	case <-time.After(50 * time.Millisecond):
	}
	close(executor.release)
	if err = <-refreshDone; err != nil {
		t.Fatalf("refreshAuthForRequest() error = %v", err)
	}
	if err = <-unsetDone; !errors.Is(err, ErrAuthRevisionConflict) {
		t.Fatalf("stale unset error = %v, want ErrAuthRevisionConflict", err)
	}

	refreshed, _ := manager.GetByID(registered.ID)
	if _, err = manager.MutatePriority(context.Background(), refreshed.ID, refreshed.Revision(), PriorityMutation{Operation: PriorityMutationUnset}); err != nil {
		t.Fatalf("retry unset error = %v", err)
	}
	current, _ := manager.GetByID(registered.ID)
	if got := current.Metadata["access_token"]; got != "refreshed-token" {
		t.Fatalf("access_token = %#v, want refreshed-token", got)
	}
	if _, present := current.Metadata["priority"]; present {
		t.Fatalf("priority metadata still present: %#v", current.Metadata["priority"])
	}
	if _, present := current.Attributes["priority"]; present {
		t.Fatalf("priority attribute still present: %#v", current.Attributes["priority"])
	}
}

func TestManagerMutatePriorityAllowsUnrelatedProviderWhenCodexWebsocketEnabled(t *testing.T) {
	store := &transactionTestStore{}
	manager := NewManager(store, &FillFirstSelector{}, nil)
	if _, err := manager.Register(context.Background(), &Auth{
		ID:         "codex-websocket.json",
		Provider:   "codex",
		Attributes: map[string]string{"websockets": "true"},
		Metadata:   map[string]any{"type": "codex", "websockets": true},
	}); err != nil {
		t.Fatalf("Register(codex) error = %v", err)
	}
	registered, err := manager.Register(context.Background(), &Auth{
		ID:         "claude-auth.json",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"type": "claude", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register(claude) error = %v", err)
	}

	result, err := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), PriorityMutation{
		Operation: PriorityMutationSet,
		Priority:  101,
	})
	if err != nil {
		t.Fatalf("MutatePriority() error = %v", err)
	}
	if result.Priority.Value != 101 {
		t.Fatalf("priority result = %#v, want 101", result.Priority)
	}
}
