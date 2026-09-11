package models

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCodexClientModelsResponse_InputModalitiesFromRegistry(t *testing.T) {
	modelID := "mimo-v2.5-pro-codex-test"
	textOnlyModelID := "mimo-text-only-codex-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("codex-input-modalities-test", "openai-compatibility", []*registry.ModelInfo{
		{
			ID:                       modelID,
			Object:                   "model",
			OwnedBy:                  "mimo",
			Type:                     "openai-compatibility",
			DisplayName:              modelID,
			SupportedInputModalities: []string{"text", "image"},
		},
		{
			ID:                       textOnlyModelID,
			Object:                   "model",
			OwnedBy:                  "mimo",
			Type:                     "openai-compatibility",
			DisplayName:              textOnlyModelID,
			SupportedInputModalities: []string{"text"},
		},
		{
			ID:                       "mimo-mixed-modalities-codex-test",
			Object:                   "model",
			OwnedBy:                  "mimo",
			Type:                     "openai-compatibility",
			DisplayName:              "mimo-mixed-modalities-codex-test",
			SupportedInputModalities: []string{"text", "image", "audio", "video", "TEXT", "IMAGE"},
		},
		{
			ID:      "compat-image-only-codex-test",
			Object:  "model",
			OwnedBy: "mimo",
			Type:    registry.OpenAIImageModelType,
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("codex-input-modalities-test")
	})

	openaiModels := modelRegistry.GetAvailableModels("openai")
	resp := BuildResponse(openaiModels, nil, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	var visionEntry map[string]any
	var textOnlyEntry map[string]any
	var mixedEntry map[string]any
	var imageEntry map[string]any
	for _, entry := range models {
		slug := stringModelValue(entry, "slug")
		switch slug {
		case modelID:
			visionEntry = entry
		case textOnlyModelID:
			textOnlyEntry = entry
		case "mimo-mixed-modalities-codex-test":
			mixedEntry = entry
		case "compat-image-only-codex-test":
			imageEntry = entry
		}
	}
	if visionEntry == nil {
		t.Fatalf("expected codex entry for %q", modelID)
	}
	modalities, ok := visionEntry["input_modalities"].([]any)
	if !ok || len(modalities) != 2 {
		t.Fatalf("input_modalities = %#v, want [text image]", visionEntry["input_modalities"])
	}
	if got, _ := modalities[0].(string); got != "text" {
		t.Fatalf("input_modalities[0] = %q, want text", got)
	}
	if got, _ := modalities[1].(string); got != "image" {
		t.Fatalf("input_modalities[1] = %q, want image", got)
	}
	if got, ok := visionEntry["supports_image_detail_original"].(bool); !ok || !got {
		t.Fatalf("supports_image_detail_original = %#v, want true", visionEntry["supports_image_detail_original"])
	}

	if textOnlyEntry == nil {
		t.Fatalf("expected codex entry for %q", textOnlyModelID)
	}
	textOnlyModalities, ok := textOnlyEntry["input_modalities"].([]any)
	if !ok || len(textOnlyModalities) != 1 {
		t.Fatalf("text-only input_modalities = %#v, want [text]", textOnlyEntry["input_modalities"])
	}
	if got, _ := textOnlyModalities[0].(string); got != "text" {
		t.Fatalf("text-only input_modalities[0] = %q, want text", got)
	}
	if _, exists := textOnlyEntry["supports_image_detail_original"]; exists {
		t.Fatalf("text-only model should not expose supports_image_detail_original: %#v", textOnlyEntry["supports_image_detail_original"])
	}

	if mixedEntry == nil {
		t.Fatal("expected codex entry for mixed-modalities model")
	}
	mixedModalities, ok := mixedEntry["input_modalities"].([]any)
	if !ok || len(mixedModalities) != 2 {
		t.Fatalf("mixed input_modalities = %#v, want [text image]", mixedEntry["input_modalities"])
	}
	if got, _ := mixedModalities[0].(string); got != "text" {
		t.Fatalf("mixed input_modalities[0] = %q, want text", got)
	}
	if got, _ := mixedModalities[1].(string); got != "image" {
		t.Fatalf("mixed input_modalities[1] = %q, want image", got)
	}
	if got, ok := mixedEntry["supports_image_detail_original"].(bool); !ok || !got {
		t.Fatalf("mixed supports_image_detail_original = %#v, want true", mixedEntry["supports_image_detail_original"])
	}

	if imageEntry == nil {
		t.Fatal("expected codex entry for image-only compat model")
	}
	if got, _ := imageEntry["visibility"].(string); got != "hide" {
		t.Fatalf("image model visibility = %q, want hide", got)
	}
	if _, exists := imageEntry["input_modalities"]; exists {
		t.Fatalf("image endpoint model should not expose input_modalities from registry: %#v", imageEntry["input_modalities"])
	}
}

func TestCodexClientModelsResponse_AppliesDisplayNameToTemplateModel(t *testing.T) {
	resp := BuildResponse([]map[string]any{{
		"id":           "gpt-5.5",
		"display_name": "Configured Codex Name",
	}}, nil, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}
	if got := stringModelValue(models[0], "display_name"); got != "Configured Codex Name" {
		t.Fatalf("display_name = %q, want Configured Codex Name", got)
	}
}

func TestCodexClientModelsResponse_RewritesTemplateMultiAgentVersionWhenEnabled(t *testing.T) {
	modelIDs := []string{"gpt-5.6-luna", "gpt-5.5"}
	resp := BuildResponse([]map[string]any{{"id": modelIDs[0]}, {"id": modelIDs[1]}}, nil, true)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	for _, model := range models {
		if got := stringModelValue(model, "multi_agent_version"); got != "v2" {
			t.Errorf("%s multi_agent_version = %q, want v2", stringModelValue(model, "slug"), got)
		}
	}
}

