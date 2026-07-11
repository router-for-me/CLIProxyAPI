package auth

import (
	"context"
	"testing"
)

// persistCaptureStore mimics the file store, which mutates the persisted
// auth's metadata map in place while holding no manager lock.
type persistCaptureStore struct {
	saved []*Auth
}

func (s *persistCaptureStore) List(ctx context.Context) ([]*Auth, error) { return nil, nil }

func (s *persistCaptureStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = auth.Disabled
	s.saved = append(s.saved, auth)
	return "", nil
}

func (s *persistCaptureStore) Delete(ctx context.Context, id string) error { return nil }

func assertPersistedAuthDecoupled(t *testing.T, manager *Manager, store *persistCaptureStore, id string) {
	t.Helper()
	manager.mu.RLock()
	live := manager.auths[id]
	manager.mu.RUnlock()
	if live == nil {
		t.Fatalf("auth %s missing from manager", id)
	}
	if len(store.saved) == 0 {
		t.Fatal("store did not receive any save")
	}
	persisted := store.saved[len(store.saved)-1]
	if persisted == live {
		t.Fatal("persisted auth aliases the manager's stored auth pointer")
	}
	persisted.Metadata["mutation-probe"] = true
	manager.mu.RLock()
	_, leaked := live.Metadata["mutation-probe"]
	manager.mu.RUnlock()
	if leaked {
		t.Fatal("store mutation leaked into the manager's stored auth metadata")
	}
}

func TestUpdatePersistsDecoupledAuthCopy(t *testing.T) {
	store := &persistCaptureStore{}
	manager := NewManager(store, nil, nil)
	seed := &Auth{ID: "persist-alias-update", Provider: "xai", Metadata: map[string]any{"access_token": "one"}}
	if _, err := manager.Register(context.Background(), seed); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	updated := seed.Clone()
	updated.Metadata["access_token"] = "two"
	if _, err := manager.Update(context.Background(), updated); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	assertPersistedAuthDecoupled(t, manager, store, "persist-alias-update")
}

func TestUpdateRefreshedAuthPersistsDecoupledAuthCopy(t *testing.T) {
	store := &persistCaptureStore{}
	manager := NewManager(store, nil, nil)
	seed := &Auth{ID: "persist-alias-refresh", Provider: "xai", Metadata: map[string]any{"access_token": "one"}}
	if _, err := manager.Register(context.Background(), seed); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	manager.SetAuthRefreshCallback(func(context.Context, *Auth, *Auth) {})

	updated := seed.Clone()
	updated.Metadata["access_token"] = "two"
	if _, err := manager.updateRefreshedAuth(context.Background(), updated); err != nil {
		t.Fatalf("updateRefreshedAuth failed: %v", err)
	}
	assertPersistedAuthDecoupled(t, manager, store, "persist-alias-refresh")
}
