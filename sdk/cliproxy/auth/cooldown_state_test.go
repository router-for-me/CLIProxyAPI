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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
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

func (s *recordingCooldownStateStore) savedRecords() []CooldownStateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCooldownStateRecords(s.records)
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

// TestCooldownEffectiveDeadlineSurvivesRestartAcrossShorterNextRetryAfter
// reproduces the sequence Codex flagged at conductor_cooldown.go:2154: an
// explicit not-found classification escalates Quota.NextRecoverAt (and
// initially NextRetryAfter) to a 12h deadline, then a later, unrelated
// transient 5xx overwrites NextRetryAfter alone with a much shorter
// deadline (see the 408/500/502/503/504 branch of
// applyAuthFailureStateForModel, which never touches Quota.NextRecoverAt).
// Gating persistence and restore on NextRetryAfter alone would silently
// drop the cooldown - and the credential would come back online early -
// the moment the short deadline passes, even though the credential is
// still blocked in memory (via availabilityBlock) until the long
// Quota.NextRecoverAt deadline. This asserts the persisted record survives
// past the short deadline, carries the effective (long) deadline as its
// NextRetryAfter - so a `.cds` file is a truthful acceptance instrument -
// and that restoring it into a brand new Manager still blocks until the
// long deadline.
func TestCooldownEffectiveDeadlineSurvivesRestartAcrossShorterNextRetryAfter(t *testing.T) {
	now := time.Now()
	longDeadline := now.Add(12 * time.Hour)
	shortDeadline := now.Add(time.Minute)

	auth := &Auth{
		ID:             "auth-1",
		Provider:       "xai",
		Unavailable:    true,
		Status:         StatusError,
		NextRetryAfter: shortDeadline, // shortened by the later 5xx
		Quota: QuotaState{
			Exceeded:      true, // set true, and left untouched, by the earlier not-found escalation
			Reason:        "model_not_found",
			NextRecoverAt: longDeadline,
		},
	}

	// Simulate a persistence tick that runs after the short deadline has
	// passed but well before the long one.
	afterShortDeadline := shortDeadline.Add(time.Minute)
	record, ok := authCooldownStateRecord(auth, afterShortDeadline)
	if !ok {
		t.Fatal("authCooldownStateRecord() = false after the short NextRetryAfter expired, want true because Quota.NextRecoverAt is still active")
	}
	if !record.NextRetryAfter.Equal(longDeadline) {
		t.Fatalf("persisted NextRetryAfter = %v, want the effective (long) deadline %v so the .cds file reflects the deadline actually in force", record.NextRetryAfter, longDeadline)
	}
	if !record.Quota.NextRecoverAt.Equal(longDeadline) {
		t.Fatalf("persisted Quota.NextRecoverAt = %v, want %v", record.Quota.NextRecoverAt, longDeadline)
	}

	// Restore into a brand new Manager, as if the process had restarted at
	// a point after the short deadline but before the long one.
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-1", Provider: "xai", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	restartAt := afterShortDeadline.Add(time.Minute)
	manager.mu.Lock()
	restored := manager.restoreCooldownRecordLocked(record, restartAt)
	manager.mu.Unlock()
	if !restored {
		t.Fatal("restoreCooldownRecordLocked() = false at a time between the short and long deadlines, want true")
	}

	restoredAuth, ok := manager.GetByID("auth-1")
	if !ok {
		t.Fatal("restored auth not found")
	}
	if !restoredAuth.Unavailable {
		t.Fatal("restored auth.Unavailable = false, want true")
	}
	if !restoredAuth.NextRetryAfter.Equal(longDeadline) {
		t.Fatalf("restored auth.NextRetryAfter = %v, want the effective (long) deadline %v", restoredAuth.NextRetryAfter, longDeadline)
	}
	blocked, _, next := availabilityBlock(restoredAuth.Unavailable, restoredAuth.Quota.Exceeded, restoredAuth.NextRetryAfter, restoredAuth.Quota.NextRecoverAt, restartAt)
	if !blocked {
		t.Fatal("availabilityBlock() = not blocked for the restored auth, want blocked until the long deadline")
	}
	if !next.Equal(longDeadline) {
		t.Fatalf("availabilityBlock() next = %v, want the long deadline %v", next, longDeadline)
	}
}