func TestCodexClientModelsResponse_DisablesSearchToolForSynthesizedModels(t *testing.T) {
	resp := BuildResponse([]map[string]any{
		{"id": "custom-openai-compatible-model"},
		{"id": "gpt-5.5"},
	}, nil, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		bySlug[stringModelValue(model, "slug")] = model
	}

	custom := bySlug["custom-openai-compatible-model"]
	if custom == nil {
		t.Fatal("expected synthesized custom model entry")
	}
	if got, ok := custom["supports_search_tool"].(bool); !ok || got {
		t.Fatalf("custom supports_search_tool = %#v, want false", custom["supports_search_tool"])
	}

	official := bySlug["gpt-5.5"]
	if official == nil {
		t.Fatal("expected official template model entry")
	}
	if got, ok := official["supports_search_tool"].(bool); !ok || !got {
		t.Fatalf("official supports_search_tool = %#v, want true", official["supports_search_tool"])
	}
}

func TestCodexClientModelsResponse_RequiresTemplateAndCodexProvidersForSearchTool(t *testing.T) {
	providers := map[string][]string{
		"new-codex-model": {"codex"},
		"gpt-5.5":         {"openai-compatible-deepseek"},
		"gpt-5.4":         {"codex", "xai"},
		"gpt-5.6-sol":     {"codex"},
	}
	resp := BuildResponse([]map[string]any{
		{"id": "new-codex-model"},
		{"id": "gpt-5.5"},
		{"id": "gpt-5.4"},
		{"id": "gpt-5.6-sol"},
	}, func(id string) []string {
		return providers[id]
	}, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		bySlug[stringModelValue(model, "slug")] = model
	}

	if got, ok := bySlug["gpt-5.6-sol"]["supports_search_tool"].(bool); !ok || !got {
		t.Errorf("gpt-5.6-sol supports_search_tool = %#v, want true", bySlug["gpt-5.6-sol"]["supports_search_tool"])
	}
	for _, slug := range []string{"new-codex-model", "gpt-5.5", "gpt-5.4"} {
		if got, ok := bySlug[slug]["supports_search_tool"].(bool); !ok || got {
			t.Errorf("%s supports_search_tool = %#v, want false", slug, bySlug[slug]["supports_search_tool"])
		}
	}
}

