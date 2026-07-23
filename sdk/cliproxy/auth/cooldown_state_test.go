package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type recordingCooldownStateStore struct {
	saveCount atomic.Int32
	mu        sync.Mutex
	records   []CooldownStateRecord
	load      []CooldownStateRecord
}

func (s *recordingCooldownStateStore) Load(context.Context) ([]CooldownStateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCooldownStateRecords(s.load), nil
}

func (s *recordingCooldownStateStore) Save(_ context.Context, records []CooldownStateRecord) error {
	s.saveCount.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = cloneCooldownStateRecords(records)
	return nil
}

func cloneCooldownStateRecords(records []CooldownStateRecord) []CooldownStateRecord {
	if len(records) == 0 {
		return nil
	}
	cloned := make([]CooldownStateRecord, len(records))
	for i := range records {
		cloned[i] = records[i]
		cloned[i].LastError = cloneError(records[i].LastError)
	}
	return cloned
}

func TestFileCooldownStateStore_StateRelativePath(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "auths")
	store := NewFileCooldownStateStoreWithAuthDir(authDir, authDir)

	cases := []struct {
		name   string
		record CooldownStateRecord
		want   string
	}{
		{
			name: "absolute auth file under auth dir",
			record: CooldownStateRecord{
				AuthID:   "auth-1",
				AuthFile: filepath.Join(authDir, "nested", "xai.json"),
			},
			want: filepath.Join("nested", "xai.cds"),
		},
		{
			name: "relative auth file",
			record: CooldownStateRecord{
				AuthID:   "auth-2",
				AuthFile: filepath.Join("team", "xai.json"),
			},
			want: filepath.Join("team", "xai.cds"),
		},
		{
			name: "absolute auth file outside auth dir",
			record: CooldownStateRecord{
				AuthID:   "auth-3",
				AuthFile: filepath.Join(t.TempDir(), "outside.json"),
			},
			want: "outside.cds",
		},
		{
			name: "relative parent escape is rejected",
			record: CooldownStateRecord{
				AuthID:   "auth-4",
				AuthFile: filepath.Join("..", "escape.json"),
			},
			want: "",
		},
		{
			name: "auth id fallback",
			record: CooldownStateRecord{
				AuthID: "auth/id 5",
			},
			want: "auth_id_5.cds",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.stateRelativePath(tc.record); got != tc.want {
				t.Fatalf("stateRelativePath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFileCooldownStateStore_SaveLoadAndCleanStale(t *testing.T) {
	authDir := t.TempDir()
	store := NewFileCooldownStateStoreWithAuthDir(authDir, authDir)
	ctx := context.Background()

	stalePath := filepath.Join(authDir, "stale.cds")
	if errWrite := os.WriteFile(stalePath, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("write stale file: %v", errWrite)
	}

	nextRetry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	updatedAt := time.Now().UTC().Truncate(time.Second)
	record := CooldownStateRecord{
		Provider:       "xai",
		AuthID:         "auth-1",
		AuthFile:       filepath.Join(authDir, "xai.json"),
		Model:          "grok-4",
		Status:         "cooling",
		NextRetryAfter: nextRetry,
		Reason:         "quota",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: nextRetry,
			BackoffLevel:  1,
		},
		LastError: &Error{Message: "rate limited", HTTPStatus: 429},
		UpdatedAt: updatedAt,
	}

	if errSave := store.Save(ctx, []CooldownStateRecord{record}); errSave != nil {
		t.Fatalf("Save() returned error: %v", errSave)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "xai.cds")); errStat != nil {
		t.Fatalf("expected xai.cds to exist: %v", errStat)
	}
	if _, errStat := os.Stat(stalePath); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("expected stale.cds to be removed, stat error = %v", errStat)
	}

	loaded, errLoad := store.Load(ctx)
	if errLoad != nil {
		t.Fatalf("Load() returned error: %v", errLoad)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded records = %d, want 1", len(loaded))
	}
	if loaded[0].AuthID != record.AuthID || loaded[0].Model != record.Model || !loaded[0].NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("loaded record = %+v, want auth/model/retry from %+v", loaded[0], record)
	}
	if loaded[0].LastError == nil || loaded[0].LastError.HTTPStatus != 429 {
		t.Fatalf("loaded last error = %+v, want HTTP 429", loaded[0].LastError)
	}

	if errSave := store.Save(ctx, nil); errSave != nil {
		t.Fatalf("Save(nil) returned error: %v", errSave)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "xai.cds")); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("expected xai.cds to be removed, stat error = %v", errStat)
	}
}