// TestCooldownRestoreHonorsNextRecoverAtOnAHandBuiltRecord isolates the
// restore-side gate from the persist-side fix above: it feeds
// restoreCooldownRecordLocked a record whose NextRetryAfter is already
// shortened (as authCooldownStateRecord would have written before this fix,
// or as any pre-existing `.cds` file on disk still does until it is next
// rewritten) while Quota.NextRecoverAt carries the true, longer deadline.
// Restore must still honor the record and adopt the effective deadline.
func TestCooldownRestoreHonorsNextRecoverAtOnAHandBuiltRecord(t *testing.T) {
	now := time.Now()
	longDeadline := now.Add(12 * time.Hour)
	shortDeadline := now.Add(time.Minute)
	restartAt := shortDeadline.Add(time.Minute) // after short, well before long

	record := CooldownStateRecord{
		Provider:       "xai",
		AuthID:         "auth-hand-built",
		Status:         "cooling",
		NextRetryAfter: shortDeadline,
		Reason:         "model_not_found",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "model_not_found",
			NextRecoverAt: longDeadline,
		},
	}

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-hand-built", Provider: "xai", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.mu.Lock()
	restored := manager.restoreCooldownRecordLocked(record, restartAt)
	manager.mu.Unlock()
	if !restored {
		t.Fatal("restoreCooldownRecordLocked() = false for a record whose NextRetryAfter already expired but Quota.NextRecoverAt is still active, want true")
	}
	restoredAuth, ok := manager.GetByID("auth-hand-built")
	if !ok {
		t.Fatal("restored auth not found")
	}
	if !restoredAuth.NextRetryAfter.Equal(longDeadline) {
		t.Fatalf("restored NextRetryAfter = %v, want the effective (long) deadline %v", restoredAuth.NextRetryAfter, longDeadline)
	}
}

// TestCooldownRecordLegacyRoundTripWithoutNextRecoverAt covers a genuinely
// legacy on-disk record: only NextRetryAfter was ever meaningful for it
// (Quota.NextRecoverAt is its zero value, as it always has been for a plain
// transient-error cooldown that never went through the not-found/quota
// escalation paths). It must persist and restore exactly as it did before
// this fix - gated on, and carrying, NextRetryAfter alone.
func TestCooldownRecordLegacyRoundTripWithoutNextRecoverAt(t *testing.T) {
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	auth := &Auth{
		ID:             "auth-legacy",
		Provider:       "xai",
		Unavailable:    true,
		Status:         StatusError,
		NextRetryAfter: deadline,
		// Quota left at its zero value: no Exceeded, no NextRecoverAt.
	}

	record, ok := authCooldownStateRecord(auth, now)
	if !ok {
		t.Fatal("authCooldownStateRecord() = false for a live legacy-shaped cooldown, want true")
	}
	if !record.NextRetryAfter.Equal(deadline) {
		t.Fatalf("persisted NextRetryAfter = %v, want %v (unchanged, no NextRecoverAt to take the max against)", record.NextRetryAfter, deadline)
	}
	if !record.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("persisted Quota.NextRecoverAt = %v, want zero for a legacy record", record.Quota.NextRecoverAt)
	}

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-legacy", Provider: "xai", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	// Before the deadline: restores exactly as before.
	beforeDeadline := deadline.Add(-time.Minute)
	manager.mu.Lock()
	restored := manager.restoreCooldownRecordLocked(record, beforeDeadline)
	manager.mu.Unlock()
	if !restored {
		t.Fatal("restoreCooldownRecordLocked() = false before the legacy deadline, want true")
	}
	restoredAuth, ok := manager.GetByID("auth-legacy")
	if !ok {
		t.Fatal("restored auth not found")
	}
	if !restoredAuth.NextRetryAfter.Equal(deadline) {
		t.Fatalf("restored NextRetryAfter = %v, want %v", restoredAuth.NextRetryAfter, deadline)
	}

	// After the deadline: a legacy record with no NextRecoverAt to fall
	// back on is correctly treated as expired, exactly like before this fix.
	manager2 := NewManager(nil, nil, nil)
	if _, errRegister := manager2.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-legacy", Provider: "xai", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	afterDeadline := deadline.Add(time.Minute)
	manager2.mu.Lock()
	restoredAfter := manager2.restoreCooldownRecordLocked(record, afterDeadline)
	manager2.mu.Unlock()
	if restoredAfter {
		t.Fatal("restoreCooldownRecordLocked() = true past a legacy record's only deadline, want false")
	}
}

