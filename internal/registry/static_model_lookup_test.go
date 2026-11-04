package registry

import (
	"reflect"
	"testing"
)

func TestLookupStaticModelInfoUsesActiveDevinCatalog(t *testing.T) {
	for _, model := range GetDevinModels() {
		got := LookupStaticModelInfo(model.ID)
		if !reflect.DeepEqual(got, model) {
			t.Errorf("lookup metadata differs from the active Devin catalog for %q", model.ID)
		}
	}
}

func TestLookupStaticModelInfoDevinRefreshAndFallback(t *testing.T) {
	modelsCatalogStore.mu.RLock()
	originalGeneral := modelsCatalogStore.data
	modelsCatalogStore.mu.RUnlock()
	devinCatalogStore.mu.RLock()
	originalModels := devinCatalogStore.models
	originalJSON := devinCatalogStore.rawJSON
	originalRevision := devinCatalogStore.revision
	devinCatalogStore.mu.RUnlock()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = originalGeneral
		modelsCatalogStore.mu.Unlock()
		devinCatalogStore.mu.Lock()
		devinCatalogStore.models = originalModels
		devinCatalogStore.rawJSON = originalJSON
		devinCatalogStore.revision = originalRevision
		devinCatalogStore.mu.Unlock()
	})

	legacy := &ModelInfo{ID: "devin/swe-2", DisplayName: "Legacy", MaxCompletionTokens: 128000}
	general := *originalGeneral
	general.Devin = []*ModelInfo{legacy}
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &general
	modelsCatalogStore.mu.Unlock()

	for _, payload := range []string{
		`{"devin":[{"id":"swe-2","max_completion_tokens":64000,"thinking":{"levels":["medium"]}}]}`,
		`{"devin":[{"id":"swe-2","max_completion_tokens":32000,"thinking":{"levels":["high"]}}]}`,
	} {
		if _, errLoad := loadDevinModelsFromBytes([]byte(payload), "test"); errLoad != nil {
			t.Fatal(errLoad)
		}
		for _, model := range GetDevinModels() {
			got := LookupStaticModelInfo(model.ID)
			if !reflect.DeepEqual(got, model) {
				t.Fatalf("lookup did not use refreshed metadata for %q: got %+v, want %+v", model.ID, got, model)
			}
			if got.Thinking != nil {
				got.Thinking.Levels[0] = "mutated"
				if !reflect.DeepEqual(LookupStaticModelInfo(model.ID), model) {
					t.Fatal("lookup result shares thinking metadata with the catalog")
				}
			}
		}
		if got := LookupStaticModelInfo("devin/gpt-6-astra"); got != nil {
			t.Fatalf("lookup resurrected a model absent from the active catalog: %+v", got)
		}
	}

	// Preserve legacy models.json compatibility when the independent catalog is unavailable.
	devinCatalogStore.mu.Lock()
	devinCatalogStore.models = nil
	devinCatalogStore.mu.Unlock()
	if got := LookupStaticModelInfo(legacy.ID); !reflect.DeepEqual(got, legacy) {
		t.Fatalf("legacy fallback = %+v, want %+v", got, legacy)
	}

	// Keep the hardcoded fallback only when both catalog sources are unavailable.
	generalWithoutDevin := general
	generalWithoutDevin.Devin = nil
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &generalWithoutDevin
	modelsCatalogStore.mu.Unlock()
	for _, model := range GetDevinModels() {
		if got := LookupStaticModelInfo(model.ID); !reflect.DeepEqual(got, model) {
			t.Errorf("hardcoded fallback for %q = %+v, want %+v", model.ID, got, model)
		}
	}
}

func TestLookupStaticModelInfoUsesEffectiveMetaCatalog(t *testing.T) {
	modelsCatalogStore.mu.RLock()
	original := modelsCatalogStore.data
	modelsCatalogStore.mu.RUnlock()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})
	for _, model := range GetMetaModels() {
		if got := LookupStaticModelInfo(model.ID); !reflect.DeepEqual(got, model) {
			t.Errorf("lookup metadata differs from the active Meta catalog for %q", model.ID)
		}
	}

	partial := *original
	partial.Meta = []*ModelInfo{{ID: "muse-spark-1.3", ContextLength: 12345}}
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &partial
	modelsCatalogStore.mu.Unlock()
	if got := LookupStaticModelInfo(partial.Meta[0].ID); !reflect.DeepEqual(got, partial.Meta[0]) {
		t.Fatalf("lookup did not use active Meta metadata: %+v", got)
	}
	if got := LookupStaticModelInfo("muse-spark-1.1"); got != nil {
		t.Fatalf("lookup resurrected a Meta model absent from the active catalog: %+v", got)
	}

	withoutMeta := partial
	withoutMeta.Meta = nil
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &withoutMeta
	modelsCatalogStore.mu.Unlock()
	for _, model := range GetMetaModels() {
		if got := LookupStaticModelInfo(model.ID); !reflect.DeepEqual(got, model) {
			t.Errorf("lookup metadata differs from the Meta fallback for %q", model.ID)
		}
	}
}
