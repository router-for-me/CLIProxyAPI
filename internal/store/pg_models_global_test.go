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

// TestModelsStoreUpdateByIDUserDefined covers the "fan user_defined out across
// providers" regression: a model id fanned out across several upstream
// providers can end up with only some rows marked user_defined (the auto-sync
// path writes false unconditionally). The dashboard exposes a global
// "Apply globally" toggle that needs to align every row in one PUT, so the
// GlobalModelPatch must carry the user_defined column and UpdateByID must
// write it to every matching row.
func TestModelsStoreUpdateByIDUserDefined(t *testing.T) {
	store := newTestPostgresStore(t, "models_global_userdefined")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(store)
	rows := []StoredModel{
		{ID: "gpt-4o", Provider: "openai", Object: "model", OwnedBy: "openai", Type: "openai",
			DisplayName: "GPT-4o (openai)", UserDefined: true},
		{ID: "gpt-4o", Provider: "acme", Object: "model", OwnedBy: "acme", Type: "openai-compatibility",
			DisplayName: "GPT-4o (acme)", UserDefined: false},
		{ID: "gpt-4o", Provider: "mirror", Object: "model", OwnedBy: "mirror", Type: "openai-compatibility",
			DisplayName: "GPT-4o (mirror)", UserDefined: false},
		// Unrelated id — must NOT be touched by an UpdateByID("gpt-4o").
		{ID: "claude-3-5", Provider: "anthropic", Object: "model", OwnedBy: "anthropic", Type: "claude",
			DisplayName: "Claude 3.5", UserDefined: false},
	}
	if err := ms.UpsertModels(ctx, rows); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}

	// Apply only user_defined=true; the canonical attributes must survive.
	flag := true
	n, err := ms.UpdateByID(ctx, "gpt-4o", GlobalModelPatch{UserDefined: &flag})
	if err != nil {
		t.Fatalf("UpdateByID: %v", err)
	}
	if n != 3 {
		t.Fatalf("UpdateByID rows = %d; want 3 (all providers of gpt-4o)", n)
	}
	all, err := ms.SelectByIDAllProviders(ctx, "gpt-4o")
	if err != nil {
		t.Fatalf("SelectByIDAllProviders: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("SelectByIDAllProviders len = %d; want 3", len(all))
	}
	for _, m := range all {
		if m.ID != "gpt-4o" {
			t.Errorf("returned id = %q; want gpt-4o", m.ID)
		}
		if !m.UserDefined {
			t.Errorf("provider %s user_defined = false; want true (global fan-out must align all rows)", m.Provider)
		}
		// DisplayName is a nil-patch field → must survive unchanged.
		want := "GPT-4o (" + m.Provider + ")"
		if m.DisplayName != want {
			t.Errorf("provider %s display_name = %q; want %q (nil patch must not nullify)", m.Provider, m.DisplayName, want)
		}
	}

	// Flip the bit back to false and ensure the fan-out still reaches every
	// row (the symmetric regression path).
	flag = false
	if _, err := ms.UpdateByID(ctx, "gpt-4o", GlobalModelPatch{UserDefined: &flag}); err != nil {
		t.Fatalf("UpdateByID flip off: %v", err)
	}
	all, err = ms.SelectByIDAllProviders(ctx, "gpt-4o")
	if err != nil {
		t.Fatalf("SelectByIDAllProviders after flip: %v", err)
	}
	for _, m := range all {
		if m.UserDefined {
			t.Errorf("provider %s user_defined = true; want false after explicit flip", m.Provider)
		}
	}

	// The unrelated claude row must be untouched.
	claude, err := ms.SelectOne(ctx, "claude-3-5", "anthropic")
	if err != nil {
		t.Fatalf("SelectOne claude: %v", err)
	}
	if claude.UserDefined {
		t.Errorf("claude user_defined flipped to true; unrelated id must be untouched")
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

// TestModelsStoreGlobalModelRoute verifies the per-model-id global routing
// override round-trip: upsert persists + populates the cache, reads resolve
// case-insensitively, empty providers clears the route, and an unknown id reads
// as nil.
func TestModelsStoreGlobalModelRoute(t *testing.T) {
	store := newTestPostgresStore(t, "models_route_test")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(store)

	// Unknown id resolves nil.
	if r := ms.GlobalModelRoute(ctx, "gpt-4o"); r != nil {
		t.Fatalf("GlobalModelRoute(unknown) = %+v, want nil", r)
	}

	// Upsert a route; read back case-insensitively.
	err := ms.UpsertGlobalModelRoute(ctx, "gpt-4o", GlobalModelRouteUpsert{
		Providers:  []string{"openai", "acme"},
		Strategy:   "priority",
		Priorities: []ProviderPriority{{Provider: "acme", Priority: 10}},
	}, false)
	if err != nil {
		t.Fatalf("UpsertGlobalModelRoute: %v", err)
	}
	r := ms.GlobalModelRoute(ctx, "GPT-4O")
	if r == nil {
		t.Fatal("GlobalModelRoute = nil after upsert, want route")
	}
	if len(r.Providers) != 2 || r.Providers[0] != "openai" || r.Providers[1] != "acme" {
		t.Fatalf("route providers = %v, want [openai acme]", r.Providers)
	}
	if r.Strategy != "priority" {
		t.Fatalf("route strategy = %q, want priority", r.Strategy)
	}
	if len(r.Priorities) != 1 || r.Priorities[0].Provider != "acme" || r.Priorities[0].Priority != 10 {
		t.Fatalf("route priorities = %+v, want [acme=10]", r.Priorities)
	}

	// Clearing (empty=true) removes the route from the cache and DB.
	if err := ms.UpsertGlobalModelRoute(ctx, "gpt-4o", GlobalModelRouteUpsert{Providers: []string{"openai"}}, true); err != nil {
		t.Fatalf("UpsertGlobalModelRoute(clear): %v", err)
	}
	if r := ms.GlobalModelRoute(ctx, "gpt-4o"); r != nil {
		t.Fatalf("GlobalModelRoute after clear = %+v, want nil", r)
	}

	// An upsert with no normalized providers also clears the route.
	err = ms.UpsertGlobalModelRoute(ctx, "gpt-4o", GlobalModelRouteUpsert{Providers: []string{"  ", ""}}, false)
	if err != nil {
		t.Fatalf("UpsertGlobalModelRoute(empty providers): %v", err)
	}
	if r := ms.GlobalModelRoute(ctx, "gpt-4o"); r != nil {
		t.Fatalf("GlobalModelRoute after empty-provider upsert = %+v, want nil", r)
	}
}

// TestGlobalModelRouteUpsertNormalize verifies Normalize trims, de-dupes,
// lowercases strategy, and drops priorities referencing providers not listed.
func TestGlobalModelRouteUpsertNormalize(t *testing.T) {
	got := (GlobalModelRouteUpsert{
		Providers:  []string{" OpenCode ", "opencode", "", "cometapi"},
		Strategy:   "PRIORITY",
		Priorities: []ProviderPriority{{Provider: "opencode", Priority: 5}, {Provider: "ghost", Priority: 9}},
	}).Normalize()
	if len(got.Providers) != 2 || got.Providers[0] != "OpenCode" || got.Providers[1] != "cometapi" {
		t.Fatalf("providers = %v, want [OpenCode cometapi]", got.Providers)
	}
	if got.Strategy != "priority" {
		t.Fatalf("strategy = %q, want priority", got.Strategy)
	}
	if len(got.Priorities) != 1 || got.Priorities[0].Provider != "opencode" {
		t.Fatalf("priorities = %+v, want [opencode=5] (ghost dropped)", got.Priorities)
	}
}
