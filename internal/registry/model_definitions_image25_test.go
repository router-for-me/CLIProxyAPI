package registry

import "testing"

func TestCodexImage25Builtins(t *testing.T) {
	for _, id := range []string{"gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		t.Run(id, func(t *testing.T) {
			// Stale remote metadata must not replace the built-in definition or duplicate it.
			models := WithCodexBuiltins([]*ModelInfo{{ID: id, DisplayName: "stale"}})
			models = WithCodexBuiltins(models)
			count := 0
			for _, model := range models {
				if model.ID == id {
					count++
					if model.DisplayName == "stale" || model.Version != id || model.OwnedBy != "openai" {
						t.Fatalf("unexpected builtin metadata: %+v", model)
					}
				}
			}
			if count != 1 {
				t.Fatalf("got %d entries for %s, want 1", count, id)
			}
			for _, tier := range []struct {
				name   string
				models []*ModelInfo
			}{
				{"team", GetCodexTeamModels()}, {"plus", GetCodexPlusModels()}, {"pro", GetCodexProModels()},
			} {
				found := false
				for _, model := range tier.models {
					if model.ID == id {
						found = true
					}
				}
				if !found {
					t.Errorf("%s missing from %s catalog", id, tier.name)
				}
			}
		})
	}
}