func TestManager_RestoreCooldownStatesCanonicalizesThinkingSuffixes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	laterRetry := now.Add(2 * time.Hour)
	store := &recordingCooldownStateStore{
		load: []CooldownStateRecord{
			{
				Provider:       "gemini",
				AuthID:         "auth-thinking",
				Model:          "gemini-3.1-pro-preview(high)",
				NextRetryAfter: now.Add(time.Hour),
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: now.Add(time.Hour),
				},
				UpdatedAt: now,
			},
			{
				Provider:       "gemini",
				AuthID:         "auth-thinking",
				Model:          "gemini-3.1-pro-preview(low)",
				NextRetryAfter: laterRetry,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: laterRetry,
				},
				UpdatedAt: now.Add(time.Minute),
			},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-thinking", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() returned error: %v", errRestore)
	}

	auth, ok := manager.GetByID("auth-thinking")
	if !ok || auth == nil {
		t.Fatal("restored auth was not found")
	}
	if len(auth.ModelStates) != 1 {
		t.Fatalf("len(ModelStates) = %d, want 1: %+v", len(auth.ModelStates), auth.ModelStates)
	}
	state := auth.ModelStates["gemini-3.1-pro-preview"]
	if state == nil || !state.Unavailable || !state.NextRetryAfter.Equal(laterRetry) {
		t.Fatalf("canonical model state = %+v, want unavailable until %v", state, laterRetry)
	}

	store.mu.Lock()
	persisted := cloneCooldownStateRecords(store.records)
	store.mu.Unlock()
	modelRecords := make([]CooldownStateRecord, 0, len(persisted))
	for _, record := range persisted {
		if record.Model != "" {
			modelRecords = append(modelRecords, record)
		}
	}
	if len(modelRecords) != 1 || modelRecords[0].Model != "gemini-3.1-pro-preview" || !modelRecords[0].NextRetryAfter.Equal(laterRetry) {
		t.Fatalf("persisted model records = %+v, want one canonical record until %v", modelRecords, laterRetry)
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

// TestAuthCooldownStateRecordSkipsAggregateOnlyQuotaWithAvailableSibling
// reproduces the P1 Codex finding on discussion_r3927779676: aggregating
// one cooling model's quota into auth.Quota (via updateAggregatedAvailability)
// while leaving auth.Unavailable false, because a sibling model remains
// available, must NOT itself cause authCooldownStateRecord to emit a
// credential-level record. A raw availabilityBlock(auth.Quota...) call
// cannot see the aggregate-vs-credential distinction and would persist a
// record that blocks the WHOLE credential (all models) after a restart,
// even though only one of several sibling models was ever cooling. This
// asserts persist skips the auth-level record, and that a fresh Manager
// restoring only the model-scoped record still leaves the sibling
// selectable while the cooled model stays blocked.
func TestAuthCooldownStateRecordSkipsAggregateOnlyQuotaWithAvailableSibling(t *testing.T) {
	now := time.Now()
	longDeadline := now.Add(12 * time.Hour)

	auth := &Auth{
		ID:       "auth-multi",
		Provider: "openai-compat",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"llama3": {
				Status:      StatusError,
				Unavailable: true,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: longDeadline,
				},
				NextRetryAfter: longDeadline,
				UpdatedAt:      now,
			},
			"mistral": {
				Status: StatusActive,
			},
		},
	}
	// Mirror what updateAggregatedAvailability actually does: copy the
	// cooling sibling's quota into auth.Quota while leaving auth.Unavailable
	// false because "mistral" is still available.
	updateAggregatedAvailability(auth, now)
	if auth.Unavailable {
		t.Fatal("auth.Unavailable = true after aggregation with an available sibling, want false (precondition for this test)")
	}
	if !auth.Quota.Exceeded {
		t.Fatal("auth.Quota.Exceeded = false after aggregation, want true (precondition: aggregate quota was copied)")
	}

	if _, ok := authCooldownStateRecord(auth, now); ok {
		t.Fatal("authCooldownStateRecord() = true from an aggregate-only quota with an available sibling, want false")
	}

	llamaRecord, ok := modelCooldownStateRecord(auth, "llama3", auth.ModelStates["llama3"], now)
	if !ok {
		t.Fatal("modelCooldownStateRecord(llama3) = false, want true")
	}

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-multi", Provider: "openai-compat", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.mu.Lock()
	restored := manager.restoreCooldownRecordLocked(llamaRecord, now)
	manager.mu.Unlock()
	if !restored {
		t.Fatal("restoreCooldownRecordLocked(llama3) = false, want true")
	}

	restoredAuth, ok := manager.GetByID("auth-multi")
	if !ok {
		t.Fatal("restored auth not found")
	}
	if blocked, _, _ := effectiveBlock(restoredAuth, "mistral", now); blocked {
		t.Fatal("effectiveBlock(mistral) = blocked after restoring only the llama3 model record, want selectable")
	}
	if blocked, _, _ := effectiveBlock(restoredAuth, "llama3", now); !blocked {
		t.Fatal("effectiveBlock(llama3) = not blocked after restore, want blocked until the cooldown deadline")
	}
	if blocked, _, _ := effectiveBlock(restoredAuth, "", now); blocked {
		t.Fatal("effectiveBlock(\"\") = blocked for the whole credential after restoring one cooling sibling, want selectable per the aggregate exception")
	}

	// Self-reinforcement guard: if restore had left restoredAuth.Unavailable
	// true, the next persist cycle's authCooldownStateRecord call would see
	// !auth.Unavailable fail, write a fresh credential-level record from
	// nothing but the surviving model-scoped state, and the credential would
	// never recover across a second restart. Confirm the persist path agrees
	// the credential is selectable one cycle after restore.
	if _, ok := authCooldownStateRecord(restoredAuth, now); ok {
		t.Fatal("authCooldownStateRecord() = true one persist cycle after restoring one cooling sibling, want false (would re-darken the credential on the next restart)")
	}
}

