package registry

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// mockModelStore is an in-memory ModelStoreOperator used by pg_sync tests.
type mockModelStore struct {
	mu              sync.Mutex
	upserts         [][]*ModelInfo
	selectAllResult []*ModelInfo
	selectAllErr    error
	upsertErr       error
	deleteErr       error
	countResult     int64
}

func (m *mockModelStore) UpsertModels(_ context.Context, models []*ModelInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.upserts = append(m.upserts, models)
	return nil
}

func (m *mockModelStore) SelectAllModels(_ context.Context) ([]*ModelInfo, error) {
	return m.selectAllResult, m.selectAllErr
}

func (m *mockModelStore) DeleteModelsByProvider(_ context.Context, _ string) (int64, error) {
	if m.deleteErr != nil {
		return 0, m.deleteErr
	}
	return 1, nil
}

func (m *mockModelStore) CountModels(_ context.Context) (int64, error) {
	return m.countResult, nil
}

func TestPGSyncNilStoreIsNoop(t *testing.T) {
	s := NewPGSync(nil)
	if s.Enabled() {
		t.Fatal("nil store should disable PGSync")
	}
	if err := s.UpsertModels(context.Background(), []*ModelInfo{{ID: "m"}}); err != nil {
		t.Fatalf("UpsertModels on disabled PGSync should be no-op: %v", err)
	}
	if _, err := s.Count(context.Background()); err != nil {
		t.Fatalf("Count on disabled PGSync should be no-op: %v", err)
	}
	if err := s.ReplaceProvider(context.Background(), "p", nil); err != nil {
		t.Fatalf("ReplaceProvider on disabled PGSync should be no-op: %v", err)
	}
}

func TestPGSyncUpsertDeduplicates(t *testing.T) {
	store := &mockModelStore{}
	s := NewPGSync(store)
	ctx := context.Background()
	m1 := &ModelInfo{ID: "gpt-4o", OwnedBy: "openai"}
	m2 := &ModelInfo{ID: "gpt-4o", OwnedBy: "openai"} // duplicate of m1
	m3 := &ModelInfo{ID: "claude-3", OwnedBy: "anthropic"}
	if err := s.UpsertModels(ctx, []*ModelInfo{m1, m2, m3, nil, {ID: ""}}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upsert count = %d; want 1", len(store.upserts))
	}
	passed := store.upserts[0]
	if len(passed) != 2 {
		t.Fatalf("dedupe failed: passed %d models; want 2", len(passed))
	}
}

func TestPGSyncUpsertPropagatesError(t *testing.T) {
	store := &mockModelStore{upsertErr: errors.New("db down")}
	s := NewPGSync(store)
	err := s.UpsertModels(context.Background(), []*ModelInfo{{ID: "x", OwnedBy: "p"}})
	if err == nil {
		t.Fatal("expected error from underlying store")
	}
}

func TestPGSyncLoadAllModels(t *testing.T) {
	expected := []*ModelInfo{{ID: "m1", OwnedBy: "p1"}}
	store := &mockModelStore{selectAllResult: expected}
	s := NewPGSync(store)
	got, err := s.LoadAllModels(context.Background())
	if err != nil {
		t.Fatalf("LoadAllModels: %v", err)
	}
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("LoadAllModels = %+v; want m1", got)
	}
}

func TestPGSyncReplaceProviderDeletesThenUpserts(t *testing.T) {
	store := &mockModelStore{}
	s := NewPGSync(store)
	if err := s.ReplaceProvider(context.Background(), "anthropic",
		[]*ModelInfo{{ID: "claude-x", OwnedBy: "anthropic"}}); err != nil {
		t.Fatalf("ReplaceProvider: %v", err)
	}
	if len(store.upserts) != 1 || len(store.upserts[0]) != 1 {
		t.Fatalf("upserts after replace = %v; want 1 model in 1 batch", store.upserts)
	}
}

func TestOwnedByAsProviderFallback(t *testing.T) {
	if got := OwnedByAsProvider(&ModelInfo{Type: "claude"}); got != "claude" {
		t.Errorf("fallback to Type failed: got %q", got)
	}
	if got := OwnedByAsProvider(&ModelInfo{OwnedBy: "anthropic", Type: "claude"}); got != "anthropic" {
		t.Errorf("OwnedBy not preferred: got %q", got)
	}
	if got := OwnedByAsProvider(nil); got != "" {
		t.Errorf("nil model should return empty provider; got %q", got)
	}
}

func TestPGSyncOnModelsRegisteredOnDisabled(t *testing.T) {
	// When disabled (nil store), the hook should not panic and should not
	// attempt any work.
	s := NewPGSync(nil)
	s.OnModelsRegistered(context.Background(), "p", "c", []*ModelInfo{{ID: "m"}})
	s.OnModelsUnregistered(context.Background(), "p", "c")
}
