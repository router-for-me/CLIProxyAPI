package registry

import (
	"slices"
	"testing"

	"github.com/MoeForge/moego/json"
)

var forkMythosModelIDs = []string{"claude-mythos-5-2", "claude-mythos-5-1"}

func restoreModelsCatalog(t *testing.T) {
	t.Helper()
	original := getModels()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})
}

func claudeModelIDs() []string {
	var ids []string
	for _, model := range GetClaudeModels() {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestForkModelsCatalogIsValid(t *testing.T) {
	var fork staticModelsJSON
	if err := json.Unmarshal(embeddedForkModelsJSON, &fork); err != nil {
		t.Fatalf("decode fork_models.json: %v", err)
	}
	if err := validateModelSection("claude", fork.Claude); err != nil {
		t.Fatalf("validate fork claude models: %v", err)
	}
	// Only the claude section is merged; entries elsewhere would be silently ignored.
	fork.Claude = nil
	empty, err := json.Marshal(&staticModelsJSON{})
	if err != nil {
		t.Fatal(err)
	}
	rest, err := json.Marshal(&fork)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != string(empty) {
		t.Fatalf("fork_models.json has non-claude entries, which are not merged: %s", rest)
	}
}

func TestEmbeddedCatalogIncludesForkModels(t *testing.T) {
	restoreModelsCatalog(t)
	if err := loadModelsFromBytes(embeddedModelsJSON, "embed"); err != nil {
		t.Fatal(err)
	}
	ids := claudeModelIDs()
	for _, id := range forkMythosModelIDs {
		if !slices.Contains(ids, id) {
			t.Fatalf("embedded catalog claude models %v missing %s", ids, id)
		}
	}
}

func TestRemoteCatalogRefreshKeepsForkModels(t *testing.T) {
	restoreModelsCatalog(t)
	var upstream staticModelsJSON
	if err := json.Unmarshal(embeddedModelsJSON, &upstream); err != nil {
		t.Fatal(err)
	}
	upstreamCount := len(upstream.Claude)
	if _, err := publishCatalogBytes(embeddedModelsJSON); err != nil {
		t.Fatal(err)
	}
	models := GetClaudeModels()
	if len(models) != upstreamCount+len(forkMythosModelIDs) {
		t.Fatalf("claude model count = %d, want %d upstream + %d fork", len(models), upstreamCount, len(forkMythosModelIDs))
	}
	for _, model := range models {
		if model.ID != "claude-mythos-5-2" {
			continue
		}
		if model.Thinking == nil || model.Thinking.ZeroAllowed || !model.Thinking.DynamicAllowed {
			t.Fatalf("claude-mythos-5-2 thinking = %+v, want adaptive-only", model.Thinking)
		}
		if !slices.Equal(model.Thinking.Levels, []string{"low", "medium", "high", "xhigh", "max"}) {
			t.Fatalf("claude-mythos-5-2 thinking levels = %v", model.Thinking.Levels)
		}
		return
	}
	t.Fatalf("refreshed catalog missing claude-mythos-5-2: %v", claudeModelIDs())
}

func TestForkModelsDoNotReplaceCatalogEntries(t *testing.T) {
	restoreModelsCatalog(t)
	catalog := `{"claude":[{"id":"claude-mythos-5-2","display_name":"Catalog Mythos"}]}`
	if _, err := publishCatalogBytes([]byte(catalog)); err != nil {
		t.Fatal(err)
	}
	models := GetClaudeModels()
	if len(models) != 2 {
		t.Fatalf("claude models = %v, want catalog entry plus claude-mythos-5-1", claudeModelIDs())
	}
	if models[0].ID != "claude-mythos-5-2" || models[0].DisplayName != "Catalog Mythos" {
		t.Fatalf("catalog entry replaced: %+v", models[0])
	}
	if models[1].ID != "claude-mythos-5-1" {
		t.Fatalf("fork entry = %s, want claude-mythos-5-1", models[1].ID)
	}
}