func TestCodexClientModelsResponse_PreservesUltraReasoningEffort(t *testing.T) {
	resp := BuildResponse([]map[string]any{{"id": "gpt-5.6-sol"}}, nil, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	var sol map[string]any
	for _, entry := range models {
		if stringModelValue(entry, "slug") == "gpt-5.6-sol" {
			sol = entry
			break
		}
	}
	if sol == nil {
		t.Fatal("expected codex client entry for gpt-5.6-sol")
	}

	levels, ok := sol["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("supported_reasoning_levels = %T, want []any", sol["supported_reasoning_levels"])
	}
	for _, rawLevel := range levels {
		level, ok := rawLevel.(map[string]any)
		if ok && stringModelValue(level, "effort") == "ultra" {
			return
		}
	}

	t.Fatalf("supported_reasoning_levels = %#v, want ultra", levels)
}

func TestCodexClientModelsResponse_FiltersMaxAndUltraForOlderClients(t *testing.T) {
	resp := BuildResponseForClient([]map[string]any{{"id": "gpt-5.6-sol"}}, nil, false, "0.137.0")
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	var sol map[string]any
	for _, entry := range models {
		if stringModelValue(entry, "slug") == "gpt-5.6-sol" {
			sol = entry
			break
		}
	}
	if sol == nil {
		t.Fatal("expected codex client entry for gpt-5.6-sol")
	}

	levels, ok := sol["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("supported_reasoning_levels = %T, want []any", sol["supported_reasoning_levels"])
	}
	for _, rawLevel := range levels {
		level, ok := rawLevel.(map[string]any)
		if !ok {
			continue
		}
		effort := stringModelValue(level, "effort")
		if effort == "max" || effort == "ultra" {
			t.Fatalf("supported_reasoning_levels contains %q for older client 0.137.0: %#v", effort, levels)
		}
	}
}

func TestSupportsExtendedReasoningLevels(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", true},
		{"pi", true},
		{"latest", true},
		{"unknown", true},
		{"0.137.0", false},
		{"0.143.9", false},
		{"v0.137.0", false},
		{"0.137.0-beta.1", false},
		{"0.144.0", true},
		{"0.144.1", true},
		{"0.149.1", true},
		{"1.0.0", true},
		{"invalid", true},
	}
	for _, tt := range tests {
		if got := supportsExtendedReasoningLevels(tt.version); got != tt.want {
			t.Errorf("supportsExtendedReasoningLevels(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestLoadCodexClientModelTemplatesRefreshesOnRevision(t *testing.T) {
	codexClientModelTemplatesMu.Lock()
	previousLoaded := codexClientModelTemplatesLoaded
	previousRevision := codexClientModelTemplatesRevision
	previousTemplates := codexClientModelTemplates
	previousDefault := codexClientDefaultTemplate
	previousErr := codexClientModelTemplatesErr
	codexClientModelTemplatesLoaded = false
	codexClientModelTemplatesMu.Unlock()
	t.Cleanup(func() {
		codexClientModelTemplatesMu.Lock()
		codexClientModelTemplatesLoaded = previousLoaded
		codexClientModelTemplatesRevision = previousRevision
		codexClientModelTemplates = previousTemplates
		codexClientDefaultTemplate = previousDefault
		codexClientModelTemplatesErr = previousErr
		codexClientModelTemplatesMu.Unlock()
	})

	first := []byte(`{"models":[{"slug":"gpt-5.5","display_name":"First"}]}`)
	templates, defaultTemplate, err := loadCodexClientModelTemplatesSnapshot(first, 100)
	if err != nil {
		t.Fatalf("load first snapshot: %v", err)
	}
	if got := stringModelValue(templates["gpt-5.5"], "display_name"); got != "First" {
		t.Fatalf("first display_name = %q, want First", got)
	}
	if got := stringModelValue(defaultTemplate, "display_name"); got != "First" {
		t.Fatalf("first default display_name = %q, want First", got)
	}

	second := []byte(`{"models":[{"slug":"gpt-5.5","display_name":"Second"}]}`)
	templates, defaultTemplate, err = loadCodexClientModelTemplatesSnapshot(second, 101)
	if err != nil {
		t.Fatalf("load second snapshot: %v", err)
	}
	if got := stringModelValue(templates["gpt-5.5"], "display_name"); got != "Second" {
		t.Fatalf("second display_name = %q, want Second", got)
	}
	if got := stringModelValue(defaultTemplate, "display_name"); got != "Second" {
		t.Fatalf("second default display_name = %q, want Second", got)
	}

	templates, _, err = loadCodexClientModelTemplatesSnapshot(first, 101)
	if err != nil {
		t.Fatalf("reload cached revision: %v", err)
	}
	if got := stringModelValue(templates["gpt-5.5"], "display_name"); got != "Second" {
		t.Fatalf("cached display_name = %q, want Second", got)
	}
}

func TestApplyCodexClientModelMetadataPreservesMultiAgentVersionWhenDisabled(t *testing.T) {
	entry := map[string]any{"multi_agent_version": "v1"}
	model := map[string]any{"id": "custom-model"}

	applyCodexClientModelMetadata(entry, "custom-model", model, false, "")
	if got := entry["multi_agent_version"]; got != "v1" {
		t.Fatalf("disabled multi_agent_version = %#v, want preserved v1", got)
	}

	applyCodexClientModelMetadata(entry, "custom-model", model, true, "")
	if got := entry["multi_agent_version"]; got != "v2" {
		t.Fatalf("enabled multi_agent_version = %#v, want v2", got)
	}
}

func TestCodexClientModelsResponseAppliesMaxContextLengthOverride(t *testing.T) {
	const wantOverride = 1048576
	const wantDefault = 272000

	resp := BuildResponse([]map[string]any{
		{"id": "deepseek-v4-flash", "max_context_length": wantOverride},
		{"id": "deepseek-v4-pro"},
		{"id": "gpt-5.5", "max_context_length": wantOverride},
	}, nil, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		bySlug[stringModelValue(model, "slug")] = model
	}

	for _, testCase := range []struct {
		slug string
		want int
	}{
		{slug: "deepseek-v4-flash", want: wantOverride},
		{slug: "deepseek-v4-pro", want: wantDefault},
		{slug: "gpt-5.5", want: wantOverride},
	} {
		entry := bySlug[testCase.slug]
		if entry == nil {
			t.Fatalf("missing model %q", testCase.slug)
		}
		if got := intModelValue(entry, "context_window"); got != testCase.want {
			t.Errorf("%s context_window = %d, want %d", testCase.slug, got, testCase.want)
		}
		if got := intModelValue(entry, "max_context_window"); got != testCase.want {
			t.Errorf("%s max_context_window = %d, want %d", testCase.slug, got, testCase.want)
		}
	}
}

func TestCodexClientModelsResponseMapsMaxCompletionTokensToMaxTokens(t *testing.T) {
	const wantTemplateLimit = 64000
	const wantSynthesizedLimit = 32000

	resp := BuildResponse([]map[string]any{
		{"id": "gpt-5.5", "max_completion_tokens": wantTemplateLimit},
		{"id": "custom-output-limit-model", "max_completion_tokens": wantSynthesizedLimit},
	}, nil, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		bySlug[stringModelValue(model, "slug")] = model
	}

	for _, testCase := range []struct {
		slug string
		want int
	}{
		{slug: "gpt-5.5", want: wantTemplateLimit},
		{slug: "custom-output-limit-model", want: wantSynthesizedLimit},
	} {
		entry := bySlug[testCase.slug]
		if entry == nil {
			t.Fatalf("missing model %q", testCase.slug)
		}
		if got := intModelValue(entry, "max_tokens"); got != testCase.want {
			t.Errorf("%s max_tokens = %d, want %d", testCase.slug, got, testCase.want)
		}
	}
}

func TestCodexClientModelsResponseUsesProvidedCapabilitiesForNewHomeModel(t *testing.T) {
	const modelID = "gemini-new-home-model-test"
	const wantContextWindow = 1048576

	resp := BuildResponse([]map[string]any{{
		"id":             modelID,
		"context_length": wantContextWindow,
		"thinking": &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}}, nil, false)
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}
	model := models[0]
	if got := intModelValue(model, "context_window"); got != wantContextWindow {
		t.Fatalf("context_window = %d, want %d", got, wantContextWindow)
	}
	if got := intModelValue(model, "max_context_window"); got != wantContextWindow {
		t.Fatalf("max_context_window = %d, want %d", got, wantContextWindow)
	}

	rawLevels, ok := model["supported_reasoning_levels"].([]any)
	if !ok || len(rawLevels) != 3 {
		t.Fatalf("supported_reasoning_levels = %#v, want low/medium/high", model["supported_reasoning_levels"])
	}
	for index, want := range []string{"low", "medium", "high"} {
		level, ok := rawLevels[index].(map[string]any)
		if !ok || stringModelValue(level, "effort") != want {
			t.Fatalf("supported_reasoning_levels[%d] = %#v, want %q", index, rawLevels[index], want)
		}
	}
}

func TestCodexClientModelsResponseDoesNotInheritUnsupportedReasoningLevels(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		thinking    registry.ThinkingSupport
		wantEfforts []string
		wantDefault string
	}{
		{name: "modern client", version: "0.144.0", thinking: registry.ThinkingSupport{Levels: []string{"max", "ultra"}}, wantEfforts: []string{"max", "ultra"}, wantDefault: "max"},
		{name: "legacy client with no compatible level", version: "0.143.9", thinking: registry.ThinkingSupport{Levels: []string{"max", "ultra"}}},
		{name: "legacy client with one compatible level", version: "0.143.9", thinking: registry.ThinkingSupport{Levels: []string{"high", "max"}}, wantEfforts: []string{"high"}, wantDefault: "high"},
		{
			name:    "budget-only model",
			version: "0.153.3",
			thinking: registry.ThinkingSupport{
				Min:            1024,
				Max:            64000,
				ZeroAllowed:    true,
				DynamicAllowed: true,
			},
		},
		{name: "empty levels", version: "0.153.3", thinking: registry.ThinkingSupport{Levels: []string{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := BuildResponseForClient([]map[string]any{{
				"id":       "home-extended-reasoning-model-test",
				"thinking": &tt.thinking,
			}}, nil, false, tt.version)
			models, ok := resp["models"].([]map[string]any)
			if !ok || len(models) != 1 {
				t.Fatalf("models = %#v, want one model", resp["models"])
			}
			model := models[0]
			if len(tt.wantEfforts) == 0 {
				encodedLevels, errMarshal := json.Marshal(model["supported_reasoning_levels"])
				if errMarshal != nil {
					t.Fatalf("marshal supported_reasoning_levels: %v", errMarshal)
				}
				if string(encodedLevels) != "[]" {
					t.Fatalf("supported_reasoning_levels JSON = %s, want []", encodedLevels)
				}
				if _, exists := model["default_reasoning_level"]; exists {
					t.Fatalf("default_reasoning_level = %#v, want absent", model["default_reasoning_level"])
				}
				return
			}

			rawLevels, ok := model["supported_reasoning_levels"].([]any)
			if !ok || len(rawLevels) != len(tt.wantEfforts) {
				t.Fatalf("supported_reasoning_levels = %#v, want %v", model["supported_reasoning_levels"], tt.wantEfforts)
			}
			for index, want := range tt.wantEfforts {
				level, ok := rawLevels[index].(map[string]any)
				if !ok || stringModelValue(level, "effort") != want {
					t.Fatalf("supported_reasoning_levels[%d] = %#v, want %q", index, rawLevels[index], want)
				}
			}
			if got := stringModelValue(model, "default_reasoning_level"); got != tt.wantDefault {
				t.Fatalf("default_reasoning_level = %q, want %q", got, tt.wantDefault)
			}
		})
	}
}

func TestSanitizeCodexClientReasoningMetadataPreservesEmptyArray(t *testing.T) {
	tests := []struct {
		name    string
		version string
		levels  []any
	}{
		{name: "empty levels", version: "0.153.3", levels: []any{}},
		{name: "nil levels", version: "0.153.3"},
		{name: "legacy client with no compatible level", version: "0.143.9", levels: []any{map[string]any{"effort": "max"}, map[string]any{"effort": "ultra"}}},
		{name: "invalid levels", version: "0.153.3", levels: []any{nil, map[string]any{"effort": "unknown"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := map[string]any{
				"supported_reasoning_levels": tt.levels,
				"default_reasoning_level":    "max",
			}
			sanitizeCodexClientReasoningMetadata(entry, tt.version)

			encodedEntry, errMarshal := json.Marshal(entry)
			if errMarshal != nil {
				t.Fatalf("marshal model metadata: %v", errMarshal)
			}
			if got, want := string(encodedEntry), `{"supported_reasoning_levels":[]}`; got != want {
				t.Fatalf("model metadata JSON = %s, want %s", got, want)
			}
		})
	}
}

func TestCodexClientModelsResponse_PrefixedRouteInheritsCanonicalTemplate(t *testing.T) {
	resp := BuildResponseForClient([]map[string]any{{
		"id": "1/gpt-6-astra",
	}}, nil, false, "")
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}
	model := models[0]
	if got := stringModelValue(model, "slug"); got != "1/gpt-6-astra" {
		t.Fatalf("slug = %q, want 1/gpt-6-astra", got)
	}
	if got := stringModelValue(model, "comp_hash"); got != "3000" {
		t.Fatalf("comp_hash = %q, want 3000", got)
	}
	if got := intModelValue(model, "max_context_window"); got != 872000 {
		t.Fatalf("max_context_window = %d, want 872000", got)
	}
}

func TestCodexClientModelsResponse_OAuthAliasesInheritCanonicalMetadata(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "codex-oauth-alias-metadata-test-client"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{
			ID:              "codex-main",
			MetadataModelID: "gpt-6-astra",
			DisplayName:     "GPT 6.0 Astra",
		},
		{
			ID:              "codex-compact",
			MetadataModelID: "gpt-6-astra",
			DisplayName:     "GPT 6.0 Astra",
		},
		{
			ID:              "codex-luna",
			MetadataModelID: "gpt-5.6-luna",
			DisplayName:     "GPT 5.6 Luna",
		},
		{
			ID:              "1/codex-main",
			MetadataModelID: "gpt-6-astra",
			DisplayName:     "Prefixed Astra Alias",
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	availableModels := []map[string]any{
		{"id": "codex-main", "display_name": "GPT 6.0 Astra"},
		{"id": "codex-compact", "display_name": "GPT 6.0 Astra"},
		{"id": "codex-luna", "display_name": "GPT 5.6 Luna"},
		{"id": "1/codex-main", "display_name": "Prefixed Astra Alias"},
	}

	resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
	models, ok := resp["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", resp["models"])
	}

	entries := make(map[string]map[string]any, len(models))
	for _, entry := range models {
		entries[stringModelValue(entry, "slug")] = entry
	}

	for _, slug := range []string{"codex-main", "codex-compact", "1/codex-main"} {
		entry := entries[slug]
		if entry == nil {
			t.Fatalf("missing entry for %q", slug)
		}
		if got := stringModelValue(entry, "comp_hash"); got != "3000" {
			t.Errorf("%s comp_hash = %q, want 3000", slug, got)
		}
		if got := intModelValue(entry, "max_context_window"); got != 872000 {
			t.Errorf("%s max_context_window = %d, want 872000", slug, got)
		}
		if got, _ := entry["supports_search_tool"].(bool); !got {
			t.Errorf("%s supports_search_tool = %v, want true", slug, got)
		}
	}

	lunaEntry := entries["codex-luna"]
	if lunaEntry == nil {
		t.Fatal("missing entry for codex-luna")
	}
	if got := stringModelValue(lunaEntry, "comp_hash"); got != "3000" {
		t.Errorf("codex-luna comp_hash = %q, want 3000", got)
	}
	if got := intModelValue(lunaEntry, "max_context_window"); got != 872000 {
		t.Errorf("codex-luna max_context_window = %d, want 872000", got)
	}
	if got, _ := lunaEntry["supports_search_tool"].(bool); !got {
		t.Errorf("codex-luna supports_search_tool = %v, want true", got)
	}
}

func TestCodexClientModelsResponse_ExplicitRouteOverridesHonored(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "codex-route-override-test-client"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{
			ID:               "custom-sol",
			MetadataModelID:  "gpt-5.6-sol",
			DisplayName:      "Overridden Display Name",
			MaxContextLength: 123456,
			ExplicitThinking: true,
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "high"},
			},
		},
		{
			ID:               "custom-astra-explicit-no-ultra",
			MetadataModelID:  "gpt-6-astra",
			DisplayName:      "Astra Without Ultra",
			ExplicitThinking: true,
			Thinking: &registry.ThinkingSupport{
				// Levels equal registry default (low..max) but explicitly defined by user
				Levels: []string{"low", "medium", "high", "xhigh", "max"},
			},
		},
		{
			ID:               "gpt-6-astra",
			DisplayName:      "Direct Canonical With Explicit Thinking",
			ExplicitThinking: true,
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "high"},
			},
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	availableModels := []map[string]any{
		{
			"id":                 "custom-sol",
			"display_name":       "Overridden Display Name",
			"max_context_length": 123456,
		},
		{
			"id":           "custom-astra-explicit-no-ultra",
			"display_name": "Astra Without Ultra",
		},
		{
			"id":           "gpt-6-astra",
			"display_name": "Direct Canonical With Explicit Thinking",
		},
	}

	resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 3 {
		t.Fatalf("models = %#v, want three models", resp["models"])
	}

	entries := make(map[string]map[string]any, len(models))
	for _, entry := range models {
		entries[stringModelValue(entry, "slug")] = entry
	}

	entry := entries["custom-sol"]
	if entry == nil {
		t.Fatal("missing custom-sol")
	}
	if got := stringModelValue(entry, "slug"); got != "custom-sol" {
		t.Errorf("slug = %q, want custom-sol", got)
	}
	if got := stringModelValue(entry, "display_name"); got != "Overridden Display Name" {
		t.Errorf("display_name = %q, want Overridden Display Name", got)
	}
	if got := intModelValue(entry, "context_window"); got != 123456 {
		t.Errorf("context_window = %d, want 123456", got)
	}
	if got := intModelValue(entry, "max_context_window"); got != 123456 {
		t.Errorf("max_context_window = %d, want 123456", got)
	}
	if got := stringModelValue(entry, "comp_hash"); got != "3000" {
		t.Errorf("comp_hash = %q, want 3000 (inherited from gpt-5.6-sol)", got)
	}
	// Assert reasoning levels were overridden to [low, high]
	levels, okLevels := entry["supported_reasoning_levels"].([]any)
	if !okLevels || len(levels) != 2 {
		t.Fatalf("custom-sol supported_reasoning_levels = %#v, want [low, high]", entry["supported_reasoning_levels"])
	}
	if got := stringModelValue(entry, "default_reasoning_level"); got != "low" {
		t.Errorf("default_reasoning_level = %q, want low", got)
	}

	// Assert custom-astra-explicit-no-ultra has exactly the 5 explicit levels and NO ultra
	astraNoUltra := entries["custom-astra-explicit-no-ultra"]
	if astraNoUltra == nil {
		t.Fatal("missing custom-astra-explicit-no-ultra")
	}
	astraLevels, okAstraLevels := astraNoUltra["supported_reasoning_levels"].([]any)
	if !okAstraLevels || len(astraLevels) != 5 {
		t.Fatalf("custom-astra-explicit-no-ultra supported_reasoning_levels = %#v, want 5 levels", astraNoUltra["supported_reasoning_levels"])
	}
	for _, l := range astraLevels {
		lMap, _ := l.(map[string]any)
		if stringModelValue(lMap, "effort") == "ultra" {
			t.Error("custom-astra-explicit-no-ultra must not contain 'ultra' because user explicitly excluded it")
		}
	}

	// Assert direct canonical model gpt-6-astra also honors explicit thinking override
	directAstra := entries["gpt-6-astra"]
	if directAstra == nil {
		t.Fatal("missing direct gpt-6-astra entry")
	}
	directLevels, okDirectLevels := directAstra["supported_reasoning_levels"].([]any)
	if !okDirectLevels || len(directLevels) != 2 {
		t.Fatalf("direct gpt-6-astra supported_reasoning_levels = %#v, want [low, high]", directAstra["supported_reasoning_levels"])
	}
	if got := stringModelValue(directAstra, "default_reasoning_level"); got != "low" {
		t.Errorf("direct gpt-6-astra default_reasoning_level = %q, want low", got)
	}
}

