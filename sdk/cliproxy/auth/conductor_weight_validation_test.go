package auth

import (
	"context"
	"encoding/json"
	"testing"
)

type weightValidationStore struct {
	auths     []*Auth
	saveCount int
}

func (s *weightValidationStore) List(context.Context) ([]*Auth, error) {
	return s.auths, nil
}

func (s *weightValidationStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount++
	return "", nil
}

func (s *weightValidationStore) Delete(context.Context, string) error {
	return nil
}

func TestManagerLoadSkipsInvalidExplicitWeights(t *testing.T) {
	store := &weightValidationStore{auths: []*Auth{
		{ID: "omitted", Provider: "test"},
		{ID: "zero", Provider: "test", Metadata: map[string]any{AttributeWeight: json.Number("0")}},
		{ID: "fraction", Provider: "test", Metadata: map[string]any{AttributeWeight: json.Number("1.5")}},
		{ID: "overflow", Provider: "test", Attributes: map[string]string{AttributeWeight: "9223372036854775808"}},
	}}
	manager := NewManager(store, nil, nil)

	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	if _, ok := manager.GetByID("omitted"); !ok {
		t.Fatal("omitted weight auth was not loaded")
	}
	if _, ok := manager.GetByID("zero"); !ok {
		t.Fatal("zero weight auth was not loaded")
	}
	for _, id := range []string{"fraction", "overflow"} {
		if _, ok := manager.GetByID(id); ok {
			t.Fatalf("invalid auth %q remained active after Load()", id)
		}
	}
}

func TestManagerRegisterAndUpdateRejectInvalidExplicitWeights(t *testing.T) {
	store := &weightValidationStore{}
	manager := NewManager(store, nil, nil)
	ctx := context.Background()

	invalid := &Auth{
		ID:       "invalid",
		Provider: "test",
		Metadata: map[string]any{AttributeWeight: "nonnumeric"},
	}
	if _, errRegister := manager.Register(ctx, invalid); errRegister == nil {
		t.Fatal("Register() accepted an invalid weight")
	}
	if _, ok := manager.GetByID(invalid.ID); ok {
		t.Fatal("invalid registered auth became active")
	}
	if store.saveCount != 0 {
		t.Fatalf("invalid Register() save count = %d, want 0", store.saveCount)
	}

	valid := &Auth{
		ID:         "valid",
		Provider:   "test",
		Attributes: map[string]string{AttributeWeight: "2"},
		Metadata:   map[string]any{"type": "test"},
	}
	if _, errRegister := manager.Register(ctx, valid); errRegister != nil {
		t.Fatalf("Register(valid) error = %v", errRegister)
	}
	invalidUpdate := valid.Clone()
	invalidUpdate.Attributes[AttributeWeight] = "1000001"
	if _, errUpdate := manager.Update(ctx, invalidUpdate); errUpdate == nil {
		t.Fatal("Update() accepted an invalid weight")
	}
	current, ok := manager.GetByID(valid.ID)
	if !ok || current.Attributes[AttributeWeight] != "2" {
		t.Fatalf("invalid Update() changed active auth: %#v", current)
	}
	if store.saveCount != 1 {
		t.Fatalf("save count = %d, want only the valid Register() save", store.saveCount)
	}
}
