package auth

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

type revisionResetStore struct {
	mu   sync.Mutex
	auth *Auth
}

func (s *revisionResetStore) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auth == nil {
		return nil, nil
	}
	return []*Auth{s.auth.Clone()}, nil
}

func (s *revisionResetStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = auth.Clone()
	s.auth.revision = ""
	return auth.ID, nil
}

func (*revisionResetStore) Delete(context.Context, string) error { return nil }

func TestManagerRegisterAssignsOpaqueProcessLocalRevision(t *testing.T) {
	manager := NewManager(nil, nil, nil)

	registered, err := manager.Register(context.Background(), &Auth{
		ID:       "synthetic-auth.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if registered == nil {
		t.Fatal("Register() returned nil auth")
	}
	if got := registered.Revision(); len(got) < 22 {
		t.Fatalf("Revision() = %q, want opaque 128-bit-or-stronger token", got)
	}

	raw, err := json.Marshal(registered)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(raw), registered.Revision()) {
		t.Fatalf("serialized auth leaked process-local revision: %s", raw)
	}
}

func TestManagerLoadRegeneratesRevisionAfterProcessRestart(t *testing.T) {
	store := &revisionResetStore{}
	firstManager := NewManager(store, nil, nil)
	registered, err := firstManager.Register(context.Background(), &Auth{
		ID:       "synthetic-auth.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	secondManager := NewManager(store, nil, nil)
	if err = secondManager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loaded, ok := secondManager.GetByID(registered.ID)
	if !ok {
		t.Fatal("GetByID() missing loaded auth")
	}
	if loaded.Revision() == "" {
		t.Fatal("loaded revision is empty")
	}
	if loaded.Revision() == registered.Revision() {
		t.Fatalf("loaded revision reused prior-process token %q", loaded.Revision())
	}
}
