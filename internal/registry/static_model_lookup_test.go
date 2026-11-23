package registry

import (
	"reflect"
	"testing"
)

func TestLookupStaticModelInfoUsesActiveDevinCatalog(t *testing.T) {
	for _, model := range GetDevinModels() {
		want := *model
		want.ID = "devin/" + model.ID
		got := LookupStaticModelInfo(want.ID)
		if !reflect.DeepEqual(got, &want) {
			t.Errorf("lookup metadata differs from the active Devin catalog for %q", model.ID)
		}
		if got := LookupStaticModelInfoByChannel(model.ID, "devin"); !reflect.DeepEqual(got, model) {
			t.Errorf("channel lookup of bare ID %q = %+v, want %+v", model.ID, got, model)
		}
	}
	for _, id := range []string{"swe-2", "swe-2-lightning", devinBuiltinSWE16SlowID} {
		want := LookupStaticModelInfoByChannel(id, "devin")
		if got := LookupStaticModelInfo(id); want == nil || !reflect.DeepEqual(got, want) {
			t.Errorf("global lookup of bare Devin model %q = %+v, want %+v", id, got, want)
		}
		if got := LookupModelInfo(id, "devin"); !reflect.DeepEqual(got, want) {
			t.Errorf("runtime lookup of %q scoped to Devin = %+v, want %+v", id, got, want)
		}
		if got := LookupModelInfo(id, "openai"); got != nil {
			t.Errorf("runtime lookup of %q scoped to OpenAI inherited Devin metadata: %+v", id, got)
		}
		if got := LookupModelInfo(id); got != nil {
			t.Errorf("runtime lookup of unregistered %q inherited Devin metadata: %+v", id, got)
		}
	}
	if got := LookupDevinModel("devin/swe-2-high-lightning"); got == nil || got.ID != "swe-2-lightning" {
		t.Errorf("Devin runtime variant lookup = %+v, want swe-2-lightning", got)
	}
}

func TestLookupStaticModelInfoWithChannelQualifier(t *testing.T) {
	for _, tc := range []struct {
		channel string
		modelID string
		query   string
	}{
		{channel: "devin", modelID: "swe-2-lightning", query: "Devin/SWE-2-LIGHTNING"},
		{channel: "copilot", modelID: "gpt-6-sol", query: "copilot/gpt-6-sol"},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			native := LookupStaticModelInfoByChannel(tc.modelID, tc.channel)
			if native == nil || native.ID != tc.modelID {
				t.Fatalf("lookup of native model %q returned %+v", tc.modelID, native)
			}
			expected := *native
			expected.ID = tc.channel + "/" + native.ID
			if got := LookupStaticModelInfoByChannel(tc.query, tc.channel); !reflect.DeepEqual(got, &expected) {
				t.Errorf("scoped lookup of %q = %+v, want %+v", tc.query, got, expected)
			}
			if got := LookupStaticModelInfo(tc.query); !reflect.DeepEqual(got, &expected) {
				t.Errorf("global lookup of %q = %+v, want %+v", tc.query, got, expected)
			}
		})
	}
	if got := LookupStaticModelInfoByChannel("copilot/gpt-6-sol", "devin"); got != nil {
		t.Errorf("Devin lookup followed a Copilot qualifier: %+v", got)
	}
	want := findStaticModel(getModels().CodexPro, "gpt-6-sol")
	if got := LookupStaticModelInfo("gpt-6-sol"); want == nil || !reflect.DeepEqual(got, want) {
		t.Errorf("global lookup of shared model gpt-6-sol = %+v, want Codex definition %+v", got, want)
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
			for _, id := range []string{model.ID, "devin/" + model.ID} {
				want := *model
				want.ID = id
				got := LookupStaticModelInfo(id)
				if !reflect.DeepEqual(got, &want) {
					t.Fatalf("lookup did not use refreshed metadata for %q: got %+v, want %+v", id, got, &want)
				}
				if got.Thinking != nil {
					got.Thinking.Levels[0] = "mutated"
					if !reflect.DeepEqual(LookupStaticModelInfo(id), &want) {
						t.Fatal("lookup result shares thinking metadata with the catalog")
					}
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
	bareLegacy := *legacy
	bareLegacy.ID = "swe-2"
	if got := LookupStaticModelInfo(bareLegacy.ID); !reflect.DeepEqual(got, &bareLegacy) {
		t.Fatalf("bare legacy fallback = %+v, want %+v", got, &bareLegacy)
	}

	// Keep the independent built-in available when both catalog sources are unavailable.
	generalWithoutDevin := general
	generalWithoutDevin.Devin = nil
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &generalWithoutDevin
	modelsCatalogStore.mu.Unlock()
	models := GetDevinModels()
	if len(models) != 1 || models[0].ID != devinBuiltinSWE16SlowID {
		t.Fatalf("fallback models = %+v, want only %s", models, devinBuiltinSWE16SlowID)
	}
	if got := LookupStaticModelInfo(legacy.ID); got != nil {
		t.Fatalf("lookup restored absent catalog entry %q: %+v", legacy.ID, got)
	}
	want := *models[0]
	want.ID = "devin/" + want.ID
	if got := LookupStaticModelInfo(want.ID); !reflect.DeepEqual(got, &want) {
		t.Errorf("built-in fallback for %q = %+v, want %+v", want.ID, got, &want)
	}
	if got := LookupStaticModelInfo(models[0].ID); !reflect.DeepEqual(got, models[0]) {
		t.Errorf("bare built-in fallback for %q = %+v, want %+v", models[0].ID, got, models[0])
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