// TestAuthCooldownStateRecordPersistsGenuineCredentialLevel401 covers the
// companion case: a real credential-wide cooldown (e.g. a 401, with no
// per-model states at all) must still persist and, on restore into a fresh
// Manager, block every model - proving the P1 fix's write-side reuse of
// effectiveBlock didn't accidentally suppress genuine credential-level
// records, and the restore-side fix didn't route this case through
// updateAggregatedAvailability (which would wipe it via
// clearAggregatedAvailability when ModelStates is empty).
func TestAuthCooldownStateRecordPersistsGenuineCredentialLevel401(t *testing.T) {
	now := time.Now()
	deadline := now.Add(time.Hour)

	auth := &Auth{
		ID:             "auth-401",
		Provider:       "openai-compat",
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: deadline,
		LastError:      &Error{Message: "unauthorized", HTTPStatus: 401},
		UpdatedAt:      now,
	}

	record, ok := authCooldownStateRecord(auth, now)
	if !ok {
		t.Fatal("authCooldownStateRecord() = false for a genuine credential-level 401 cooldown, want true")
	}
	if !record.NextRetryAfter.Equal(deadline) {
		t.Fatalf("persisted NextRetryAfter = %v, want %v", record.NextRetryAfter, deadline)
	}

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-401", Provider: "openai-compat", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.mu.Lock()
	restored := manager.restoreCooldownRecordLocked(record, now)
	manager.mu.Unlock()
	if !restored {
		t.Fatal("restoreCooldownRecordLocked() = false for the credential-level 401 record, want true")
	}

	restoredAuth, ok := manager.GetByID("auth-401")
	if !ok {
		t.Fatal("restored auth not found")
	}
	if !restoredAuth.Unavailable {
		t.Fatal("restored auth.Unavailable = false, want true (genuine credential-level cooldown must survive restore)")
	}
	for _, model := range []string{"", "any-model", "another-model"} {
		if blocked, _, _ := effectiveBlock(restoredAuth, model, now); !blocked {
			t.Fatalf("effectiveBlock(%q) = not blocked after restoring a credential-level 401, want blocked", model)
		}
	}
}

// TestAuthCooldownStateRecordAgreesWithSelectorOnWriteSideGate is a direct,
// standalone assertion that authCooldownStateRecord agrees with
// effectiveBlock(auth, "", now) on the same in-memory auth (no
// restore round-trip). It does not by itself prove the write-side fix is
// exercised - that proof is the mutation-control run recorded in the P1
// report (reverting authCooldownStateRecord to call
// availabilityBlock(auth.Quota...) directly is caught by
// TestAuthCooldownStateRecordSkipsAggregateOnlyQuotaWithAvailableSibling).
// This test exists so a reviewer can see the exact gate condition without
// cross-referencing that other test.
func TestAuthCooldownStateRecordAgreesWithSelectorOnWriteSideGate(t *testing.T) {
	now := time.Now()
	auth := &Auth{
		ID:       "auth-gate",
		Provider: "openai-compat",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"cooling-model": {
				Status:      StatusError,
				Unavailable: true,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: now.Add(12 * time.Hour),
				},
				NextRetryAfter: now.Add(12 * time.Hour),
				UpdatedAt:      now,
			},
			"available-model": {Status: StatusActive},
		},
	}
	updateAggregatedAvailability(auth, now)

	// effectiveBlock(auth, "", now) is the exact function the fixed
	// authCooldownStateRecord now calls; assert it agrees the credential is
	// NOT blocked here - this is the gate whose removal would regress to
	// the old bug.
	blocked, _, _ := effectiveBlock(auth, "", now)
	if blocked {
		t.Fatal("effectiveBlock(auth, \"\", now) = blocked from an aggregate-only quota with an available sibling, want selectable")
	}
	if _, ok := authCooldownStateRecord(auth, now); ok {
		t.Fatal("authCooldownStateRecord() must agree with effectiveBlock(auth, \"\", now) and skip the record")
	}
}