func TestFileCooldownStateStore_ConcurrentSave(t *testing.T) {
	authDir := t.TempDir()
	store := NewFileCooldownStateStoreWithAuthDir(authDir, authDir)
	ctx := context.Background()
	nextRetry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Save(ctx, []CooldownStateRecord{
				{
					Provider:       "xai",
					AuthID:         "auth-1",
					AuthFile:       filepath.Join(authDir, "xai.json"),
					Model:          "grok-4",
					Status:         "cooling",
					NextRetryAfter: nextRetry.Add(time.Duration(i) * time.Second),
					UpdatedAt:      nextRetry,
				},
			})
		}()
	}
	wg.Wait()
	close(errs)
	for errSave := range errs {
		if errSave != nil {
			t.Fatalf("Save() returned error: %v", errSave)
		}
	}

	loaded, errLoad := store.Load(ctx)
	if errLoad != nil {
		t.Fatalf("Load() returned error: %v", errLoad)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded records = %d, want 1", len(loaded))
	}

	tmpMatches, errGlob := filepath.Glob(filepath.Join(authDir, "*.tmp"))
	if errGlob != nil {
		t.Fatalf("glob temp files: %v", errGlob)
	}
	if len(tmpMatches) != 0 {
		t.Fatalf("leftover temp files = %v, want none", tmpMatches)
	}
}

func TestManager_MarkResult_PersistsCooldownOnlyWhenStateChanges(t *testing.T) {
	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	auth := &Auth{ID: "auth-1", Provider: "xai", Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "xai", Model: "grok-4", Success: true})
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("healthy success saved cooldown state %d times, want 0", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "xai",
		Model:    "grok-4",
		Success:  false,
		Error:    &Error{Message: "upstream unavailable", HTTPStatus: 500},
	})
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("cooldown failure saved cooldown state %d times, want 1", got)
	}

	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "xai", Model: "grok-4", Success: true})
	if got := store.saveCount.Load(); got != 2 {
		t.Fatalf("cooldown clear saved cooldown state %d times, want 2", got)
	}

	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "xai", Model: "grok-4", Success: true})
	if got := store.saveCount.Load(); got != 2 {
		t.Fatalf("clean success saved cooldown state %d times, want 2", got)
	}
}

func TestManagerSetConfigSnapshotDefersCooldownPersistence(t *testing.T) {
	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	auth := &Auth{ID: "auth-1", Provider: "xai", Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "grok-4",
		Success:  false,
		Error:    &Error{Message: "rate limited", HTTPStatus: 429},
	})
	store.saveCount.Store(0)

	if changed := manager.SetConfigSnapshot(&internalconfig.Config{DisableCooling: true}); !changed {
		t.Fatal("SetConfigSnapshot() = false, want cleared cooldown state")
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("SetConfigSnapshot() persisted cooldown state %d times, want 0", got)
	}
	manager.PersistCooldownStates(context.Background())
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("PersistCooldownStates() saved cooldown state %d times, want 1", got)
	}
}

type blockingCooldownStateStore struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingCooldownStateStore) Load(context.Context) ([]CooldownStateRecord, error) {
	return nil, nil
}

func (s *blockingCooldownStateStore) Save(ctx context.Context, _ []CooldownStateRecord) error {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestManagerSwapCooldownStateStorePersistsOldStoreBeforeSwap(t *testing.T) {
	oldStore := &recordingCooldownStateStore{}
	newStore := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(oldStore)
	auth := &Auth{ID: "auth-1", Provider: "xai", Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "grok-4", Success: false,
		Error: &Error{Message: "rate limited", HTTPStatus: 429},
	})
	oldStore.saveCount.Store(0)
	if changed := manager.SetConfigSnapshot(&internalconfig.Config{DisableCooling: true}); !changed {
		t.Fatal("SetConfigSnapshot() = false, want cleared cooldown state")
	}

	if swapped := manager.SwapCooldownStateStore(context.Background(), newStore, true); !swapped {
		t.Fatal("SwapCooldownStateStore() = false, want true")
	}
	if got := oldStore.saveCount.Load(); got != 1 {
		t.Fatalf("old store save count = %d, want 1", got)
	}
	if len(oldStore.records) != 0 {
		t.Fatalf("old store records = %+v, want cleared cooldown state", oldStore.records)
	}
	manager.mu.RLock()
	currentStore := manager.cooldownStore
	manager.mu.RUnlock()
	if currentStore != newStore {
		t.Fatal("cooldown store swapped before the old store was persisted")
	}
}

func TestManagerApplyConfigWithCooldownStoreSerializesTransitions(t *testing.T) {
	oldStore := &blockingCooldownStateStore{started: make(chan struct{}), release: make(chan struct{})}
	firstStore := &recordingCooldownStateStore{}
	secondStore := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-1", Provider: "xai", Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "grok-4", Success: false,
		Error: &Error{Message: "rate limited", HTTPStatus: 429},
	})
	manager.SetCooldownStateStore(oldStore)

	firstDone := make(chan bool, 1)
	go func() {
		firstDone <- manager.ApplyConfigWithCooldownStateStore(context.Background(), &internalconfig.Config{DisableCooling: true}, firstStore)
	}()
	select {
	case <-oldStore.started:
	case <-time.After(time.Second):
		t.Fatal("first old-store persistence did not start")
	}

	secondDone := make(chan bool, 1)
	go func() {
		secondDone <- manager.ApplyConfigWithCooldownStateStore(context.Background(), &internalconfig.Config{}, secondStore)
	}()
	select {
	case <-secondDone:
		t.Fatal("concurrent config transition completed while old-store persistence was blocked")
	case <-time.After(100 * time.Millisecond):
	}

	close(oldStore.release)
	if applied := waitForCooldownTransition(t, firstDone, "first config transition"); !applied {
		t.Fatal("first config transition returned false")
	}
	if applied := waitForCooldownTransition(t, secondDone, "second config transition"); !applied {
		t.Fatal("second config transition returned false")
	}
	manager.mu.RLock()
	currentStore := manager.cooldownStore
	manager.mu.RUnlock()
	if currentStore != secondStore {
		t.Fatal("concurrent config transitions did not leave the final resolved store installed")
	}
}

