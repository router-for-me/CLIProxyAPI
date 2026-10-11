package registry

import (
	"slices"
	"testing"
)

func TestBuildDevinModelsFromUpstream_AggregatesVariants(t *testing.T) {
	models := BuildDevinModelsFromUpstream([]DevinUpstreamModel{
		{UID: "upstream-only-model-low", Label: "Upstream Only Low", ContextLength: 100000, VendorID: 2},
		{UID: "upstream-only-model-high", Label: "Upstream Only High", ContextLength: 200000, VendorID: 2, Multimodal: true},
		{UID: "upstream-only-model-high", Label: "duplicate"},
		{UID: "upstream-bare", Label: "Upstream Bare", ContextLength: 50000, VendorID: 3},
	})

	byID := make(map[string]*ModelInfo, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	m := byID["devin/upstream-only-model"]
	if m == nil {
		t.Fatalf("missing aggregated model, got %v", byID)
	}
	if m.DisplayName != "Upstream Only" || m.OwnedBy != "openai" || m.ContextLength != 200000 {
		t.Errorf("aggregated model = %+v", m)
	}
	if m.Thinking == nil || !slices.Equal(m.Thinking.Levels, []string{"low", "high"}) {
		t.Errorf("levels = %+v, want [low high]", m.Thinking)
	}
	if !slices.Contains(m.SupportedInputModalities, "image") {
		t.Errorf("input modalities = %v, want image", m.SupportedInputModalities)
	}
	if m.MaxCompletionTokens != devinUpstreamMaxCompletionTokens || m.OutputTokenLimit != devinUpstreamMaxCompletionTokens {
		t.Errorf("completion limits = %d/%d", m.MaxCompletionTokens, m.OutputTokenLimit)
	}
	if bare := byID["devin/upstream-bare"]; bare == nil || bare.Thinking != nil || bare.OwnedBy != "anthropic" {
		t.Errorf("bare model = %+v", bare)
	}
	if byID[devinBuiltinSWE16SlowID] == nil {
		t.Errorf("expected builtin %s", devinBuiltinSWE16SlowID)
	}
}

func TestBuildDevinModelsFromUpstream_PreservesCatalogLevels(t *testing.T) {
	var catalog *ModelInfo
	for _, m := range GetDevinModels() {
		if m.Thinking != nil && len(m.Thinking.Levels) > 1 {
			catalog = m
			break
		}
	}
	if catalog == nil {
		t.Skip("catalog has no multi-level model")
	}
	bare := catalog.ID[len("devin/"):]
	models := BuildDevinModelsFromUpstream([]DevinUpstreamModel{{UID: bare + "-" + catalog.Thinking.Levels[0], Label: catalog.DisplayName}})
	for _, m := range models {
		if m.ID == catalog.ID {
			if !slices.Equal(m.Thinking.Levels, catalog.Thinking.Levels) {
				t.Errorf("levels = %v, want %v", m.Thinking.Levels, catalog.Thinking.Levels)
			}
			return
		}
	}
	t.Fatalf("model %s not built", catalog.ID)
}

func TestLookupDevinModel_UsesUpstreamModels(t *testing.T) {
	id := "devin/lookup-upstream-test-model"
	t.Cleanup(func() {
		devinUpstreamStore.Lock()
		delete(devinUpstreamStore.models, id)
		devinUpstreamStore.Unlock()
	})
	if LookupDevinModel("lookup-upstream-test-model") != nil {
		t.Fatal("model unexpectedly present before recording")
	}
	RecordDevinUpstreamModels(BuildDevinModelsFromUpstream([]DevinUpstreamModel{
		{UID: "lookup-upstream-test-model-medium"},
		{UID: "lookup-upstream-test-model-xhigh"},
	}))
	for _, query := range []string{"lookup-upstream-test-model", "devin/lookup-upstream-test-model", "lookup-upstream-test-model-xhigh"} {
		m := LookupDevinModel(query)
		if m == nil || m.ID != id || m.Thinking == nil || !slices.Equal(m.Thinking.Levels, []string{"medium", "xhigh"}) {
			t.Errorf("LookupDevinModel(%q) = %+v", query, m)
		}
	}
}