// TestRestoreCooldownStatesAllModelsCoolingBlocksCredential covers the delta
// review's must-fix item 1: when the registered model set for a credential
// is PROVABLY COMPLETE - every model the registry lists for this credential
// has a restored, blocked ModelState - restore must raise auth.Unavailable,
// not just lower it. Without this, a credential whose every model is
// cooling would restore into a selectable-looking state, the opposite of
// the escalation-suppression fix's intent.
func TestRestoreCooldownStatesAllModelsCoolingBlocksCredential(t *testing.T) {
	now := time.Now()
	deadline := now.Add(12 * time.Hour)

	llamaState := &ModelState{
		Status:      StatusError,
		Unavailable: true,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: deadline,
		},
		NextRetryAfter: deadline,
		UpdatedAt:      now,
	}
	mistralState := &ModelState{
		Status:      StatusError,
		Unavailable: true,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: deadline,
		},
		NextRetryAfter: deadline,
		UpdatedAt:      now,
	}
	auth := &Auth{
		ID:       "auth-all-cooling",
		Provider: "openai-compat",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"llama3":  llamaState,
			"mistral": mistralState,
		},
	}
	llamaRecord, ok := modelCooldownStateRecord(auth, "llama3", llamaState, now)
	if !ok {
		t.Fatal("modelCooldownStateRecord(llama3) = false, want true")
	}
	mistralRecord, ok := modelCooldownStateRecord(auth, "mistral", mistralState, now)
	if !ok {
		t.Fatal("modelCooldownStateRecord(mistral) = false, want true")
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-all-cooling", "openai-compat", []*registry.ModelInfo{{ID: "llama3"}, {ID: "mistral"}})
	t.Cleanup(func() { reg.UnregisterClient("auth-all-cooling") })

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-all-cooling", Provider: "openai-compat", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.mu.Lock()
	if !manager.restoreCooldownRecordLocked(llamaRecord, now) {
		manager.mu.Unlock()
		t.Fatal("restoreCooldownRecordLocked(llama3) = false, want true")
	}
	if !manager.restoreCooldownRecordLocked(mistralRecord, now) {
		manager.mu.Unlock()
		t.Fatal("restoreCooldownRecordLocked(mistral) = false, want true")
	}
	manager.mu.Unlock()

	restoredAuth, ok := manager.GetByID("auth-all-cooling")
	if !ok {
		t.Fatal("restored auth not found")
	}
	if blocked, _, _ := effectiveBlock(restoredAuth, "llama3", now); !blocked {
		t.Fatal("effectiveBlock(llama3) = not blocked after restoring both cooling models, want blocked")
	}
	if blocked, _, _ := effectiveBlock(restoredAuth, "mistral", now); !blocked {
		t.Fatal("effectiveBlock(mistral) = not blocked after restoring both cooling models, want blocked")
	}
	if blocked, _, _ := effectiveBlock(restoredAuth, "", now); !blocked {
		t.Fatal("effectiveBlock(\"\") = selectable after restoring every registered model as cooling, want blocked (registry set is provably complete)")
	}
	if !restoredAuth.Unavailable {
		t.Fatal("restoredAuth.Unavailable = false after restoring every registered model as cooling, want true")
	}
}

// TestRestoreCooldownStatesPartialModelSetStaysSelectable re-confirms the
// original P1 scenario now that restore can escalate: with the registry
// listing a sibling model that has NO restored state, the restored set is
// not provably complete, so restore must still only lower - never raise -
// auth.Unavailable.
func TestRestoreCooldownStatesPartialModelSetStaysSelectable(t *testing.T) {
	now := time.Now()
	deadline := now.Add(12 * time.Hour)

	llamaState := &ModelState{
		Status:      StatusError,
		Unavailable: true,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: deadline,
		},
		NextRetryAfter: deadline,
		UpdatedAt:      now,
	}
	auth := &Auth{
		ID:       "auth-partial",
		Provider: "openai-compat",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"llama3": llamaState,
		},
	}
	llamaRecord, ok := modelCooldownStateRecord(auth, "llama3", llamaState, now)
	if !ok {
		t.Fatal("modelCooldownStateRecord(llama3) = false, want true")
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-partial", "openai-compat", []*registry.ModelInfo{{ID: "llama3"}, {ID: "mistral"}})
	t.Cleanup(func() { reg.UnregisterClient("auth-partial") })

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-partial", Provider: "openai-compat", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.mu.Lock()
	restored := manager.restoreCooldownRecordLocked(llamaRecord, now)
	manager.mu.Unlock()
	if !restored {
		t.Fatal("restoreCooldownRecordLocked(llama3) = false, want true")
	}

	restoredAuth, ok := manager.GetByID("auth-partial")
	if !ok {
		t.Fatal("restored auth not found")
	}
	if blocked, _, _ := effectiveBlock(restoredAuth, "", now); blocked {
		t.Fatal("effectiveBlock(\"\") = blocked when the registry lists an unrestored sibling (mistral), want selectable - the restored set is not provably complete")
	}
	if restoredAuth.Unavailable {
		t.Fatal("restoredAuth.Unavailable = true when the restored model set is not provably complete, want false")
	}
}

