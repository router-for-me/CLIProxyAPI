package models

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCodexClientModelsResponse_InputModalitiesFromRegistry(t *testing.T) {
	modelID := "mimo-v2.5-pro-codex-test"
	textOnlyModelID := "mimo-text-only-codex-test"
	sharedImageModelID := "compat-shared-image-codex-test"
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
		{
			ID:               sharedImageModelID,
			Object:           "model",
			OwnedBy:          "mimo",
			Type:             "openai-compatibility",
			SupportsImageAPI: true,
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
	var sharedImageEntry map[string]any
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
		case sharedImageModelID:
			sharedImageEntry = entry
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
	if sharedImageEntry == nil {
		t.Fatalf("expected codex entry for %q", sharedImageModelID)
	}
	if got, _ := sharedImageEntry["visibility"].(string); got == "hide" {
		t.Fatalf("shared image-capable model visibility = %q, want visible", got)
	}
}

func TestCodexClientModelsResponse_SharedImageAliasPrefersChatRegistration(t *testing.T) {
	const modelID = "gpt-image-2"

	for _, imageFirst := range []bool{false, true} {
		name := "chat-first"
		if imageFirst {
			name = "image-first"
		}
		t.Run(name, func(t *testing.T) {
			modelRegistry := registry.GetGlobalRegistry()
			chatClientID := "codex-shared-chat-" + name
			imageClientID := "codex-shared-image-" + name
			chat := &registry.ModelInfo{
				ID:                       modelID,
				Object:                   "model",
				OwnedBy:                  "chat-owner",
				Type:                     "openai-compatibility",
				DisplayName:              "Shared Chat Model",
				Description:              "Chat metadata",
				ContextLength:            123456,
				SupportedInputModalities: []string{"text", "image"},
				Thinking:                 &registry.ThinkingSupport{Levels: []string{"low", "high"}},
			}
			image := &registry.ModelInfo{
				ID:          modelID,
				Object:      "model",
				OwnedBy:     "image-owner",
				Type:        registry.OpenAIImageModelType,
				DisplayName: "Image-only Model",
				Description: "Image metadata",
			}
			registerChat := func() {
				modelRegistry.RegisterClient(chatClientID, "openai-compatibility", []*registry.ModelInfo{chat})
			}
			registerImage := func() {
				modelRegistry.RegisterClient(imageClientID, "codex", []*registry.ModelInfo{image})
			}
			if imageFirst {
				registerImage()
				registerChat()
			} else {
				registerChat()
				registerImage()
			}
			t.Cleanup(func() {
				modelRegistry.UnregisterClient(chatClientID)
				modelRegistry.UnregisterClient(imageClientID)
			})

			entry := codexClientModelTestEntry(t, modelRegistry, modelID)
			if got := stringModelValue(entry, "display_name"); got != "Shared Chat Model" {
				t.Fatalf("shared display_name = %q, want chat metadata", got)
			}
			if got := stringModelValue(entry, "visibility"); got == "hide" {
				t.Fatalf("shared visibility = %q, want visible chat route", got)
			}
			modalities, ok := entry["input_modalities"].([]any)
			if !ok || len(modalities) != 2 || modalities[0] != "text" || modalities[1] != "image" {
				t.Fatalf("shared input_modalities = %#v, want chat metadata", entry["input_modalities"])
			}
			levels, ok := entry["supported_reasoning_levels"].([]any)
			if !ok || len(levels) != 2 {
				t.Fatalf("shared supported_reasoning_levels = %#v, want chat metadata", entry["supported_reasoning_levels"])
			}
			high, ok := levels[1].(map[string]any)
			if !ok || stringModelValue(high, "effort") != "high" {
				t.Fatalf("shared supported_reasoning_levels = %#v, want high effort", entry["supported_reasoning_levels"])
			}

			modelRegistry.UnregisterClient(chatClientID)

			entry = codexClientModelTestEntry(t, modelRegistry, modelID)
			if got := stringModelValue(entry, "display_name"); got != "Image-only Model" {
				t.Fatalf("remaining display_name = %q, want image metadata", got)
			}
			if got := stringModelValue(entry, "visibility"); got != "hide" {
				t.Fatalf("remaining visibility = %q, want hidden image-only route", got)
			}
			if _, exists := entry["input_modalities"]; exists {
				t.Fatalf("image-only route input_modalities = %#v, want omitted", entry["input_modalities"])
			}
		})
	}
}

func TestCodexClientModelsResponse_HardcodedImageIDWithChatOnlyRouteIsVisible(t *testing.T) {
	const (
		clientID = "codex-hardcoded-chat-only-test"
		modelID  = "gpt-image-2"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{{
		ID:          modelID,
		Object:      "model",
		OwnedBy:     "chat-owner",
		Type:        "openai-compatibility",
		DisplayName: "Chat-only Alias",
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	info, supportsChat := modelRegistry.GetCatalogModelInfo(modelID)
	if !supportsChat || info == nil || info.SupportsImageAPI {
		t.Fatalf("chat-only aggregate = %#v, supportsChat=%v", info, supportsChat)
	}
	entry := codexClientModelTestEntry(t, modelRegistry, modelID)
	if got := stringModelValue(entry, "visibility"); got == "hide" {
		t.Fatalf("chat-only hardcoded alias visibility = %q, want visible", got)
	}
	if got := stringModelValue(entry, "display_name"); got != "Chat-only Alias" {
		t.Fatalf("chat-only hardcoded alias display_name = %q", got)
	}
}

func TestCodexClientModelsResponse_TemplateSlugUsesAggregateChatCapability(t *testing.T) {
	const (
		modelID       = "gpt-5.5"
		imageClientID = "codex-template-image-only"
		chatClientID  = "codex-template-chat"
	)
	assertReasoningLevels := func(t *testing.T, entry map[string]any, expected ...string) {
		t.Helper()
		levels, ok := entry["supported_reasoning_levels"].([]any)
		if !ok || len(levels) != len(expected) {
			t.Fatalf("supported_reasoning_levels = %#v, want %v", entry["supported_reasoning_levels"], expected)
		}
		for i, expectedLevel := range expected {
			level, ok := levels[i].(map[string]any)
			if !ok || stringModelValue(level, "effort") != expectedLevel {
				t.Fatalf("supported_reasoning_levels = %#v, want %v", entry["supported_reasoning_levels"], expected)
			}
		}
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(imageClientID)
	modelRegistry.UnregisterClient(chatClientID)
	modelRegistry.RegisterClient(imageClientID, "codex", []*registry.ModelInfo{{
		ID:               modelID,
		Type:             "openai",
		DisplayName:      "Template Image Alias",
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(imageClientID)
		modelRegistry.UnregisterClient(chatClientID)
	})

	entry := codexClientModelTestEntry(t, modelRegistry, modelID)
	if got := stringModelValue(entry, "visibility"); got != "hide" {
		t.Fatalf("image-only template visibility = %q, want hide", got)
	}
	if _, exists := entry["input_modalities"]; exists {
		t.Fatalf("image-only template input_modalities = %#v, want omitted", entry["input_modalities"])
	}
	if _, exists := entry["supports_image_detail_original"]; exists {
		t.Fatalf("image-only template supports_image_detail_original = %#v, want omitted", entry["supports_image_detail_original"])
	}

	modelRegistry.RegisterClient(chatClientID, "codex", []*registry.ModelInfo{{
		ID:                       modelID,
		Type:                     "openai",
		DisplayName:              "Template Chat Alias",
		SupportedInputModalities: []string{"text", "image"},
		Thinking:                 &registry.ThinkingSupport{Levels: []string{"low", "high"}},
	}})
	entry = codexClientModelTestEntry(t, modelRegistry, modelID)
	if got := stringModelValue(entry, "visibility"); got == "hide" {
		t.Fatalf("shared template visibility = %q, want visible chat route", got)
	}
	if got := stringModelValue(entry, "display_name"); got != "Template Chat Alias" {
		t.Fatalf("shared template display_name = %q, want chat metadata", got)
	}
	modalities, ok := entry["input_modalities"].([]any)
	if !ok || len(modalities) != 2 || modalities[0] != "text" || modalities[1] != "image" {
		t.Fatalf("shared template input_modalities = %#v, want [text image]", entry["input_modalities"])
	}
	assertReasoningLevels(t, entry, "low", "high")
	if got := stringModelValue(entry, "default_reasoning_level"); got != "low" {
		t.Fatalf("configured default_reasoning_level = %q, want low", got)
	}

	modelRegistry.RegisterClient(chatClientID, "codex", []*registry.ModelInfo{{
		ID:                       modelID,
		Type:                     "openai",
		DisplayName:              "Template Chat Alias",
		SupportedInputModalities: []string{"text", "image"},
	}})
	entry = codexClientModelTestEntry(t, modelRegistry, modelID)
	assertReasoningLevels(t, entry, "low", "medium", "high", "xhigh")
	if got := stringModelValue(entry, "default_reasoning_level"); got != "medium" {
		t.Fatalf("template default_reasoning_level = %q, want medium", got)
	}
}

func TestCodexClientModelsResponse_NativeImageAliasUsesNonChatMetadata(t *testing.T) {
	const (
		clientID = "codex-native-image-alias-test"
		modelID  = "tenant/public-image"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "xai", []*registry.ModelInfo{{
		ID:               modelID,
		Object:           "model",
		OwnedBy:          "xai",
		Type:             "xai",
		DisplayName:      "Public Image",
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	entry := codexClientModelTestEntry(t, modelRegistry, modelID)
	if got := stringModelValue(entry, "visibility"); got != "hide" {
		t.Fatalf("native image alias visibility = %q, want hide", got)
	}
	if _, exists := entry["input_modalities"]; exists {
		t.Fatalf("native image alias input_modalities = %#v, want omitted", entry["input_modalities"])
	}
}

func TestCodexClientModelsResponse_SharedImageAliasSuspensionTransitions(t *testing.T) {
	const (
		chatClientID  = "codex-shared-suspension-chat-test"
		imageClientID = "codex-shared-suspension-image-test"
		modelID       = "gpt-image-2"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(chatClientID, "openai-compatibility", []*registry.ModelInfo{{
		ID:          modelID,
		Object:      "model",
		OwnedBy:     "chat-owner",
		Type:        "openai-compatibility",
		DisplayName: "Suspension Chat Model",
	}})
	modelRegistry.RegisterClient(imageClientID, "openai-compatibility", []*registry.ModelInfo{{
		ID:               modelID,
		Object:           "model",
		OwnedBy:          "image-owner",
		Type:             registry.OpenAIImageModelType,
		DisplayName:      "Suspension Image Model",
		SupportsImageAPI: true,
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(chatClientID)
		modelRegistry.UnregisterClient(imageClientID)
	})

	modelRegistry.SuspendClientModel(chatClientID, modelID, "model_not_supported")
	entry := codexClientModelTestEntry(t, modelRegistry, modelID)
	if got := stringModelValue(entry, "visibility"); got != "hide" {
		t.Fatalf("chat-suspended visibility = %q, want hidden image-only route", got)
	}
	if got := stringModelValue(entry, "display_name"); got != "Suspension Image Model" {
		t.Fatalf("chat-suspended display_name = %q, want image metadata", got)
	}

	modelRegistry.ResumeClientModel(chatClientID, modelID)
	entry = codexClientModelTestEntry(t, modelRegistry, modelID)
	if got := stringModelValue(entry, "visibility"); got == "hide" {
		t.Fatalf("resumed chat visibility = %q, want visible", got)
	}
	if got := stringModelValue(entry, "display_name"); got != "Suspension Chat Model" {
		t.Fatalf("resumed chat display_name = %q, want chat metadata", got)
	}

	modelRegistry.SuspendClientModel(imageClientID, modelID, "model_not_supported")
	info, supportsChat := modelRegistry.GetCatalogModelInfo(modelID)
	if !supportsChat || info == nil || info.SupportsImageAPI {
		t.Fatalf("image-suspended aggregate = %#v, supportsChat=%v", info, supportsChat)
	}
	entry = codexClientModelTestEntry(t, modelRegistry, modelID)
	if got := stringModelValue(entry, "visibility"); got == "hide" {
		t.Fatalf("image-suspended chat visibility = %q, want visible", got)
	}
}

func codexClientModelTestEntry(t *testing.T, modelRegistry *registry.ModelRegistry, modelID string) map[string]any {
	t.Helper()
	response := BuildResponse(modelRegistry.GetAvailableModels("openai"), nil, false)
	models, ok := response["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", response["models"])
	}
	for _, entry := range models {
		if stringModelValue(entry, "slug") == modelID {
			return entry
		}
	}
	t.Fatalf("expected Codex client entry for %q", modelID)
	return nil
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

	applyCodexClientModelMetadata(entry, "custom-model", model, nil, false, false)
	if got := entry["multi_agent_version"]; got != "v1" {
		t.Fatalf("disabled multi_agent_version = %#v, want preserved v1", got)
	}

	applyCodexClientModelMetadata(entry, "custom-model", model, nil, false, true)
	if got := entry["multi_agent_version"]; got != "v2" {
		t.Fatalf("enabled multi_agent_version = %#v, want v2", got)
	}
}
