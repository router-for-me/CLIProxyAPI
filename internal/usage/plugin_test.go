package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestLoggerPluginPersistsRecord(t *testing.T) {
	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	ctx := internallogging.WithEndpoint(context.Background(), "POST /v1/messages")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 200)

	plugin.HandleUsage(ctx, coreusage.Record{
		APIKey:           " api-key ",
		Provider:         " claude ",
		Model:            " claude-sonnet-4-6 ",
		Source:           " user@example.com ",
		AuthIndex:        " 0 ",
		AuthType:         " oauth ",
		RequestedAt:      time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		Latency:          1800 * time.Millisecond,
		FirstByteLatency: 320 * time.Millisecond,
		ThinkingEffort:   " high ",
		Detail: coreusage.Detail{
			InputTokens:     300,
			OutputTokens:    500,
			ReasoningTokens: 60,
			CachedTokens:    100,
		},
	})

	usage, err := store.Query(context.Background(), QueryRange{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	details := usage["api-key"]["claude-sonnet-4-6"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	got := details[0]
	if got.ID == "" {
		t.Fatalf("ID is empty")
	}
	if got.GenerationMs != 1480 {
		t.Fatalf("GenerationMs = %d, want 1480", got.GenerationMs)
	}
	if got.ThinkingEffort != "high" {
		t.Fatalf("ThinkingEffort = %q, want high", got.ThinkingEffort)
	}
}

func TestLoggerPluginSkipsWhenDisabled(t *testing.T) {
	previous := StatisticsEnabled()
	SetStatisticsEnabled(false)
	defer SetStatisticsEnabled(previous)

	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	ctx := internallogging.WithEndpoint(context.Background(), "POST /v1/messages")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 200)

	plugin.HandleUsage(ctx, coreusage.Record{
		APIKey:      "api-key",
		Model:       "claude-sonnet-4-6",
		RequestedAt: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		Latency:     time.Second,
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 1,
		},
	})

	usage, err := store.Query(context.Background(), QueryRange{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(usage) != 0 {
		t.Fatalf("usage len = %d, want 0: %+v", len(usage), usage)
	}
}

func TestReplaceDefaultStoreClosesPreviousStore(t *testing.T) {
	isolateDefaultRecorder(t)

	oldStore := &fakeStore{}
	newStore := &fakeStore{}

	if previous := defaultRecorder.SetStore(oldStore); previous != nil {
		t.Fatalf("SetStore() previous = %T, want nil", previous)
	}
	replaceDefaultStore(newStore)

	if oldStore.closeCalls != 1 {
		t.Fatalf("oldStore closeCalls = %d, want 1", oldStore.closeCalls)
	}
	if newStore.closeCalls != 0 {
		t.Fatalf("newStore closeCalls = %d, want 0", newStore.closeCalls)
	}
	if got := DefaultStore(); got != newStore {
		t.Fatalf("DefaultStore() = %T, want newStore", got)
	}
}

func TestCloseDefaultStoreClosesAndClearsActiveStore(t *testing.T) {
	isolateDefaultRecorder(t)

	store := &fakeStore{}
	defaultRecorder.SetStore(store)

	if err := CloseDefaultStore(); err != nil {
		t.Fatalf("CloseDefaultStore() error = %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("closeCalls = %d, want 1", store.closeCalls)
	}
	if got := DefaultStore(); got != nil {
		t.Fatalf("DefaultStore() = %T, want nil", got)
	}
}

func TestSetDefaultStoreForTestRestoresPreviousStoreWithoutClosing(t *testing.T) {
	isolateDefaultRecorder(t)

	previousStore := &fakeStore{}
	testStore := &fakeStore{}
	defaultRecorder.SetStore(previousStore)

	restore := SetDefaultStoreForTest(testStore)
	if got := DefaultStore(); got != testStore {
		t.Fatalf("DefaultStore() = %T, want testStore", got)
	}

	restore()
	if got := DefaultStore(); got != previousStore {
		t.Fatalf("DefaultStore() = %T, want previousStore", got)
	}
	if previousStore.closeCalls != 0 {
		t.Fatalf("previousStore closeCalls = %d, want 0", previousStore.closeCalls)
	}
	if testStore.closeCalls != 0 {
		t.Fatalf("testStore closeCalls = %d, want 0", testStore.closeCalls)
	}
}

type fakeStore struct {
	closeCalls int
}

func (s *fakeStore) Insert(ctx context.Context, record Record) error { return nil }

func (s *fakeStore) Query(ctx context.Context, rng QueryRange) (APIUsage, error) { return nil, nil }

func (s *fakeStore) Delete(ctx context.Context, ids []string) (DeleteResult, error) {
	return DeleteResult{}, nil
}

func (s *fakeStore) Close() error {
	s.closeCalls++
	return nil
}

func isolateDefaultRecorder(t *testing.T) {
	t.Helper()
	original := defaultRecorder
	defaultRecorder = NewRecorder(nil)
	t.Cleanup(func() {
		if defaultRecorder != nil {
			_ = CloseDefaultStore()
		}
		defaultRecorder = original
	})
}

func newPluginTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
