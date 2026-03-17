package registry

import (
	"strings"
	"testing"
)

func TestGetCodexFreeModels_BackfillsPreTierSplitModels(t *testing.T) {
	original := getModels()

	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &staticModelsJSON{
		CodexFree: []*ModelInfo{
			{ID: "gpt-5"},
			{ID: "gpt-5.2-codex"},
		},
		CodexPlus: []*ModelInfo{
			{ID: "gpt-5.3-codex"},
			{ID: "gpt-5.4"},
		},
		CodexTeam: []*ModelInfo{
			{ID: "gpt-5.3-codex"},
			{ID: "gpt-5.4"},
		},
		CodexPro: []*ModelInfo{
			{ID: "gpt-5.3-codex"},
			{ID: "gpt-5.4"},
		},
	}
	modelsCatalogStore.mu.Unlock()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})

	models := GetCodexFreeModels()
	if len(models) != 4 {
		t.Fatalf("expected 4 free models after backfill, got %d", len(models))
	}

	got := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			t.Fatal("expected model entry, got nil")
		}
		got = append(got, model.ID)
		normalizedID := strings.ToLower(strings.TrimSpace(model.ID))
		if _, ok := seen[normalizedID]; ok {
			t.Fatalf("expected unique model IDs, got duplicate %q", model.ID)
		}
		seen[normalizedID] = struct{}{}
	}

	want := []string{"gpt-5", "gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.4"}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("expected model %d to be %q, got %q", i, id, got[i])
		}
	}
}

func TestGetCodexFreeModels_DoesNotDuplicateCaseVariants(t *testing.T) {
	original := getModels()

	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &staticModelsJSON{
		CodexFree: []*ModelInfo{
			{ID: "GPT-5.4"},
		},
		CodexPlus: []*ModelInfo{
			{ID: "gpt-5.4"},
			{ID: "gpt-5.3-codex"},
		},
		CodexTeam: []*ModelInfo{{ID: "gpt-5.3-codex"}},
		CodexPro:  []*ModelInfo{{ID: "gpt-5.3-codex"}},
	}
	modelsCatalogStore.mu.Unlock()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})

	models := GetCodexFreeModels()
	if len(models) != 2 {
		t.Fatalf("expected 2 free models without case-duplicate entries, got %d", len(models))
	}

	gotNormalized := make(map[string]int, len(models))
	for _, model := range models {
		if model == nil {
			t.Fatal("expected model entry, got nil")
		}
		gotNormalized[strings.ToLower(strings.TrimSpace(model.ID))]++
	}

	if gotNormalized["gpt-5.4"] != 1 {
		t.Fatalf("expected exactly one gpt-5.4 variant, got %d", gotNormalized["gpt-5.4"])
	}
	if gotNormalized["gpt-5.3-codex"] != 1 {
		t.Fatalf("expected exactly one gpt-5.3-codex entry, got %d", gotNormalized["gpt-5.3-codex"])
	}
}
