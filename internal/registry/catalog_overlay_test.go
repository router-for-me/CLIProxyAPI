package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeOverlay(t *testing.T, doc any) string {
	t.Helper()
	data, errMarshal := json.Marshal(doc)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	path := filepath.Join(t.TempDir(), "overlay.json")
	if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	return path
}

func sectionIDs(t *testing.T, data []byte, section string) map[string]int {
	t.Helper()
	var doc map[string][]map[string]any
	if errUnmarshal := json.Unmarshal(data, &doc); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	ids := map[string]int{}
	for _, entry := range doc[section] {
		id, _ := entry["id"].(string)
		ids[id]++
	}
	return ids
}

func TestMergeCatalogOverlayAddsMissingAndKeepsOfficial(t *testing.T) {
	var base staticModelsJSON
	if errUnmarshal := json.Unmarshal(embeddedModelsJSON, &base); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(base.Antigravity) == 0 {
		t.Fatal("embedded catalog has no antigravity section")
	}
	existing := base.Antigravity[0].ID
	overlay := writeOverlay(t, map[string]any{
		"antigravity": []map[string]any{
			{"id": "overlay-only-model", "object": "model", "owned_by": "antigravity", "type": "antigravity"},
			{"id": existing, "object": "model", "owned_by": "antigravity", "type": "antigravity", "description": "overlay must not win"},
		},
	})
	merged, errMerge := mergeCatalogOverlay(context.Background(), embeddedModelsJSON, overlay)
	if errMerge != nil {
		t.Fatal(errMerge)
	}
	if errValidate := validateCatalogBytes(merged); errValidate != nil {
		t.Fatalf("merged catalog invalid: %v", errValidate)
	}
	ids := sectionIDs(t, merged, "antigravity")
	if ids["overlay-only-model"] != 1 || ids[existing] != 1 {
		t.Fatalf("unexpected ids: %v", ids)
	}
	var parsed staticModelsJSON
	if errUnmarshal := json.Unmarshal(merged, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	for _, model := range parsed.Antigravity {
		if model.ID == existing && model.Description == "overlay must not win" {
			t.Fatal("overlay replaced an official entry")
		}
	}
	if len(parsed.Claude) != len(base.Claude) {
		t.Fatal("untouched section changed")
	}
}

func TestMergeCatalogOverlayRejectsBrokenOverlay(t *testing.T) {
	for _, doc := range []any{
		map[string]any{"antigravity": []map[string]any{{"object": "model"}}},
		"not an object",
	} {
		if _, errMerge := mergeCatalogOverlay(context.Background(), embeddedModelsJSON, writeOverlay(t, doc)); errMerge == nil {
			t.Fatalf("accepted broken overlay %v", doc)
		}
	}
	if _, errMerge := mergeCatalogOverlay(context.Background(), embeddedModelsJSON, filepath.Join(t.TempDir(), "missing.json")); errMerge == nil {
		t.Fatal("accepted missing overlay")
	}
}

func TestCatalogUpdaterAppliesOverlayAndRestartsOnOverlayChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published := make(chan []byte, 4)
	u := &catalogUpdater{
		fetch:   catalogFetcher(embeddedModelsJSON, nil, validateCatalogBytes),
		merge:   mergeCatalogOverlay,
		publish: func(data []byte) ([]string, error) { published <- data; return nil, nil },
	}
	first := writeOverlay(t, map[string]any{"antigravity": []map[string]any{{"id": "overlay-a"}}})
	u.configureWithOverlay(ctx, embeddedCatalogSource, first)
	select {
	case data := <-published:
		if sectionIDs(t, data, "antigravity")["overlay-a"] != 1 {
			t.Fatal("overlay entry not published")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no publication")
	}
	u.configureWithOverlay(ctx, embeddedCatalogSource, first)
	u.mu.Lock()
	generation := u.generation
	u.mu.Unlock()
	if generation != 1 {
		t.Fatal("unchanged overlay restarted the reader")
	}
	second := writeOverlay(t, map[string]any{"antigravity": []map[string]any{{"id": "overlay-b"}}})
	u.configureWithOverlay(ctx, embeddedCatalogSource, second)
	select {
	case data := <-published:
		ids := sectionIDs(t, data, "antigravity")
		if ids["overlay-b"] != 1 || ids["overlay-a"] != 0 {
			t.Fatalf("overlay change not applied: %v", ids)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("overlay change did not refresh")
	}
}

func TestCatalogOverlayAppliesAgainstFallbackWhenBaseFetchFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published := make(chan []byte, 2)
	u := &catalogUpdater{
		fallbackBase: embeddedModelsJSON,
		fetch:        func(context.Context, string) ([]byte, error) { return nil, errors.New("network unreachable") },
		merge:        mergeCatalogOverlay,
		publish:      func(data []byte) ([]string, error) { published <- data; return nil, nil },
	}
	overlay := writeOverlay(t, map[string]any{"antigravity": []map[string]any{{"id": "overlay-offline"}}})
	u.configureWithOverlay(ctx, "https://unreachable.invalid/models.json", overlay)
	select {
	case data := <-published:
		if sectionIDs(t, data, "antigravity")["overlay-offline"] != 1 {
			t.Fatal("overlay entry not published against fallback base")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("overlay was not published when base fetch failed")
	}
}

func TestCatalogOverlayValidation(t *testing.T) {
	if errValidate := (CatalogSources{CatalogOverlay: "relative/overlay.json"}).Validate(); errValidate == nil {
		t.Fatal("relative overlay path accepted")
	}
	if errValidate := (CatalogSources{CatalogOverlay: "/abs/overlay.json"}).Validate(); errValidate != nil {
		t.Fatal(errValidate)
	}
	got := effectiveCatalogSources(CatalogSources{CatalogOverlay: "/abs/overlay.json"}, true, false)
	if got.CatalogOverlay != "/abs/overlay.json" || got.Catalog != embeddedCatalogSource {
		t.Fatalf("overlay not carried: %+v", got)
	}
}

func TestAntigravityStaticCatalogIncludesClaudeHaiku55Thinking(t *testing.T) {
	info := LookupStaticModelInfoByChannel("claude-haiku-5-5", "antigravity")
	if info == nil {
		t.Fatal("LookupStaticModelInfoByChannel(claude-haiku-5-5, antigravity) returned nil")
	}
	if info.Thinking == nil {
		t.Fatal("claude-haiku-5-5 in antigravity channel is missing Thinking support")
	}
	if info.Thinking.Min != 1024 ||
		info.Thinking.Max != 64000 ||
		!info.Thinking.ZeroAllowed ||
		!info.Thinking.DynamicAllowed ||
		!slices.Equal(info.Thinking.Levels, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("unexpected Thinking config for claude-haiku-5-5: %+v", info.Thinking)
	}
}
