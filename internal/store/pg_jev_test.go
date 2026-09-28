package store

import (
	"context"
	"testing"
)

func newTestJevStore(t *testing.T) *JevStore {
	t.Helper()
	pg := newTestPostgresStore(t, "test_jev")
	// The shared helper does not clean jev_settings, so reset the singleton to
	// its seeded state. Without this, a test that leaves enabled=true makes the
	// next run's default assertion fail.
	js := NewJevStore(pg)
	if js == nil {
		t.Fatal("NewJevStore returned nil")
	}
	ctx := context.Background()
	if _, errExec := pg.DB().ExecContext(ctx, "DELETE FROM "+js.table); errExec != nil {
		t.Fatalf("reset jev_settings: %v", errExec)
	}
	if _, errExec := pg.DB().ExecContext(ctx, "INSERT INTO "+js.table+" (id) VALUES (1)"); errExec != nil {
		t.Fatalf("reseed jev_settings: %v", errExec)
	}
	return js
}

func TestJevStoreDefaultsDisabledWithPinnedModel(t *testing.T) {
	s := newTestJevStore(t)
	ctx := context.Background()

	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled {
		t.Error("enabled must default to false")
	}
	if got.APIKeySet {
		t.Error("api_key_set must default to false")
	}
	if got.Model != JevDefaultModel {
		t.Errorf("model = %q, want %q", got.Model, JevDefaultModel)
	}
}

func TestJevStoreUpsertKeyLifecycle(t *testing.T) {
	s := newTestJevStore(t)
	ctx := context.Background()
	defer func() {
		if _, errClean := s.Upsert(ctx, JevSettings{Model: JevDefaultModel}, ptrString("")); errClean != nil {
			t.Errorf("cleanup Upsert: %v", errClean)
		}
	}()

	key := "sk-ts-abcdef123456"
	got, err := s.Upsert(ctx, JevSettings{Enabled: true, Model: "jev-1.13.0"}, &key)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !got.Enabled {
		t.Error("enabled must round-trip")
	}
	if !got.APIKeySet {
		t.Error("api_key_set must be true after storing a key")
	}
	if got.APIKeyPrefix == "" || got.APIKeyPrefix == key {
		t.Errorf("prefix must be a mask, got %q", got.APIKeyPrefix)
	}

	// nil keeps the stored key.
	kept, err := s.Upsert(ctx, JevSettings{Enabled: false, Model: "jev-1.13.0"}, nil)
	if err != nil {
		t.Fatalf("Upsert keep: %v", err)
	}
	if kept.Enabled {
		t.Error("enabled must update to false")
	}
	if !kept.APIKeySet {
		t.Error("nil apiKey must keep the stored key")
	}
	if kept.APIKeyPrefix == key {
		t.Error("settings projection leaked the key")
	}

	// The plaintext key must survive the nil-keep write.
	plain, err := s.APIKey(ctx)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if plain != key {
		t.Errorf("APIKey = %q, want %q (nil must not clear)", plain, key)
	}

	// "" clears it.
	cleared, err := s.Upsert(ctx, JevSettings{Model: "jev-1.13.0"}, ptrString(""))
	if err != nil {
		t.Fatalf("Upsert clear: %v", err)
	}
	if cleared.APIKeySet {
		t.Error("empty apiKey must clear the stored key")
	}
	if cleared.APIKeyPrefix != "" {
		t.Errorf("cleared prefix must be empty, got %q", cleared.APIKeyPrefix)
	}
	gone, err := s.APIKey(ctx)
	if err != nil {
		t.Fatalf("APIKey after clear: %v", err)
	}
	if gone != "" {
		t.Errorf("APIKey = %q, want empty", gone)
	}
}

func TestJevStoreAPIKeyRoundTrips(t *testing.T) {
	s := newTestJevStore(t)
	ctx := context.Background()
	key := "sk-ts-roundtrip"
	defer func() {
		if _, errClean := s.Upsert(ctx, JevSettings{Model: JevDefaultModel}, ptrString("")); errClean != nil {
			t.Errorf("cleanup Upsert: %v", errClean)
		}
	}()

	if _, err := s.Upsert(ctx, JevSettings{Model: JevDefaultModel}, &key); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.APIKey(ctx)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != key {
		t.Errorf("APIKey = %q, want %q", got, key)
	}
}

func TestJevStoreDefaultsModelWhenBlank(t *testing.T) {
	s := newTestJevStore(t)
	ctx := context.Background()

	got, err := s.Upsert(ctx, JevSettings{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.Model != JevDefaultModel {
		t.Errorf("model = %q, want %q", got.Model, JevDefaultModel)
	}
}

func TestNewJevStoreNilParent(t *testing.T) {
	if s := NewJevStore(nil); s != nil {
		t.Fatalf("NewJevStore(nil) = %v, want nil", s)
	}
}

func ptrString(s string) *string { return &s }
