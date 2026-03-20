package registry

import (
	"strings"
	"testing"
)

func TestCodexTierModelsIncludeSharedTranscriptionModels(t *testing.T) {
	testCases := []struct {
		name   string
		models []*ModelInfo
	}{
		{name: "free", models: GetCodexFreeModels()},
		{name: "team", models: GetCodexTeamModels()},
		{name: "plus", models: GetCodexPlusModels()},
		{name: "pro", models: GetCodexProModels()},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !modelListContainsID(tc.models, "gpt-4o-mini-transcribe") {
				t.Fatalf("%s tier missing gpt-4o-mini-transcribe", tc.name)
			}
			if !modelListContainsID(tc.models, "whisper-1") {
				t.Fatalf("%s tier missing whisper-1", tc.name)
			}
		})
	}
}

func TestLookupStaticModelInfoFindsSharedCodexModels(t *testing.T) {
	for _, modelID := range []string{"gpt-4o-mini-transcribe", "whisper-1"} {
		info := LookupStaticModelInfo(modelID)
		if info == nil {
			t.Fatalf("LookupStaticModelInfo(%q) = nil", modelID)
		}
		if info.ID != modelID {
			t.Fatalf("LookupStaticModelInfo(%q).ID = %q, want %q", modelID, info.ID, modelID)
		}
	}
}

func TestValidateModelsCatalogRejectsCodexSharedOverlap(t *testing.T) {
	data := newStaticModelsCatalogFixture()
	data.CodexShared = []*ModelInfo{testStaticModel("shared-transcribe")}
	data.CodexFree = []*ModelInfo{testStaticModel("shared-transcribe")}

	err := validateModelsCatalog(data)
	if err == nil || !strings.Contains(err.Error(), `codex-shared overlaps codex-free on model id "shared-transcribe"`) {
		t.Fatalf("validateModelsCatalog() error = %v, want overlap error", err)
	}
}

func TestDetectChangedProvidersTreatsCodexSharedAndDuplicatedSchemasAsEquivalent(t *testing.T) {
	oldData := newStaticModelsCatalogFixture()
	oldData.CodexFree = []*ModelInfo{
		testStaticModel("free-only"),
		testStaticModel("gpt-4o-mini-transcribe"),
		testStaticModel("whisper-1"),
	}
	oldData.CodexTeam = []*ModelInfo{
		testStaticModel("team-only"),
		testStaticModel("gpt-4o-mini-transcribe"),
		testStaticModel("whisper-1"),
	}
	oldData.CodexPlus = []*ModelInfo{
		testStaticModel("plus-only"),
		testStaticModel("gpt-4o-mini-transcribe"),
		testStaticModel("whisper-1"),
	}
	oldData.CodexPro = []*ModelInfo{
		testStaticModel("pro-only"),
		testStaticModel("gpt-4o-mini-transcribe"),
		testStaticModel("whisper-1"),
	}

	newData := newStaticModelsCatalogFixture()
	newData.CodexShared = []*ModelInfo{
		testStaticModel("gpt-4o-mini-transcribe"),
		testStaticModel("whisper-1"),
	}
	newData.CodexFree = []*ModelInfo{testStaticModel("free-only")}
	newData.CodexTeam = []*ModelInfo{testStaticModel("team-only")}
	newData.CodexPlus = []*ModelInfo{testStaticModel("plus-only")}
	newData.CodexPro = []*ModelInfo{testStaticModel("pro-only")}

	changed := detectChangedProviders(oldData, newData)
	if stringSliceContains(changed, "codex") {
		t.Fatalf("detectChangedProviders() = %v, want codex unchanged", changed)
	}
}

func newStaticModelsCatalogFixture() *staticModelsJSON {
	return &staticModelsJSON{
		Claude:      []*ModelInfo{testStaticModel("claude-base")},
		Gemini:      []*ModelInfo{testStaticModel("gemini-base")},
		Vertex:      []*ModelInfo{testStaticModel("vertex-base")},
		GeminiCLI:   []*ModelInfo{testStaticModel("gemini-cli-base")},
		AIStudio:    []*ModelInfo{testStaticModel("aistudio-base")},
		CodexFree:   []*ModelInfo{testStaticModel("free-base")},
		CodexTeam:   []*ModelInfo{testStaticModel("team-base")},
		CodexPlus:   []*ModelInfo{testStaticModel("plus-base")},
		CodexPro:    []*ModelInfo{testStaticModel("pro-base")},
		Qwen:        []*ModelInfo{testStaticModel("qwen-base")},
		IFlow:       []*ModelInfo{testStaticModel("iflow-base")},
		Kimi:        []*ModelInfo{testStaticModel("kimi-base")},
		Antigravity: []*ModelInfo{testStaticModel("antigravity-base")},
	}
}

func testStaticModel(id string) *ModelInfo {
	return &ModelInfo{ID: id, Object: "model"}
}

func modelListContainsID(models []*ModelInfo, modelID string) bool {
	for _, model := range models {
		if model != nil && model.ID == modelID {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
