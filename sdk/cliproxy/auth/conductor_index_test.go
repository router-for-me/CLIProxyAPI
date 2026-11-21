package auth

import (
	"context"
	"sync"
	"testing"
)

func TestManagerGetByIndexSnapshotAndNormalization(t *testing.T) {
	var absent *Manager
	if got, ok := absent.GetByIndex("index"); got != nil || ok {
		t.Fatal("nil manager returned a credential")
	}
	stored := &Auth{
		ID: "fixture.json", Index: "  index  ",
		Attributes:  map[string]string{"label": "original"},
		Metadata:    map[string]any{"type": "codex"},
		ModelStates: map[string]*ModelState{"model": {Status: StatusActive, LastError: &Error{Message: "original"}}},
	}
	manager := NewManager(nil, nil, nil)
	manager.auths[stored.ID] = stored
	manager.auths["nil-entry"] = nil
	for _, missing := range []string{"", "  ", "missing"} {
		if got, ok := manager.GetByIndex(missing); got != nil || ok {
			t.Fatalf("unexpected match for %q", missing)
		}
	}
	got, ok := manager.GetByIndex(" index ")
	if !ok || got.ID != stored.ID || got.Index != "index" {
		t.Fatal("lookup did not normalize and resolve the index")
	}
	got.Attributes["label"] = "changed"
	got.Metadata["type"] = "changed"
	got.ModelStates["model"].LastError.Message = "changed"
	if stored.Attributes["label"] != "original" || stored.Metadata["type"] != "codex" || stored.ModelStates["model"].LastError.Message != "original" {
		t.Fatal("lookup returned mutable manager-owned state")
	}
	if stored.Index != "  index  " || stored.indexAssigned {
		t.Fatal("lookup mutated the stored index under the read lock")
	}
}

func TestManagerGetByIndexLazyIndexDoesNotMutate(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	stored := &Auth{ID: "lazy.json", Provider: "codex"}
	manager.auths[stored.ID] = stored
	copy := *stored
	index := copy.EnsureIndex()
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 50 {
				got, ok := manager.GetByIndex(index)
				if !ok || got.Index != index {
					t.Error("lazy index was not resolved")
				}
			}
		})
	}
	readers.Wait()
	if stored.Index != "" || stored.indexAssigned {
		t.Fatal("lazy lookup mutated manager-owned state")
	}
}

func TestManagerGetByIndexTracksLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	registered, errRegister := manager.Register(ctx, &Auth{ID: "lifecycle.json", Provider: "codex", Status: StatusActive})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	if _, ok := manager.GetByIndex(registered.Index); !ok {
		t.Fatal("registered auth is missing")
	}
	originalIndex := registered.Index
	registered.Index = "replacement-index"
	if _, errUpdate := manager.Update(ctx, registered); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if _, ok := manager.GetByIndex(originalIndex); ok {
		t.Fatal("old index survived an update")
	}
	if _, ok := manager.GetByIndex(registered.Index); !ok {
		t.Fatal("new index is missing")
	}
	manager.Remove(ctx, registered.ID)
	if _, ok := manager.GetByIndex(registered.Index); ok {
		t.Fatal("removed auth is still visible")
	}
	store.mu.Lock()
	store.auths = map[string]*Auth{"loaded.json": {ID: "loaded.json", Index: "loaded-index", Provider: "codex"}}
	store.mu.Unlock()
	if errLoad := manager.Load(ctx); errLoad != nil {
		t.Fatal(errLoad)
	}
	if _, ok := manager.GetByIndex(registered.Index); ok {
		t.Fatal("stale index survived reload")
	}
	if got, ok := manager.GetByIndex("loaded-index"); !ok || got.ID != "loaded.json" {
		t.Fatal("reloaded index is missing")
	}
}
