package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexConfigurationUpdateCapability(t *testing.T) {
	for _, tier := range []struct {
		name    string
		models  func() []*ModelInfo
		capable []string
	}{
		{name: "free", models: GetCodexFreeModels, capable: []string{"gpt-6-luna"}},
		{name: "team", models: GetCodexTeamModels, capable: []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"}},
		{name: "plus", models: GetCodexPlusModels, capable: []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"}},
		{name: "pro", models: GetCodexProModels, capable: []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"}},
	} {
		t.Run(tier.name, func(t *testing.T) {
			byID := make(map[string]*ModelInfo)
			for _, info := range tier.models() {
				if info != nil {
					byID[info.ID] = info
				}
			}
			for _, id := range append([]string{"gpt-5.5"}, tier.capable...) {
				info := byID[id]
				if info == nil {
					t.Errorf("%s is missing", id)
					continue
				}
				want := id != "gpt-5.5"
				if info.SupportConfigurationUpdate != want {
					t.Errorf("%s configuration_update = %v, want %v", id, info.SupportConfigurationUpdate, want)
				}
				if cloned := cloneModelInfo(info); cloned.SupportConfigurationUpdate != want {
					t.Errorf("%s clone configuration_update = %v, want %v", id, cloned.SupportConfigurationUpdate, want)
				}
				raw, errMarshal := json.Marshal(info)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				if strings.Contains(string(raw), "support_configuration_update") || strings.Contains(string(raw), "SupportConfigurationUpdate") {
					t.Errorf("%s exposed configuration_update capability: %s", id, raw)
				}
			}
		})
	}

	for _, tt := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "true", raw: `{"id":"test","support_configuration_update":true}`, want: true},
		{name: "false", raw: `{"id":"test","support_configuration_update":false}`},
		{name: "absent", raw: `{"id":"test"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := ModelInfo{SupportConfigurationUpdate: true}
			if errUnmarshal := json.Unmarshal([]byte(tt.raw), &info); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if info.SupportConfigurationUpdate != tt.want {
				t.Errorf("capability = %v, want %v", info.SupportConfigurationUpdate, tt.want)
			}
		})
	}
}

func TestGetStaticModelDefinitionsByChannelSupportsGeminiInteractions(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("gemini-interactions")
	if len(models) == 0 {
		t.Fatal("GetStaticModelDefinitionsByChannel(gemini-interactions) returned no models")
	}
}

func TestGetStaticModelDefinitionsByChannelSupportsKimiAndKimiAI(t *testing.T) {
	for _, channel := range []string{"kimi", "kimi-ai", "kimi.ai", "kimi.com"} {
		models := GetStaticModelDefinitionsByChannel(channel)
		if len(models) == 0 {
			t.Fatalf("GetStaticModelDefinitionsByChannel(%s) returned no models", channel)
		}
	}
}

func TestModelOverrideHeadersFromEmbeddedModels(t *testing.T) {
	const wantUA = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	got := ModelOverrideHeaders("gpt-5.6-luna")
	if got == nil {
		t.Fatal("ModelOverrideHeaders(gpt-5.6-luna) = nil, want headers")
	}
	if got["user-agent"] != wantUA {
		t.Fatalf("user-agent = %q, want %q", got["user-agent"], wantUA)
	}
	if got := ModelOverrideHeaders("gpt-5.4"); got != nil {
		t.Fatalf("ModelOverrideHeaders(gpt-5.4) = %#v, want nil", got)
	}
}

func TestGeminiVertexModelsUseFlashLiteReleaseID(t *testing.T) {
	const releaseID = "gemini-3.1-flash-lite"
	const previewID = releaseID + "-preview"

	for _, model := range GetGeminiVertexModels() {
		if model == nil {
			continue
		}
		if model.ID == previewID {
			t.Fatalf("Vertex model ID = %q, want release ID %q", model.ID, releaseID)
		}
		if model.ID == releaseID {
			return
		}
	}

	t.Fatalf("Vertex models do not contain %q", releaseID)
}

func TestWithXAIBuiltinsIncludesImage20(t *testing.T) {
	models := WithXAIBuiltins(nil)
	for _, model := range models {
		if model != nil && model.ID == xaiBuiltinImage20ModelID {
			if model.Created != 1786060800 {
				t.Fatalf("created = %d, want 1786060800 (2026-08-07)", model.Created)
			}
			return
		}
	}
	t.Fatalf("expected xAI builtin model %s", xaiBuiltinImage20ModelID)
}

func TestWithXAIBuiltinsIncludesSpeechModels(t *testing.T) {
	models := WithXAIBuiltins(nil)
	found := map[string]*ModelInfo{}
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == xaiBuiltinSpeechModelID || model.ID == xaiBuiltinSpeechVoiceModelID {
			found[model.ID] = model
		}
	}
	for _, id := range []string{xaiBuiltinSpeechModelID, xaiBuiltinSpeechVoiceModelID} {
		model := found[id]
		if model == nil {
			t.Fatalf("expected xAI builtin model %s", id)
		}
		if model.OwnedBy != "xai" || model.Type != "xai" {
			t.Fatalf("%s owned_by/type = %s/%s, want xai/xai", id, model.OwnedBy, model.Type)
		}
	}
}

func TestWithXAIBuiltinsIncludesVideo15GAAndPreviewAlias(t *testing.T) {
	models := WithXAIBuiltins(nil)
	foundGA := false
	foundPreviewAlias := false

	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == xaiBuiltinVideo15ModelID {
			foundGA = true
		}
		if model.ID == xaiBuiltinVideo15PreviewID {
			foundPreviewAlias = true
		}
	}

	if !foundGA {
		t.Fatalf("expected xAI builtin model %s", xaiBuiltinVideo15ModelID)
	}
	if !foundPreviewAlias {
		t.Fatalf("expected xAI builtin compatibility alias %s", xaiBuiltinVideo15PreviewID)
	}
}

func TestAntigravityWebSearchModelForRequiresRequestedModelCapability(t *testing.T) {
	registryRef := GetGlobalRegistry()
	registryRef.RegisterClient("test-antigravity-websearch-route", "antigravity", []*ModelInfo{
		{ID: "gemini-route-test"},
		{ID: "gemini-web-search-test", SupportsWebSearch: true},
	})
	registryRef.RegisterClient("test-gemini-websearch-route", "gemini", []*ModelInfo{
		{ID: "gemini-cross-provider-route"},
		{ID: "gemini-cross-provider-search", SupportsWebSearch: true},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient("test-antigravity-websearch-route")
		registryRef.UnregisterClient("test-gemini-websearch-route")
	})

	if got := AntigravityWebSearchModelFor("gemini-route-test"); got != "" {
		t.Fatalf("route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-route-test(high)"); got != "" {
		t.Fatalf("suffix route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-web-search-test"); got != "gemini-web-search-test" {
		t.Fatalf("AntigravityWebSearchModelFor capable model = %q, want itself", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-cross-provider-route"); got != "" {
		t.Fatalf("cross-provider model should not get Antigravity web search model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("unknown-model"); got != "" {
		t.Fatalf("unknown model should not get Antigravity web search model, got %q", got)
	}
}

func TestValidateModelsCatalog_Meta(t *testing.T) {
	valid := &staticModelsJSON{
		Meta: []*ModelInfo{
			{ID: "muse-spark-1.3"},
		},
	}
	if err := validateModelsCatalog(valid); err != nil {
		t.Fatalf("expected valid Meta catalog to pass, got: %v", err)
	}

	withNull := &staticModelsJSON{
		Meta: []*ModelInfo{nil},
	}
	if err := validateModelsCatalog(withNull); err == nil {
		t.Fatal("expected error for Meta section with null model, got nil")
	}

	withEmptyID := &staticModelsJSON{
		Meta: []*ModelInfo{{ID: " "}},
	}
	if err := validateModelsCatalog(withEmptyID); err == nil {
		t.Fatal("expected error for Meta section with empty model id, got nil")
	}

	withDuplicate := &staticModelsJSON{
		Meta: []*ModelInfo{
			{ID: "muse-spark-1.3"},
			{ID: "muse-spark-1.3"},
		},
	}
	if err := validateModelsCatalog(withDuplicate); err == nil {
		t.Fatal("expected error for Meta section with duplicate model id, got nil")
	}
}

func TestWithCodexBuiltinsIncludesImage25Models(t *testing.T) {
	models := WithCodexBuiltins(nil)
	expectedModels := map[string]string{
		"gpt-image-2.5-flare":    "GPT Image 2.5 Flare",
		"gpt-image-2.5-sunburst": "GPT Image 2.5 Sunburst",
		"gpt-image-2.5":          "GPT Image 2.5",
	}

	found := make(map[string]*ModelInfo)
	for _, model := range models {
		if model != nil {
			if _, ok := expectedModels[model.ID]; ok {
				found[model.ID] = model
			}
		}
	}

	for id, wantDisplayName := range expectedModels {
		model, ok := found[id]
		if !ok {
			t.Fatalf("expected builtin model %s in WithCodexBuiltins", id)
		}
		if model.DisplayName != wantDisplayName {
			t.Errorf("model %s DisplayName = %q, want %q", id, model.DisplayName, wantDisplayName)
		}
		if model.Object != "model" {
			t.Errorf("model %s Object = %q, want model", id, model.Object)
		}
		if model.OwnedBy != "openai" {
			t.Errorf("model %s OwnedBy = %q, want openai", id, model.OwnedBy)
		}
		if model.Type != "openai" {
			t.Errorf("model %s Type = %q, want openai", id, model.Type)
		}
		if model.Version != id {
			t.Errorf("model %s Version = %q, want %q", id, model.Version, id)
		}
		if model.Created != 1704067200 {
			t.Errorf("model %s Created = %d, want 1704067200", id, model.Created)
		}
	}
}

// Gemini 4 Argon must populate both OpenAI-compatible (ContextLength /
// MaxCompletionTokens) and Gemini-native (InputTokenLimit / OutputTokenLimit)
// limits so /v1/models and /v1beta/models both report its token bounds.
func TestWithGeminiBuiltinsIncludesArgon(t *testing.T) {
	models := WithGeminiBuiltins(nil)
	var argon *ModelInfo
	for _, model := range models {
		if model != nil && model.ID == geminiBuiltinArgonModelID {
			argon = model
			break
		}
	}
	if argon == nil {
		t.Fatalf("expected builtin model %s in WithGeminiBuiltins", geminiBuiltinArgonModelID)
	}
	if argon.Object != "model" || argon.OwnedBy != "google" || argon.Type != "gemini" {
		t.Errorf("argon object/owned_by/type = %s/%s/%s, want model/google/gemini", argon.Object, argon.OwnedBy, argon.Type)
	}
	if argon.Name != "models/"+geminiBuiltinArgonModelID {
		t.Errorf("argon name = %q, want models/%s", argon.Name, geminiBuiltinArgonModelID)
	}
	if argon.InputTokenLimit != 1048576 || argon.OutputTokenLimit != 65536 {
		t.Errorf("argon Gemini token limits = %d/%d, want 1048576/65536", argon.InputTokenLimit, argon.OutputTokenLimit)
	}
	if argon.ContextLength != 1048576 || argon.MaxCompletionTokens != 65536 {
		t.Errorf("argon OpenAI token limits = %d/%d, want 1048576/65536", argon.ContextLength, argon.MaxCompletionTokens)
	}
	reg := GetGlobalRegistry()
	openaiView := reg.convertModelToMap(argon, "openai")
	if openaiView["context_length"] != 1048576 || openaiView["max_completion_tokens"] != 65536 {
		t.Errorf("convertModelToMap(openai) = %+v, want context_length=1048576 max_completion_tokens=65536", openaiView)
	}
	geminiView := reg.convertModelToMap(argon, "gemini")
	if geminiView["inputTokenLimit"] != 1048576 || geminiView["outputTokenLimit"] != 65536 {
		t.Errorf("convertModelToMap(gemini) = %+v, want inputTokenLimit=1048576 outputTokenLimit=65536", geminiView)
	}
	if argon.Thinking == nil {
		t.Fatal("argon thinking support is nil")
	}
	wantLevels := []string{"low", "medium", "high"}
	if len(argon.Thinking.Levels) != len(wantLevels) {
		t.Fatalf("argon thinking levels = %v, want %v", argon.Thinking.Levels, wantLevels)
	}
	for i, level := range wantLevels {
		if argon.Thinking.Levels[i] != level {
			t.Fatalf("argon thinking levels = %v, want %v", argon.Thinking.Levels, wantLevels)
		}
	}
	if argon.NativeCapabilities == nil || argon.NativeCapabilities.WebSearch == nil || !*argon.NativeCapabilities.WebSearch {
		t.Error("argon should advertise native web search")
	}
}

// The built-in must replace, not duplicate, a catalog entry with the same ID so
// the model list never reports Argon twice.
func TestWithGeminiBuiltinsReplacesCatalogEntry(t *testing.T) {
	catalog := []*ModelInfo{
		{ID: geminiBuiltinArgonModelID, Object: "model", OwnedBy: "google", Type: "gemini", DisplayName: "Stale Argon"},
		{ID: "gemini-3.8-flash", Object: "model", OwnedBy: "google", Type: "gemini"},
	}
	models := WithGeminiBuiltins(catalog)
	count := 0
	for _, model := range models {
		if model != nil && model.ID == geminiBuiltinArgonModelID {
			count++
			if model.DisplayName != "Gemini 4 Argon" {
				t.Errorf("argon DisplayName = %q, want the built-in definition", model.DisplayName)
			}
			if model.ContextLength != 1048576 || model.MaxCompletionTokens != 65536 {
				t.Errorf("argon OpenAI limits after upsert = %d/%d, want 1048576/65536", model.ContextLength, model.MaxCompletionTokens)
			}
		}
	}
	if count != 1 {
		t.Fatalf("argon appears %d times after upsert, want 1", count)
	}
	foundOther := false
	for _, model := range models {
		if model != nil && model.ID == "gemini-3.8-flash" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Error("upsert dropped an unrelated catalog model")
	}
}

// GetGeminiModels is what the Gemini API-key provider registers, so the model
// must be present there without any explicit per-key configuration.
func TestGetGeminiModelsIncludesArgon(t *testing.T) {
	for _, model := range GetGeminiModels() {
		if model != nil && model.ID == geminiBuiltinArgonModelID {
			if model.ContextLength != 1048576 || model.MaxCompletionTokens != 65536 {
				t.Fatalf("GetGeminiModels() argon OpenAI limits = %d/%d, want 1048576/65536", model.ContextLength, model.MaxCompletionTokens)
			}
			if model.InputTokenLimit != 1048576 || model.OutputTokenLimit != 65536 {
				t.Fatalf("GetGeminiModels() argon Gemini limits = %d/%d, want 1048576/65536", model.InputTokenLimit, model.OutputTokenLimit)
			}
			return
		}
	}
	t.Fatalf("GetGeminiModels() does not include %s", geminiBuiltinArgonModelID)
}

func TestGetDevinModelsFallback(t *testing.T) {
	devinModels := GetDevinModels()
	if len(devinModels) == 0 {
		t.Fatal("GetDevinModels() returned empty list")
	}

	foundSWE2 := false
	foundFable := false
	foundGemini38 := false
	foundGrok46 := false
	foundDeepSeekV4Flash := false
	foundDeepSeekV41Flash := false
	for _, m := range devinModels {
		if m != nil && m.ID == "devin/swe-2" {
			foundSWE2 = true
			if m.Type != "devin" {
				t.Errorf("devin/swe-2 Type = %q, want devin", m.Type)
			}
		}
		if m != nil && m.ID == "devin/claude-fable-5-1" {
			foundFable = true
		}
		if m != nil && m.ID == "devin/gemini-3-8-flash" {
			foundGemini38 = true
			if m.OwnedBy != "google" {
				t.Errorf("devin/gemini-3-8-flash OwnedBy = %q, want google", m.OwnedBy)
			}
		}
		if m != nil && m.ID == "devin/grok-4-6" {
			foundGrok46 = true
			if m.OwnedBy != "xai" {
				t.Errorf("devin/grok-4-6 OwnedBy = %q, want xai", m.OwnedBy)
			}
		}
		if m != nil && m.ID == "devin/deepseek-v4-flash" {
			foundDeepSeekV4Flash = true
			if m.OwnedBy != "deepseek" {
				t.Errorf("devin/deepseek-v4-flash OwnedBy = %q, want deepseek", m.OwnedBy)
			}
		}
		if m != nil && m.ID == "devin/deepseek-v4-1-flash" {
			foundDeepSeekV41Flash = true
			if m.OwnedBy != "deepseek" {
				t.Errorf("devin/deepseek-v4-1-flash OwnedBy = %q, want deepseek", m.OwnedBy)
			}
		}
	}
	if !foundSWE2 {
		t.Error("expected devin/swe-2 in GetDevinModels()")
	}
	if !foundFable {
		t.Error("expected devin/claude-fable-5-1 in GetDevinModels()")
	}
	if !foundGemini38 {
		t.Error("expected devin/gemini-3-8-flash in GetDevinModels()")
	}
	if !foundGrok46 {
		t.Error("expected devin/grok-4-6 in GetDevinModels()")
	}
	if !foundDeepSeekV4Flash {
		t.Error("expected devin/deepseek-v4-flash in GetDevinModels()")
	}
	if !foundDeepSeekV41Flash {
		t.Error("expected devin/deepseek-v4-1-flash in GetDevinModels()")
	}

	byChannel := GetStaticModelDefinitionsByChannel("devin")
	if len(byChannel) == 0 {
		t.Fatal("GetStaticModelDefinitionsByChannel(\"devin\") returned empty list")
	}

	info := LookupStaticModelInfo("devin/swe-2")
	if info == nil {
		t.Fatal("LookupStaticModelInfo(\"devin/swe-2\") = nil, want valid model")
	}
	if info.DisplayName != "SWE-2" {
		t.Errorf("info.DisplayName = %q, want SWE-2", info.DisplayName)
	}
}