func TestCodexClientModelsResponse_UnrelatedCustomProviderRetainsFallback(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "codex-unrelated-custom-provider-test-client"
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{
		{
			ID:          "my-unrelated-custom-model",
			DisplayName: "Unrelated Model",
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	availableModels := []map[string]any{
		{
			"id":           "my-unrelated-custom-model",
			"display_name": "Unrelated Model",
		},
	}

	resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}

	entry := models[0]
	if got := stringModelValue(entry, "slug"); got != "my-unrelated-custom-model" {
		t.Errorf("slug = %q, want my-unrelated-custom-model", got)
	}
	if got := stringModelValue(entry, "comp_hash"); got != "2911" {
		t.Errorf("comp_hash = %q, want 2911 (from generic gpt-5.5 fallback)", got)
	}
	if got, _ := entry["supports_search_tool"].(bool); got {
		t.Errorf("supports_search_tool = %v, want false for non-template fallback", got)
	}
	// Fallback model without explicit modalities retains generic gpt-5.5 fallback modalities
	mods, okMods := entry["input_modalities"].([]any)
	if !okMods || len(mods) != 2 {
		t.Fatalf("unrelated model input_modalities = %#v, want generic [text image]", entry["input_modalities"])
	}
	if got, _ := entry["supports_image_detail_original"].(bool); !got {
		t.Errorf("supports_image_detail_original = %v, want true for generic fallback", got)
	}
}

func TestCodexClientModelsResponse_UnrelatedProviderCannotWidenSearchTool(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "codex-unrelated-provider-widen-test-client"
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{
		{
			ID:              "custom-astra-on-openai-compat",
			MetadataModelID: "gpt-6-astra",
			DisplayName:     "Astra on OpenAI Compat",
			// OpenAI compatibility models get low/medium/high registered without ExplicitThinking
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "medium", "high"},
			},
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	availableModels := []map[string]any{
		{
			"id":           "custom-astra-on-openai-compat",
			"display_name": "Astra on OpenAI Compat",
		},
	}

	resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}

	entry := models[0]
	if got := stringModelValue(entry, "slug"); got != "custom-astra-on-openai-compat" {
		t.Errorf("slug = %q, want custom-astra-on-openai-compat", got)
	}
	// Inherited model metadata from canonical gpt-6-astra
	if got := stringModelValue(entry, "comp_hash"); got != "3000" {
		t.Errorf("comp_hash = %q, want 3000", got)
	}
	if got := intModelValue(entry, "max_context_window"); got != 872000 {
		t.Errorf("max_context_window = %d, want 872000", got)
	}
	// Protocol-level capabilities MUST be restricted for non-Codex providers
	if got, _ := entry["supports_search_tool"].(bool); got {
		t.Errorf("supports_search_tool = %v, want false because provider is openai-compatibility, not codex", got)
	}
	if got, _ := entry["prefer_websockets"].(bool); got {
		t.Errorf("prefer_websockets = %v, want false for non-Codex provider", got)
	}
	if _, exists := entry["apply_patch_tool_type"]; exists {
		t.Errorf("apply_patch_tool_type should be deleted for non-Codex provider, got %#v", entry["apply_patch_tool_type"])
	}
	if tiers, okTiers := entry["service_tiers"].([]any); !okTiers || len(tiers) != 0 {
		t.Errorf("service_tiers = %#v, want empty array for non-Codex provider", entry["service_tiers"])
	}
	if _, exists := entry["upgrade"]; exists {
		t.Errorf("upgrade should be deleted for non-Codex provider")
	}
	if _, exists := entry["availability_nux"]; exists {
		t.Errorf("availability_nux should be deleted for non-Codex provider")
	}
	// Reasoning levels must NOT be widened beyond provider's registered capabilities
	levels, okLevels := entry["supported_reasoning_levels"].([]any)
	if !okLevels || len(levels) != 3 {
		t.Fatalf("supported_reasoning_levels = %#v, want 3 levels restricted to provider", entry["supported_reasoning_levels"])
	}
	for _, l := range levels {
		lMap, _ := l.(map[string]any)
		effort := stringModelValue(lMap, "effort")
		if effort == "ultra" || effort == "max" || effort == "xhigh" {
			t.Errorf("non-Codex provider must not widen reasoning levels to %q", effort)
		}
	}
}

