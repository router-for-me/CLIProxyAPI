package store

import (
	"context"
	"testing"
)

// TestModelsStoreUpdateByID verifies the Global Model fan-out primitive:
// UpdateByID applies a patch to every catalog row sharing a model id
// (case-insensitive), across providers, while leaving omitted (nil) fields
// untouched.
func TestModelsStoreUpdateByID(t *testing.T) {
	store := newTestPostgresStore(t, "models_global_test")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(store)

	// Seed the same model id under two providers with distinct attributes.
	rows := []StoredModel{
		{
			ID: "gpt-4o", Provider: "openai", Object: "model", Created: 1700000000,
			OwnedBy: "openai", Type: "openai", DisplayName: "GPT-4o",
			OfficialProvider: "openai", ContextLength: 128000, MaxCompletionTokens: 16384,
			Description: "original-openai",
		},
		{
			ID: "gpt-4o", Provider: "acme", Object: "model", Created: 1700000000,
			OwnedBy: "acme", Type: "openai-compatibility", DisplayName: "GPT-4o (acme)",
			OfficialProvider: "acme", ContextLength: 8000, MaxCompletionTokens: 4096,
			Description: "original-acme",
		},
		{
			// Unrelated id — must NOT be touched by an UpdateByID("gpt-4o").
			ID: "claude-3-5", Provider: "anthropic", Object: "model",
			OwnedBy: "anthropic", Type: "claude", DisplayName: "Claude 3.5",
			ContextLength: 200000,
		},
	}
	if err := ms.UpsertModels(ctx, rows); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}

	// Patch only context_length + official_provider; leave description untouched
	// so we can assert it survives.
	newContext := 200000
	newOfficial := "openai-official"
	n, err := ms.UpdateByID(ctx, "GPT-4O", GlobalModelPatch{
		ContextLength:    &newContext,
		OfficialProvider: &newOfficial,
	})
	if err != nil {
		t.Fatalf("UpdateByID: %v", err)
	}
	if n != 2 {
		t.Fatalf("UpdateByID rows = %d; want 2 (both providers of gpt-4o)", n)
	}

	all, err := ms.SelectByIDAllProviders(ctx, "gpt-4o")
	if err != nil {
		t.Fatalf("SelectByIDAllProviders: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("SelectByIDAllProviders len = %d; want 2", len(all))
	}
	for _, m := range all {
		if m.ID != "gpt-4o" {
			t.Errorf("returned id = %q; want gpt-4o", m.ID)
		}
		if m.ContextLength != newContext {
			t.Errorf("provider %s context_length = %d; want %d", m.Provider, m.ContextLength, newContext)
		}
		if m.OfficialProvider != newOfficial {
			t.Errorf("provider %s official_provider = %q; want %q", m.Provider, m.OfficialProvider, newOfficial)
		}
		// description is a nil-patch field → must survive unchanged.
		want := "original-" + m.Provider
		if m.Description != want {
			t.Errorf("provider %s description = %q; want %q (nil patch must not nullify)", m.Provider, m.Description, want)
		}
	}

	// The unrelated claude row must be untouched.
	claude, err := ms.SelectOne(ctx, "claude-3-5", "anthropic")
	if err != nil {
		t.Fatalf("SelectOne claude: %v", err)
	}
	if claude.ContextLength != 200000 {
		t.Errorf("claude context_length mutated to %d; want 200000", claude.ContextLength)
	}
	if claude.OfficialProvider == newOfficial {
		t.Errorf("claude official_provider changed to %q; unrelated id must be untouched", newOfficial)
	}
}

// TestModelsStoreUpdateByIDModalities asserts the *Modalities pointer fields
// replace the jsonb column in full (empty slice → "[]").
func TestModelsStoreUpdateByIDModalities(t *testing.T) {
	store := newTestPostgresStore(t, "models_global_mods")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(store)
	if err := ms.UpsertModels(ctx, []StoredModel{
		{ID: "m1", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t",
			InputModalities: []string{"TEXT", "IMAGE"}, OutputModalities: []string{"TEXT"}},
	}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	in := []string{"TEXT"}
	out := []string{}
	n, err := ms.UpdateByID(ctx, "m1", GlobalModelPatch{
		InputModalities:  &in,
		OutputModalities: &out,
	})
	if err != nil {
		t.Fatalf("UpdateByID: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows = %d; want 1", n)
	}
	got, err := ms.SelectOne(ctx, "m1", "p1")
	if err != nil {
		t.Fatalf("SelectOne: %v", err)
	}
	if len(got.InputModalities) != 1 || got.InputModalities[0] != "TEXT" {
		t.Errorf("input_modalities = %v; want [TEXT]", got.InputModalities)
	}
	if len(got.OutputModalities) != 0 {
		t.Errorf("output_modalities = %v; want []", got.OutputModalities)
	}
}

// TestModelsStoreUpdateByIDEmptyPatch covers the no-op path: a patch with no
// non-nil fields updates nothing and returns 0 without error.
func TestModelsStoreUpdateByIDEmptyPatch(t *testing.T) {
	store := newTestPostgresStore(t, "models_global_empty")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(store)
	if err := ms.UpsertModels(ctx, []StoredModel{
		{ID: "m2", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t", DisplayName: "M2"},
	}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	n, err := ms.UpdateByID(ctx, "m2", GlobalModelPatch{})
	if err != nil {
		t.Fatalf("UpdateByID empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("empty patch rows = %d; want 0", n)
	}
}

// TestModelsStoreSelectByIDAllProvidersNilSafe asserts the no-PG / empty-id
// guards return errors (never panic), mirroring TestModelsStoreNilSafe.
func TestModelsStoreSelectByIDAllProvidersNilSafe(t *testing.T) {
	var ms *ModelsStore
	_, err := ms.SelectByIDAllProviders(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error on nil ModelsStore SelectByIDAllProviders")
	}
	_, err = ms.UpdateByID(context.Background(), "x", GlobalModelPatch{OfficialProvider: strPtr("o")})
	if err == nil {
		t.Fatal("expected error on nil ModelsStore UpdateByID")
	}
}

// strPtr is a tiny helper to take the address of a string literal (Go does not
// allow &"literal").
func strPtr(s string) *string { return &s }
