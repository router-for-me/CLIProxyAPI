package registry

import "testing"

func TestDeepSeekStaticModelsIncludeV4(t *testing.T) {
	models := GetDeepSeekModels()
	for _, id := range []string{"deepseek-v4-pro", "deepseek-v4-flash"} {
		model := findModelInfo(models, id)
		if model == nil {
			t.Fatalf("expected DeepSeek models to include %s", id)
		}
		if model.OwnedBy != "deepseek" {
			t.Fatalf("%s owned_by = %q, want deepseek", id, model.OwnedBy)
		}
		if model.Type != "deepseek" {
			t.Fatalf("%s type = %q, want deepseek", id, model.Type)
		}
		if model.ContextLength != 1000000 {
			t.Fatalf("%s context length = %d, want 1000000", id, model.ContextLength)
		}
		if model.MaxCompletionTokens != 384000 {
			t.Fatalf("%s max completion tokens = %d, want 384000", id, model.MaxCompletionTokens)
		}
		if model.Thinking == nil {
			t.Fatalf("%s missing thinking support", id)
		}
		wantLevels := []string{"high", "max"}
		if len(model.Thinking.Levels) != len(wantLevels) {
			t.Fatalf("%s thinking level count = %d, want %d", id, len(model.Thinking.Levels), len(wantLevels))
		}
		for i := range wantLevels {
			if model.Thinking.Levels[i] != wantLevels[i] {
				t.Fatalf("%s thinking level[%d] = %q, want %q", id, i, model.Thinking.Levels[i], wantLevels[i])
			}
		}
	}

	if got := GetStaticModelDefinitionsByChannel("deepseek"); findModelInfo(got, "deepseek-v4-pro") == nil {
		t.Fatal("expected channel lookup to include deepseek-v4-pro")
	}
	if got := LookupStaticModelInfo("deepseek-v4-flash"); got == nil {
		t.Fatal("expected LookupStaticModelInfo to find deepseek-v4-flash")
	}
}