func TestCodexClientModelsResponse_MixedProvidersRestrictProtocolCapabilities(t *testing.T) {
	for _, tt := range []struct {
		name       string
		codexFirst bool
	}{
		{name: "codex_first_compat_second", codexFirst: true},
		{name: "compat_first_codex_second", codexFirst: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modelRegistry := registry.GetGlobalRegistry()
			clientCodex := "codex-mixed-provider-client-codex-" + tt.name
			clientCompat := "codex-mixed-provider-client-compat-" + tt.name
			aliasName := "mixed-astra-alias-" + tt.name

			registerCodex := func() {
				modelRegistry.RegisterClient(clientCodex, "codex", []*registry.ModelInfo{
					{
						ID:              aliasName,
						MetadataModelID: "gpt-6-astra",
						DisplayName:     "Mixed Astra Alias",
					},
				})
			}
			registerCompat := func() {
				modelRegistry.RegisterClient(clientCompat, "openai-compatibility", []*registry.ModelInfo{
					{
						ID:              aliasName,
						MetadataModelID: "gpt-6-astra",
						DisplayName:     "Mixed Astra Alias",
						Thinking: &registry.ThinkingSupport{
							Levels: []string{"low", "medium", "high"},
						},
					},
				})
			}

			if tt.codexFirst {
				registerCodex()
				registerCompat()
			} else {
				registerCompat()
				registerCodex()
			}

			t.Cleanup(func() {
				modelRegistry.UnregisterClient(clientCodex)
				modelRegistry.UnregisterClient(clientCompat)
			})

			availableModels := []map[string]any{
				{"id": aliasName},
			}

			resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
			models, ok := resp["models"].([]map[string]any)
			if !ok || len(models) != 1 {
				t.Fatalf("models = %#v, want one model", resp["models"])
			}

			entry := models[0]
			// Inherited model metadata from canonical gpt-6-astra
			if got := stringModelValue(entry, "comp_hash"); got != "3000" {
				t.Errorf("comp_hash = %q, want 3000", got)
			}
			if got := intModelValue(entry, "max_context_window"); got != 872000 {
				t.Errorf("max_context_window = %d, want 872000", got)
			}
			// Protocol-level capabilities MUST be restricted because providers are mixed (not pure Codex)
			if got, _ := entry["supports_search_tool"].(bool); got {
				t.Errorf("supports_search_tool = %v, want false for mixed provider", got)
			}
			if got, _ := entry["prefer_websockets"].(bool); got {
				t.Errorf("prefer_websockets = %v, want false for mixed provider", got)
			}
			if _, exists := entry["apply_patch_tool_type"]; exists {
				t.Errorf("apply_patch_tool_type should be deleted for mixed provider")
			}
			if tiers, okTiers := entry["service_tiers"].([]any); !okTiers || len(tiers) != 0 {
				t.Errorf("service_tiers = %#v, want empty array for mixed provider", entry["service_tiers"])
			}
			if _, exists := entry["upgrade"]; exists {
				t.Errorf("upgrade should be deleted for mixed provider")
			}
			if _, exists := entry["availability_nux"]; exists {
				t.Errorf("availability_nux should be deleted for mixed provider")
			}
			// Reasoning levels must NOT be widened to ultra/max/xhigh regardless of registration order
			levels, okLevels := entry["supported_reasoning_levels"].([]any)
			if !okLevels || len(levels) != 3 {
				t.Fatalf("supported_reasoning_levels = %#v, want 3 levels restricted to mixed provider", entry["supported_reasoning_levels"])
			}
			for _, l := range levels {
				lMap, _ := l.(map[string]any)
				effort := stringModelValue(lMap, "effort")
				if effort == "ultra" || effort == "max" || effort == "xhigh" {
					t.Errorf("mixed provider must not widen reasoning levels to %q", effort)
				}
			}
		})
	}
}

