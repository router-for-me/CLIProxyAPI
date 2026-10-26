package auth

import (
	"context"
	"testing"
)

type duplicateIndexStore struct {
	auths []*Auth
}

func (s *duplicateIndexStore) List(context.Context) ([]*Auth, error) {
	out := make([]*Auth, 0, len(s.auths))
	for _, auth := range s.auths {
		out = append(out, auth.Clone())
	}
	return out, nil
}
func (*duplicateIndexStore) Save(context.Context, *Auth) (string, error) { return "", nil }
func (*duplicateIndexStore) Delete(context.Context, string) error        { return nil }

func TestManagerLoadDeduplicatesAuthEntriesByIndex(t *testing.T) {
	store := &duplicateIndexStore{auths: []*Auth{
		{ID: "zcode-eyJh---1KC8", Index: "same-index", Provider: "zcode"},
		{ID: "zcode-eyjh---1kc8", Index: "same-index", Provider: "zcode"},
		{ID: "zcode-eyJh---gr1Q", Index: "other-index", Provider: "zcode"},
	}}
	manager := NewManager(store, nil, nil)
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	got := manager.List()
	if len(got) != 2 {
		t.Fatalf("List() returned %d auth entries, want 2", len(got))
	}
	if got[0].Index == got[1].Index {
		t.Fatalf("List() returned duplicate auth index %q", got[0].Index)
	}
}
