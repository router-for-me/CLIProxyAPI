package registry

import "testing"

func TestValidateModelsCatalogAllowsMissingDeepSeekSection(t *testing.T) {
	data := cloneModelsCatalogForTest(getModels())
	data.DeepSeek = nil

	if err := validateModelsCatalog(data); err != nil {
		t.Fatalf("validateModelsCatalog() error = %v, want nil for missing deepseek section", err)
	}
}

func TestDeepSeekBuiltinsSurviveMissingCatalogSection(t *testing.T) {
	models := WithDeepSeekBuiltins(nil)
	for _, id := range []string{deepSeekV4FlashModelID, deepSeekV4ProModelID, deepSeekChatModelID, deepSeekReasonerModelID} {
		if findModelInfo(models, id) == nil {
			t.Fatalf("expected DeepSeek builtins to include %s", id)
		}
	}
}

func cloneModelsCatalogForTest(in *staticModelsJSON) *staticModelsJSON {
	if in == nil {
		return &staticModelsJSON{}
	}
	return &staticModelsJSON{
		Claude:      cloneModelInfos(in.Claude),
		Gemini:      cloneModelInfos(in.Gemini),
		Vertex:      cloneModelInfos(in.Vertex),
		GeminiCLI:   cloneModelInfos(in.GeminiCLI),
		AIStudio:    cloneModelInfos(in.AIStudio),
		CodexFree:   cloneModelInfos(in.CodexFree),
		CodexTeam:   cloneModelInfos(in.CodexTeam),
		CodexPlus:   cloneModelInfos(in.CodexPlus),
		CodexPro:    cloneModelInfos(in.CodexPro),
		Kimi:        cloneModelInfos(in.Kimi),
		DeepSeek:    cloneModelInfos(in.DeepSeek),
		Antigravity: cloneModelInfos(in.Antigravity),
	}
}