func TestCodexClientModelsResponse_MixedProvidersIntersectCodexExplicitRestrictions(t *testing.T) {
	for _, tt := range []struct {
		name       string
		codexFirst bool
	}{
		{name: "codex_explicit_first", codexFirst: true},
		{name: "compat_first_codex_explicit_second", codexFirst: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modelRegistry := registry.GetGlobalRegistry()
			clientCodex := "codex-explicit-client-" + tt.name
			clientCompat := "compat-client-" + tt.name
			aliasName := "intersect-explicit-alias-" + tt.name

			registerCodex := func() {
				modelRegistry.RegisterClient(clientCodex, "codex", []*registry.ModelInfo{
					{
						ID:               aliasName,
						MetadataModelID:  "gpt-6-astra",
						DisplayName:      "Astra Mixed Explicit",
						ExplicitThinking: true,
						Thinking: &registry.ThinkingSupport{
							// Codex explicitly restricted to low, high
							Levels: []string{"low", "high"},
						},
					},
				})
			}
			registerCompat := func() {
				modelRegistry.RegisterClient(clientCompat, "openai-compatibility", []*registry.ModelInfo{
					{
						ID:              aliasName,
						MetadataModelID: "gpt-6-astra",
						DisplayName:     "Astra Mixed Explicit",
						Thinking: &registry.ThinkingSupport{
							// Compat supports low, medium, high
							Levels: []string{"low", "medium", "high"},
						},
					},
				})
			}

			if tt.codexFirst {
				registerCodex()
				registerCompat()
			} else {
				registerCompat()
				registerCodex()
			}

			t.Cleanup(func() {
				modelRegistry.UnregisterClient(clientCodex)
				modelRegistry.UnregisterClient(clientCompat)
			})

			availableModels := []map[string]any{
				{"id": aliasName},
			}

			resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
			models, ok := resp["models"].([]map[string]any)
			if !ok || len(models) != 1 {
				t.Fatalf("models = %#v, want one model", resp["models"])
			}

			entry := models[0]
			// The intersection of [low, high] and [low, medium, high] is exactly [low, high]
			levels, okLevels := entry["supported_reasoning_levels"].([]any)
			if !okLevels || len(levels) != 2 {
				t.Fatalf("supported_reasoning_levels = %#v, want exactly 2 levels (low, high)", entry["supported_reasoning_levels"])
			}
			for _, l := range levels {
				lMap, _ := l.(map[string]any)
				effort := stringModelValue(lMap, "effort")
				if effort == "medium" || effort == "ultra" || effort == "max" || effort == "xhigh" {
					t.Errorf("mixed route must not contain %q because Codex explicitly excluded it", effort)
				}
			}
		})
	}
}

