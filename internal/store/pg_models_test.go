package store

import (
	"context"
	"testing"
)

func TestModelsStoreUpsertAndSelect(t *testing.T) {
	store := newTestPostgresStore(t, "models_test")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(store)

	models := []StoredModel{
		{
			ID: "gpt-4o", Provider: "openai", Object: "model", Created: 1700000000,
			OwnedBy: "openai", Type: "openai", DisplayName: "GPT-4o",
			ContextLength: 128000, MaxCompletionTokens: 16384,
			InputModalities:   []string{"TEXT", "IMAGE"},
			OutputModalities:  []string{"TEXT"},
			SupportsWebSearch: true,
			Thinking:          map[string]any{"min": 1024, "max": 128000, "zero_allowed": true},
			OverrideHeader:    map[string]string{"user-agent": "custom"},
		},
		{
			ID: "claude-3-5-sonnet", Provider: "anthropic", Object: "model",
			OwnedBy: "anthropic", Type: "claude", DisplayName: "Claude 3.5 Sonnet",
			ContextLength:       200000,
			SupportedParameters: []string{"max_tokens", "stop"},
		},
	}
	if err := ms.UpsertModels(ctx, models); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	count, err := ms.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count < 2 {
		t.Fatalf("count = %d; want >=2", count)
	}
	all, err := ms.SelectAll(ctx)
	if err != nil {
		t.Fatalf("SelectAll: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("SelectAll len = %d; want >=2", len(all))
	}

	// Upsert again with a changed field for the same (id, provider) — should
	// update, not insert a duplicate.
	updated := models[0]
	updated.DisplayName = "GPT-4o (revised)"
	if err := ms.UpsertModels(ctx, []StoredModel{updated}); err != nil {
		t.Fatalf("UpsertModels (2): %v", err)
	}
	all2, _ := ms.SelectAll(ctx)
	var found bool
	for _, m := range all2 {
		if m.ID == updated.ID && m.Provider == updated.Provider {
			if m.DisplayName != "GPT-4o (revised)" {
				t.Fatalf("display name not updated: %q", m.DisplayName)
			}
			if !m.SupportsWebSearch {
				t.Errorf("supports_web_search lost on upsert")
			}
			if len(m.Thinking) == 0 {
				t.Errorf("thinking config lost on upsert")
			}
			if v, ok := m.OverrideHeader["user-agent"]; !ok || v != "custom" {
				t.Errorf("override header lost: %v", m.OverrideHeader)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("upserted model not found in SelectAll")
	}

	byProvider, err := ms.SelectByProvider(ctx, "anthropic")
	if err != nil {
		t.Fatalf("SelectByProvider: %v", err)
	}
	if len(byProvider) != 1 {
		t.Fatalf("SelectByProvider anthropic len = %d; want 1", len(byProvider))
	}
}

func TestModelsStoreDeleteByProvider(t *testing.T) {
	store := newTestPostgresStore(t, "models_test2")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(store)
	if err := ms.UpsertModels(ctx, []StoredModel{
		{ID: "a", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "b", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "c", Provider: "p2", Object: "model", OwnedBy: "p2", Type: "t"},
	}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	n, err := ms.DeleteByProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("DeleteByProvider: %v", err)
	}
	if n != 2 {
		t.Fatalf("DeleteByProvider rows = %d; want 2", n)
	}
	count, _ := ms.Count(ctx)
	if count != 1 {
		t.Fatalf("count after delete = %d; want 1", count)
	}
}

func TestModelsStoreNilSafe(t *testing.T) {
	var ms *ModelsStore
	err := ms.UpsertModels(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on nil ModelsStore")
	}
	_, err = ms.SelectAll(context.Background())
	if err == nil {
		t.Fatal("expected error on nil SelectAll")
	}
}

func TestModelsStorePagedFilterPricedAndExclude(t *testing.T) {
	st := newTestPostgresStore(t, "models_filter_test")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(st)
	us := NewUsageStore(st)

	if err := ms.UpsertModels(ctx, []StoredModel{
		{ID: "a", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "b", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "c", Provider: "p2", Object: "model", OwnedBy: "p2", Type: "t"},
	}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	if err := us.UpsertPricing(ctx, Pricing{ID: "a", InputPer1M: 1.0}); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}

	priced := true
	rows, total, err := ms.SelectAllPagedFilter(ctx, 1, 25,
		ModelsListFilter{Priced: &priced, PricingTable: us.PricingTable()},
		ModelsListSort{Column: "id", Ascending: true})
	if err != nil {
		t.Fatalf("SelectAllPagedFilter priced: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "a" {
		t.Fatalf("priced filter: total=%d rows=%v; want only a", total, rows)
	}

	unpriced := false
	rows, total, err = ms.SelectAllPagedFilter(ctx, 1, 25,
		ModelsListFilter{Priced: &unpriced, PricingTable: us.PricingTable()},
		ModelsListSort{Column: "id", Ascending: true})
	if err != nil {
		t.Fatalf("SelectAllPagedFilter unpriced: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("unpriced filter: total=%d len=%d; want 2", total, len(rows))
	}

	rows, total, err = ms.SelectAllPagedFilter(ctx, 1, 25,
		ModelsListFilter{ExcludeIDFilter: []string{"b"}},
		ModelsListSort{Column: "id", Ascending: true})
	if err != nil {
		t.Fatalf("SelectAllPagedFilter exclude: %v", err)
	}
	if total != 2 {
		t.Fatalf("exclude filter total=%d; want 2", total)
	}
	for _, r := range rows {
		if r.ID == "b" {
			t.Fatalf("excluded id b still returned")
		}
	}
}

func TestModelsStoreDistinctPagedFilterPricedAndExclude(t *testing.T) {
	st := newTestPostgresStore(t, "models_distinct_filter_test")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(st)
	us := NewUsageStore(st)

	// Model "a" is served by two providers so the distinct variant must
	// collapse it to one row while the priced/exclude predicates still apply.
	if err := ms.UpsertModels(ctx, []StoredModel{
		{ID: "a", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "a", Provider: "p2", Object: "model", OwnedBy: "p2", Type: "t"},
		{ID: "b", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "c", Provider: "p2", Object: "model", OwnedBy: "p2", Type: "t"},
	}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	if err := us.UpsertPricing(ctx, Pricing{ID: "a", InputPer1M: 1.0}); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}

	priced := true
	rows, total, err := ms.SelectAllDistinctPagedFilter(ctx, 1, 25,
		ModelsListFilter{Priced: &priced, PricingTable: us.PricingTable()},
		ModelsListSort{Column: "id", Ascending: true})
	if err != nil {
		t.Fatalf("SelectAllDistinctPagedFilter priced: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != "a" {
		t.Fatalf("distinct priced filter: total=%d rows=%v; want only a", total, rows)
	}

	unpriced := false
	rows, total, err = ms.SelectAllDistinctPagedFilter(ctx, 1, 25,
		ModelsListFilter{Priced: &unpriced, PricingTable: us.PricingTable()},
		ModelsListSort{Column: "id", Ascending: true})
	if err != nil {
		t.Fatalf("SelectAllDistinctPagedFilter unpriced: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("distinct unpriced filter: total=%d len=%d; want 2", total, len(rows))
	}

	rows, total, err = ms.SelectAllDistinctPagedFilter(ctx, 1, 25,
		ModelsListFilter{ExcludeIDFilter: []string{"b"}},
		ModelsListSort{Column: "id", Ascending: true})
	if err != nil {
		t.Fatalf("SelectAllDistinctPagedFilter exclude: %v", err)
	}
	if total != 2 {
		t.Fatalf("distinct exclude filter total=%d; want 2", total)
	}
	for _, r := range rows {
		if r.ID == "b" {
			t.Fatalf("excluded id b still returned")
		}
	}
}

func TestModelsStoreProviderCountsByIDs(t *testing.T) {
	st := newTestPostgresStore(t, "models_pcount_test")
	ctx := cancelableTestCtx(t)
	ms := NewModelsStore(st)
	if err := ms.UpsertModels(ctx, []StoredModel{
		{ID: "a", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "a", Provider: "p2", Object: "model", OwnedBy: "p2", Type: "t"},
		{ID: "b", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
		{ID: "Llama-3.3-70B", Provider: "p1", Object: "model", OwnedBy: "p1", Type: "t"},
	}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	counts, err := ms.ProviderCountsByIDs(ctx, []string{"a", "b", "missing"})
	if err != nil {
		t.Fatalf("ProviderCountsByIDs: %v", err)
	}
	if counts["a"] != 2 || counts["b"] != 1 {
		t.Fatalf("counts = %v; want a=2 b=1", counts)
	}
	if _, ok := counts["missing"]; ok {
		t.Fatalf("unexpected count for missing id: %v", counts)
	}

	// Mixed-case id: the query is case-insensitive and keys are lowercased.
	mixedCase, err := ms.ProviderCountsByIDs(ctx, []string{"Llama-3.3-70B"})
	if err != nil {
		t.Fatalf("ProviderCountsByIDs mixed-case: %v", err)
	}
	if mixedCase["llama-3.3-70b"] != 1 {
		t.Fatalf("mixed-case counts = %v; want llama-3.3-70b=1", mixedCase)
	}

	// nil input returns a non-nil empty map with no error.
	empty, err := ms.ProviderCountsByIDs(ctx, nil)
	if err != nil {
		t.Fatalf("ProviderCountsByIDs(nil): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("ProviderCountsByIDs(nil) = %v; want non-nil empty map", empty)
	}
}