func waitForCooldownTransition(t *testing.T, done <-chan bool, name string) bool {
	t.Helper()
	select {
	case applied := <-done:
		return applied
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return false
	}
}

func TestManagerSwapCooldownStateStoreKeepsOldStoreWhenCanceled(t *testing.T) {
	oldStore := &blockingCooldownStateStore{started: make(chan struct{}), release: make(chan struct{})}
	newStore := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(oldStore)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- manager.SwapCooldownStateStore(ctx, newStore, true) }()
	select {
	case <-oldStore.started:
	case <-time.After(time.Second):
		t.Fatal("old cooldown store persistence did not start")
	}
	manager.mu.RLock()
	currentStore := manager.cooldownStore
	manager.mu.RUnlock()
	if currentStore != oldStore {
		t.Fatal("cooldown store swapped while old store persistence was blocked")
	}
	cancel()
	select {
	case swapped := <-done:
		if swapped {
			t.Fatal("SwapCooldownStateStore() = true after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("SwapCooldownStateStore() did not honor cancellation")
	}

	close(oldStore.release)
	if swapped := manager.SwapCooldownStateStore(context.Background(), newStore, false); !swapped {
		t.Fatal("SwapCooldownStateStore() = false, want retry to persist the old store before swapping")
	}
	manager.mu.RLock()
	currentStore = manager.cooldownStore
	manager.mu.RUnlock()
	if currentStore != newStore {
		t.Fatal("cooldown store was not swapped after pending persistence completed")
	}
}

func TestManager_RestoreCooldownStates(t *testing.T) {
	nextRetry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	store := &recordingCooldownStateStore{
		load: []CooldownStateRecord{
			{
				Provider:       "xai",
				AuthID:         "auth-1",
				Model:          "grok-4",
				Status:         "cooling",
				NextRetryAfter: nextRetry,
				Reason:         "quota",
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
				},
				LastError: &Error{Message: "rate limited", HTTPStatus: 429},
				UpdatedAt: nextRetry.Add(-time.Minute),
			},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-1", Provider: "xai"}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() returned error: %v", errRestore)
	}

	auth, ok := manager.GetByID("auth-1")
	if !ok {
		t.Fatal("restored auth was not found")
	}
	state := auth.ModelStates["grok-4"]
	if state == nil {
		t.Fatal("model state was not restored")
	}
	if !state.Unavailable || state.Status != StatusError || !state.NextRetryAfter.Equal(nextRetry) {
		t.Fatalf("restored state = %+v, want unavailable status error until %v", state, nextRetry)
	}
	if state.LastError == nil || state.LastError.HTTPStatus != 429 {
		t.Fatalf("restored last error = %+v, want HTTP 429", state.LastError)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("restore cleanup saved cooldown state %d times, want 1", got)
	}
}

func TestManagerResultSaveWaitsForCooldownStoreTransition(t *testing.T) {
	oldStore := &blockingCooldownStateStore{started: make(chan struct{}), release: make(chan struct{})}
	newStore := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-1", Provider: "xai", Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.SetCooldownStateStore(oldStore)

	transitionDone := make(chan bool, 1)
	go func() {
		transitionDone <- manager.SwapCooldownStateStore(context.Background(), newStore, true)
	}()
	select {
	case <-oldStore.started:
	case <-time.After(time.Second):
		t.Fatal("old-store transition save did not start")
	}

	resultDone := make(chan struct{})
	go func() {
		manager.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: auth.Provider, Model: "grok-4", Success: false,
			Error: &Error{Message: "rate limited", HTTPStatus: 429},
		})
		close(resultDone)
	}()
	select {
	case <-resultDone:
		t.Fatal("result save completed while the store transition was blocked")
	case <-time.After(100 * time.Millisecond):
	}

	close(oldStore.release)
	if swapped := waitForCooldownTransition(t, transitionDone, "cooldown store transition"); !swapped {
		t.Fatal("SwapCooldownStateStore() = false")
	}
	select {
	case <-resultDone:
	case <-time.After(time.Second):
		t.Fatal("result save did not complete after store transition")
	}
	if got := newStore.saveCount.Load(); got != 1 {
		t.Fatalf("new store save count = %d, want 1", got)
	}
}