func TestCodexClientModelsResponse_DisjointModalitiesIntersectionDoesNotRestoreTemplate(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientA := "modalities-client-a"
	clientB := "modalities-client-b"
	clientC := "modalities-client-c"
	aliasName := "disjoint-modalities-alias"

	// Provider A supports text only
	modelRegistry.RegisterClient(clientA, "openai-compatibility", []*registry.ModelInfo{
		{
			ID:                       aliasName,
			MetadataModelID:          "gpt-6-astra",
			SupportedInputModalities: []string{"text"},
		},
	})
	// Provider B supports image only
	modelRegistry.RegisterClient(clientB, "custom-provider-b", []*registry.ModelInfo{
		{
			ID:                       aliasName,
			MetadataModelID:          "gpt-6-astra",
			SupportedInputModalities: []string{"image"},
		},
	})
	// Provider C supports audio only (3-provider test)
	modelRegistry.RegisterClient(clientC, "custom-provider-c", []*registry.ModelInfo{
		{
			ID:                       aliasName,
			MetadataModelID:          "gpt-6-astra",
			SupportedInputModalities: []string{"audio"},
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientA)
		modelRegistry.UnregisterClient(clientB)
		modelRegistry.UnregisterClient(clientC)
	})

	availableModels := []map[string]any{
		{"id": aliasName},
	}

	resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}

	entry := models[0]
	// Empty intersection must remain empty and NOT restore template's ["text", "image"]
	mods, okMods := entry["input_modalities"].([]any)
	if !okMods || len(mods) != 0 {
		t.Fatalf("disjoint input_modalities = %#v, want empty array []", entry["input_modalities"])
	}
	if got, _ := entry["supports_image_detail_original"].(bool); got {
		t.Errorf("supports_image_detail_original must be absent/false for disjoint modalities")
	}
}

func TestCodexClientModelsResponse_AudioOnlyModalitiesDoesNotRestoreTemplate(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientA := "audio-only-client"
	aliasName := "audio-only-astra-alias"

	modelRegistry.RegisterClient(clientA, "openai-compatibility", []*registry.ModelInfo{
		{
			ID:                       aliasName,
			MetadataModelID:          "gpt-6-astra",
			SupportedInputModalities: []string{"audio"},
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientA)
	})

	availableModels := []map[string]any{
		{"id": aliasName},
	}

	resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
	models, ok := resp["models"].([]map[string]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v, want one model", resp["models"])
	}

	entry := models[0]
	mods, okMods := entry["input_modalities"].([]any)
	if !okMods || len(mods) != 0 {
		t.Fatalf("audio-only input_modalities = %#v, want empty array []", entry["input_modalities"])
	}
	if got, _ := entry["supports_image_detail_original"].(bool); got {
		t.Errorf("supports_image_detail_original must be absent/false for audio-only model")
	}
}