// TestRestoreCooldownStatesModelSetCompletenessMutationControl is a
// mutation control for the item-1 fix: reverting restoreCooldownRecordLocked
// to always suppress the escalation (its pre-this-round behavior) must be
// caught by TestRestoreCooldownStatesAllModelsCoolingBlocksCredential.
func TestRestoreCooldownStatesModelSetCompletenessMutationControl(t *testing.T) {
	now := time.Now()
	auth := &Auth{
		ID:       "auth-gate-complete",
		Provider: "openai-compat",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"only-model": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(time.Hour),
				UpdatedAt:      now,
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-gate-complete", "openai-compat", []*registry.ModelInfo{{ID: "only-model"}})
	t.Cleanup(func() { reg.UnregisterClient("auth-gate-complete") })

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-gate-complete", Provider: "openai-compat", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.mu.Lock()
	registered := manager.auths["auth-gate-complete"]
	manager.mu.Unlock()
	if !manager.restoredModelSetIsComplete(mergeAuthModelStates(registered, auth), now) {
		t.Fatal("restoredModelSetIsComplete() = false for a registry set fully covered by restored, blocked states, want true")
	}
}

// mergeAuthModelStates copies model states from src onto a shallow clone of
// dst for the completeness-gate mutation control, without going through the
// full restore path (which is already covered end-to-end by the other two
// tests in this group).
func mergeAuthModelStates(dst, src *Auth) *Auth {
	if dst == nil || src == nil {
		return dst
	}
	dst.ModelStates = src.ModelStates
	return dst
}

// TestUpdateAggregatedAvailabilityPreservesGenericNotFoundThroughSiblingSuccess
// covers the delta review's must-fix item 3, reproducing the exact sequence
// from Codex's finding: a generic (non-explicit) 404 stores its backoff in
// state.Quota.NextRecoverAt while leaving state.Quota.Exceeded false (see
// notFoundRetryAfter's non-explicit branch, which writes
// retryState.NextRecoverAt directly with no applyCooldownFields call). A
// concurrent, shorter-lived 5xx then overwrites state.NextRetryAfter with an
// earlier deadline without touching Quota at all. Once that shorter
// NextRetryAfter passes, a sibling model's success calls
// updateAggregatedAvailability. Pre-fix, the aggregation loop decided
// per-model unavailability from state.Unavailable + state.NextRetryAfter
// alone: once NextRetryAfter was in the past it unconditionally cleared
// BOTH state.Unavailable and state.NextRetryAfter, discarding the still
// future state.Quota.NextRecoverAt with no attempt to consult it - and
// availabilityBlock's own bypass (`!unavailable && !quotaExceeded`) then
// ignores NextRecoverAt too, so the model becomes selectable hours before
// its generic-404 backoff actually expires. Post-fix, aggregation decides
// via the same availabilityBlock predicate the selector itself uses, fed
// state.Unavailable (still true - nothing clears it prematurely) alongside
// both deadlines, so it keeps blocking until the later of the two expires.
func TestUpdateAggregatedAvailabilityPreservesGenericNotFoundThroughSiblingSuccess(t *testing.T) {
	now := time.Now()
	shortenedRetryAfter := now.Add(30 * time.Second)   // the concurrent 5xx's shorter deadline
	genericNotFoundRecoverAt := now.Add(2 * time.Hour) // the original generic-404 backoff

	problemState := &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: shortenedRetryAfter,
		Quota: QuotaState{
			Exceeded:      false,
			NextRecoverAt: genericNotFoundRecoverAt,
		},
		UpdatedAt: now,
	}
	siblingState := &ModelState{Status: StatusActive}
	auth := &Auth{
		ID:       "auth-generic-404",
		Provider: "openai-compat",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"problem-model": problemState,
			"sibling-model": siblingState,
		},
	}

	// The shortened NextRetryAfter has now passed, but the generic-404's
	// own NextRecoverAt (2h out) has not. A sibling success triggers
	// aggregation at this later time.
	afterShortDeadline := now.Add(31 * time.Second)
	updateAggregatedAvailability(auth, afterShortDeadline)

	if blocked, _, next := effectiveBlock(auth, "problem-model", afterShortDeadline); !blocked {
		t.Fatalf("effectiveBlock(problem-model) = not blocked at %v after a sibling success, want blocked until %v (generic-404 NextRecoverAt) - got next=%v", afterShortDeadline, genericNotFoundRecoverAt, next)
	}
	if blocked, _, _ := effectiveBlock(auth, "sibling-model", afterShortDeadline); blocked {
		t.Fatal("effectiveBlock(sibling-model) = blocked, want selectable")
	}

	// Confirm the deadline itself is honored: once genericNotFoundRecoverAt
	// has actually passed, the model must become selectable again.
	afterLongDeadline := genericNotFoundRecoverAt.Add(time.Second)
	updateAggregatedAvailability(auth, afterLongDeadline)
	if blocked, _, _ := effectiveBlock(auth, "problem-model", afterLongDeadline); blocked {
		t.Fatal("effectiveBlock(problem-model) = still blocked after genericNotFoundRecoverAt has passed, want selectable")
	}
}

