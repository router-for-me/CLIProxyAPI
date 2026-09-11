package auth

import (
	"testing"
	"time"
)

// TestAffinityBindingMigratesAcrossReRender pins the re-render migration
// behavior: removing an OpenAI-compat auth whose ID is a hash of its API key
// stashes the session bindings, and a Register of the same logical credential
// (same base_url + compat_name equivalence key) inside the 30s window rebinds
// them onto the new ID.
func TestAffinityBindingMigratesAcrossReRender(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	sel := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager.SetSelector(sel)
	old := &Auth{ID: "openai-compatibility:p1:old", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example", "config_index": "0"}}
	if _, err := manager.Register(t.Context(), old); err != nil {
		t.Fatalf("Register old: %v", err)
	}
	sel.cache.Set("mixed::sess1::glm-5", old.ID)

	// Removal stashes the binding; the binding itself is invalidated.
	manager.Remove(t.Context(), old.ID)
	if _, ok := sel.cache.Get("mixed::sess1::glm-5"); ok {
		t.Fatal("binding should be invalidated on Remove")
	}

	replacement := &Auth{ID: "openai-compatibility:p1:new", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example", "config_index": "0"}}
	if _, err := manager.Register(t.Context(), replacement); err != nil {
		t.Fatalf("Register replacement: %v", err)
	}

	if id, ok := sel.cache.Get("mixed::sess1::glm-5"); !ok || id != replacement.ID {
		t.Fatalf("binding did not migrate: %q %v", id, ok)
	}
}

// TestAffinityBindingNotMigratedToDifferentCredential guards the negative
// case: a Register with a different equivalence key must not pick up the
// stashed bindings of a removed credential.
func TestAffinityBindingNotMigratedToDifferentCredential(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	sel := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager.SetSelector(sel)
	old := &Auth{ID: "openai-compatibility:p1:old", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example", "config_index": "0"}}
	if _, err := manager.Register(t.Context(), old); err != nil {
		t.Fatalf("Register old: %v", err)
	}
	sel.cache.Set("mixed::sess1::glm-5", old.ID)

	manager.Remove(t.Context(), old.ID)
	other := &Auth{ID: "openai-compatibility:p2:new", Provider: "openai-compatibility:p2", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://other.example", "compat_name": "Other", "config_index": "1"}}
	if _, err := manager.Register(t.Context(), other); err != nil {
		t.Fatalf("Register other: %v", err)
	}

	if _, ok := sel.cache.Get("mixed::sess1::glm-5"); ok {
		t.Fatal("binding must not migrate to a different credential")
	}
}

// TestAffinityBindingMigrationExpires pins the TTL bound: a stash whose
// expiry has passed must not rebind when a late replacement with the same
// equivalence key registers afterwards.
func TestAffinityBindingMigrationExpires(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	sel := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager.SetSelector(sel)
	old := &Auth{ID: "openai-compatibility:p1:old", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example", "config_index": "0"}}
	if _, err := manager.Register(t.Context(), old); err != nil {
		t.Fatalf("Register old: %v", err)
	}
	sel.cache.Set("mixed::sess1::glm-5", old.ID)

	manager.Remove(t.Context(), old.ID)

	// Force-expire the stash, then simulate a late replacement.
	manager.mu.Lock()
	for key, pending := range manager.pendingAffinityMigrations {
		pending.expiresAt = time.Now().Add(-time.Second)
		manager.pendingAffinityMigrations[key] = pending
	}
	manager.mu.Unlock()

	late := &Auth{ID: "openai-compatibility:p1:late", Provider: "openai-compatibility:p1", Status: StatusActive,
		Attributes: map[string]string{"base_url": "https://up.example", "compat_name": "Up Example", "config_index": "0"}}
	if _, err := manager.Register(t.Context(), late); err != nil {
		t.Fatalf("Register late: %v", err)
	}
	if _, ok := sel.cache.Get("mixed::sess1::glm-5"); ok {
		t.Fatal("stale stash must not rebind after its TTL window")
	}
}