func TestCodexClientModelsResponse_MixedProviderWithoutThinkingRestrictsReasoning(t *testing.T) {
	for _, tt := range []struct {
		name       string
		codexFirst bool
	}{
		{name: "codex_first_no_thinking_second", codexFirst: true},
		{name: "no_thinking_first_codex_second", codexFirst: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modelRegistry := registry.GetGlobalRegistry()
			clientCodex := "codex-mixed-no-think-" + tt.name
			clientCompat := "compat-mixed-no-think-" + tt.name
			aliasName := "mixed-no-think-alias-" + tt.name

			registerCodex := func() {
				modelRegistry.RegisterClient(clientCodex, "codex", []*registry.ModelInfo{
					{
						ID:              aliasName,
						MetadataModelID: "gpt-6-astra",
						DisplayName:     "Mixed Astra Alias",
					},
				})
			}
			registerCompat := func() {
				modelRegistry.RegisterClient(clientCompat, "openai-compatibility", []*registry.ModelInfo{
					{
						ID:              aliasName,
						MetadataModelID: "gpt-6-astra",
						DisplayName:     "Mixed Astra Alias",
						// Compat provider does NOT support thinking (Thinking == nil)
						Thinking: nil,
					},
				})
			}

			if tt.codexFirst {
				registerCodex()
				registerCompat()
			} else {
				registerCompat()
				registerCodex()
			}

			t.Cleanup(func() {
				modelRegistry.UnregisterClient(clientCodex)
				modelRegistry.UnregisterClient(clientCompat)
			})

			availableModels := []map[string]any{
				{"id": aliasName},
			}

			resp := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
			models, ok := resp["models"].([]map[string]any)
			if !ok || len(models) != 1 {
				t.Fatalf("models = %#v, want one model", resp["models"])
			}

			entry := models[0]
			// Because compat provider does not support thinking, mixed route must NOT advertise reasoning levels
			levels, okLevels := entry["supported_reasoning_levels"].([]any)
			if !okLevels || len(levels) != 0 {
				t.Fatalf("supported_reasoning_levels = %#v, want empty array [] when one provider has no thinking support", entry["supported_reasoning_levels"])
			}
			if _, exists := entry["default_reasoning_level"]; exists {
				t.Fatalf("default_reasoning_level should be deleted when no reasoning levels supported")
			}
		})
	}
}

func TestCodexClientModelsResponse_OAuthAliasesInheritCompleteReasoningLevelsWithUltra(t *testing.T) {
	// Look up real Astra definition from registry
	realAstra := registry.LookupModelInfo("gpt-6-astra")
	if realAstra == nil {
		t.Fatal("expected gpt-6-astra in static/dynamic registry")
	}

	// Create alias and prefixed alias models that inherit real Astra's Thinking from registry
	codexMain := *realAstra
	codexMain.ID = "codex-main"
	codexMain.MetadataModelID = "gpt-6-astra"

	prefixedMain := *realAstra
	prefixedMain.ID = "1/codex-main"
	prefixedMain.MetadataModelID = "gpt-6-astra"

	modelRegistry := registry.GetGlobalRegistry()
	clientID := "codex-reasoning-levels-test-client"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		realAstra,
		&codexMain,
		&prefixedMain,
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	availableModels := []map[string]any{
		{"id": "gpt-6-astra"},
		{"id": "codex-main"},
		{"id": "1/codex-main"},
	}

	// 1. Test for modern client version (0.153.4) which supports "ultra"
	respModern := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.153.4")
	modelsModern, ok := respModern["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", respModern["models"])
	}

	entriesModern := make(map[string]map[string]any, len(modelsModern))
	for _, entry := range modelsModern {
		entriesModern[stringModelValue(entry, "slug")] = entry
	}

	for _, slug := range []string{"gpt-6-astra", "codex-main", "1/codex-main"} {
		entry := entriesModern[slug]
		if entry == nil {
			t.Fatalf("missing modern entry for %q", slug)
		}
		rawLevels, okLevels := entry["supported_reasoning_levels"].([]any)
		if !okLevels {
			t.Fatalf("%s supported_reasoning_levels missing or wrong type: %#v", slug, entry["supported_reasoning_levels"])
		}
		hasUltra := false
		hasDescriptions := false
		for _, rawLevel := range rawLevels {
			levelMap, okMap := rawLevel.(map[string]any)
			if !okMap {
				continue
			}
			if stringModelValue(levelMap, "effort") == "ultra" {
				hasUltra = true
			}
			if stringModelValue(levelMap, "description") != "" {
				hasDescriptions = true
			}
		}
		if !hasUltra {
			t.Errorf("%s must support 'ultra' reasoning level on modern client (inherited from gpt-6-astra template)", slug)
		}
		if !hasDescriptions {
			t.Errorf("%s reasoning levels must retain rich descriptions from template", slug)
		}
	}

	// 2. Test for older client version (0.137.0) which does not support "ultra" or "max"
	respLegacy := BuildResponseForClient(availableModels, modelRegistry.GetModelProviders, false, "0.137.0")
	modelsLegacy, ok := respLegacy["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", respLegacy["models"])
	}

	entriesLegacy := make(map[string]map[string]any, len(modelsLegacy))
	for _, entry := range modelsLegacy {
		entriesLegacy[stringModelValue(entry, "slug")] = entry
	}

	for _, slug := range []string{"gpt-6-astra", "codex-main", "1/codex-main"} {
		entry := entriesLegacy[slug]
		if entry == nil {
			t.Fatalf("missing legacy entry for %q", slug)
		}
		rawLevels, okLevels := entry["supported_reasoning_levels"].([]any)
		if !okLevels {
			t.Fatalf("%s legacy supported_reasoning_levels missing: %#v", slug, entry["supported_reasoning_levels"])
		}
		for _, rawLevel := range rawLevels {
			levelMap, okMap := rawLevel.(map[string]any)
			if !okMap {
				continue
			}
			effort := stringModelValue(levelMap, "effort")
			if effort == "ultra" || effort == "max" {
				t.Errorf("%s legacy client must filter out %q", slug, effort)
			}
		}
	}
}