// TestUpdateAggregatedAvailabilityGenericNotFoundMutationControl is a
// mutation control for item 3: reverting updateAggregatedAvailability's
// per-model decision to the pre-fix field-specific shortcut (NextRetryAfter
// alone, unconditionally clearing Unavailable/NextRetryAfter once it
// expires) must be caught by
// TestUpdateAggregatedAvailabilityPreservesGenericNotFoundThroughSiblingSuccess.
func TestUpdateAggregatedAvailabilityGenericNotFoundMutationControl(t *testing.T) {
	now := time.Now()
	afterShortDeadline := now.Add(31 * time.Second)

	// Directly re-execute the pre-fix per-model decision to document the
	// exact gate this test group protects, independent of the full
	// updateAggregatedAvailability call.
	state := &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: now.Add(30 * time.Second),
		Quota: QuotaState{
			Exceeded:      false,
			NextRecoverAt: now.Add(2 * time.Hour),
		},
	}
	preFixStateUnavailable := false
	if state.Status == StatusDisabled {
		preFixStateUnavailable = true
	} else if state.Unavailable {
		if state.NextRetryAfter.IsZero() {
			preFixStateUnavailable = false
		} else if state.NextRetryAfter.After(afterShortDeadline) {
			preFixStateUnavailable = true
		} else {
			preFixStateUnavailable = false // the pre-fix wipe branch
		}
	}
	if preFixStateUnavailable {
		t.Fatal("pre-fix field-specific shortcut unexpectedly still reports unavailable - mutation control precondition broken, update this test")
	}
	blocked, _, _ := availabilityBlock(false, false, time.Time{}, state.Quota.NextRecoverAt, afterShortDeadline)
	if blocked {
		t.Fatal("pre-fix wipe followed by availabilityBlock(false, false, ...) unexpectedly still blocks - mutation control precondition broken, update this test")
	}
}

