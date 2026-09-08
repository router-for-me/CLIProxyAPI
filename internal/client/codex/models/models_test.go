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