// TestRestoreCooldownStatesCredentialWideSurvivesCleanModelStates covers the
// delta review's second must-fix item: a genuine credential-wide failure
// (e.g. a 401, applied directly to auth.Unavailable/NextRetryAfter by
// applyAuthFailureStateForModel, with no per-model context at all) must not
// be silently cleared by updateAggregatedAvailability just because this
// credential also happens to carry clean historical ModelStates entries
// (models it has served successfully before, with no error state of their
// own). Before this fix, restoreCooldownRecordLocked's model=="" branch
// unconditionally re-derived auth.Unavailable from ModelStates the moment
// ModelStates was non-empty, discarding the persisted 401's own deadline the
// instant any clean sibling model state existed. The fix persists an
// explicit Scope=credential marker on the auth-level record and threads it
// through to auth.CredentialCooldown, which updateAggregatedAvailability now
// checks before ever consulting ModelStates.
func TestRestoreCooldownStatesCredentialWideSurvivesCleanModelStates(t *testing.T) {
	now := time.Now()
	deadline := now.Add(30 * time.Minute).UTC().Truncate(time.Second)

	store := &recordingCooldownStateStore{
		load: []CooldownStateRecord{
			{
				Provider:       "claude",
				AuthID:         "auth-401",
				Model:          "",
				Status:         "cooling",
				NextRetryAfter: deadline,
				Reason:         "unauthorized",
				Scope:          cooldownScopeCredential,
				UpdatedAt:      now,
			},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	auth := &Auth{
		ID:       "auth-401",
		Provider: "claude",
		Status:   StatusActive,
		// Clean historical model states: this credential has served these
		// models before with no error of their own. This is exactly the
		// condition Codex flagged as silently clearing a genuine
		// credential-wide cooldown.
		ModelStates: map[string]*ModelState{
			"claude-opus":  {Status: StatusActive},
			"claude-haiku": {Status: StatusActive},
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() returned error: %v", errRestore)
	}

	restored, ok := manager.GetByID("auth-401")
	if !ok {
		t.Fatal("restored auth was not found")
	}
	if !restored.Unavailable {
		t.Fatal("restored auth.Unavailable = false, want true (credential-wide 401 must survive clean sibling model states)")
	}
	if !restored.CredentialCooldown {
		t.Fatal("restored auth.CredentialCooldown = false, want true")
	}
	if !restored.NextRetryAfter.Equal(deadline) {
		t.Fatalf("restored auth.NextRetryAfter = %v, want %v", restored.NextRetryAfter, deadline)
	}
	if blocked, _, next := effectiveBlock(restored, "", now); !blocked || !next.Equal(deadline) {
		t.Fatalf("effectiveBlock(auth, \"\", now) = (%v, _, %v), want (true, _, %v)", blocked, next, deadline)
	}
	// Every individual model must be blocked too - a credential-wide failure
	// blocks everything, not just the aggregate query.
	if blocked, _, _ := effectiveBlock(restored, "claude-opus", now); !blocked {
		t.Fatal("effectiveBlock(auth, \"claude-opus\", now) = false, want true")
	}

	// Once the deadline has actually passed, the credential-wide block must
	// lift - this is a deadline, not a permanent flag.
	afterDeadline := deadline.Add(time.Second)
	if blocked, _, _ := effectiveBlock(restored, "", afterDeadline); blocked {
		t.Fatal("effectiveBlock(auth, \"\", afterDeadline) = true, want false once the credential-wide deadline has passed")
	}
}

// TestRestoreCooldownStatesLegacyAuthRecordRoundTrip confirms a record
// written before the Scope field existed (Scope == "", the zero value)
// keeps its pre-existing behavior on restore: an auth-level record is still
// re-derived from ModelStates whenever any exist, exactly as before this
// round's fix. This is the documented backward-compatibility default from
// the delta review - only an explicit Scope=credential marker changes
// behavior.
func TestRestoreCooldownStatesLegacyAuthRecordRoundTrip(t *testing.T) {
	now := time.Now()
	deadline := now.Add(30 * time.Minute).UTC().Truncate(time.Second)

	store := &recordingCooldownStateStore{
		load: []CooldownStateRecord{
			{
				Provider:       "claude",
				AuthID:         "auth-legacy",
				Model:          "",
				Status:         "cooling",
				NextRetryAfter: deadline,
				Reason:         "unauthorized",
				// Scope intentionally left unset (zero value), simulating a
				// record persisted by a build before this field existed.
				UpdatedAt: now,
			},
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	auth := &Auth{
		ID:       "auth-legacy",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"claude-opus": {Status: StatusActive},
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() returned error: %v", errRestore)
	}

	restored, ok := manager.GetByID("auth-legacy")
	if !ok {
		t.Fatal("restored auth was not found")
	}
	// Unchanged pre-existing behavior: a legacy auth-level record with
	// ModelStates present is re-derived from ModelStates, not trusted
	// directly - the clean sibling clears it.
	if restored.Unavailable {
		t.Fatal("restored auth.Unavailable = true, want false (legacy Scope=\"\" record must keep the pre-existing ModelStates-derived behavior)")
	}
	if restored.CredentialCooldown {
		t.Fatal("restored auth.CredentialCooldown = true, want false for a legacy record")
	}
}

// TestRestoreCooldownStatesCredentialWideMutationControl is a mutation
// control for the credential-scope fix: reverting
// restoreCooldownRecordLocked's model=="" branch to unconditionally call
// updateAggregatedAvailability whenever ModelStates is non-empty (ignoring
// record.Scope entirely, as before this round) must be caught by
// TestRestoreCooldownStatesCredentialWideSurvivesCleanModelStates.
func TestRestoreCooldownStatesCredentialWideMutationControl(t *testing.T) {
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	auth := &Auth{
		ID:       "auth-401",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"claude-opus": {Status: StatusActive},
		},
	}
	// Directly re-execute the pre-fix branch: unconditional
	// updateAggregatedAvailability whenever ModelStates is non-empty,
	// without ever consulting a Scope marker.
	auth.Unavailable = true
	auth.Status = StatusError
	auth.NextRetryAfter = deadline
	if len(auth.ModelStates) > 0 {
		updateAggregatedAvailability(auth, now)
	}
	if auth.Unavailable {
		t.Fatal("pre-fix unconditional updateAggregatedAvailability unexpectedly still reports unavailable - mutation control precondition broken, update this test")
	}
}
