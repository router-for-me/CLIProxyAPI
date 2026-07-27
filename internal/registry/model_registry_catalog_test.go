package registry

import "testing"

func TestGetCatalogModelInfoPrefersChatRegistration(t *testing.T) {
	for _, imageFirst := range []bool{false, true} {
		name := "chat-first"
		if imageFirst {
			name = "image-first"
		}
		t.Run(name, func(t *testing.T) {
			r := newTestModelRegistry()
			registerSharedCatalogTestModel(r, imageFirst)

			info, supportsChat := r.GetCatalogModelInfo("shared-catalog-model")
			if !supportsChat {
				t.Fatal("expected shared model to support chat")
			}
			if info == nil {
				t.Fatal("expected aggregate model metadata")
			}
			if info.Type != "openai-compatibility" || info.DisplayName != "Chat Model" {
				t.Fatalf("aggregate metadata = %#v, want chat registration", info)
			}
			if !info.SupportsImageAPI {
				t.Fatal("expected aggregate image API capability")
			}
			if info.Thinking == nil || len(info.Thinking.Levels) != 2 || info.Thinking.Levels[1] != "high" {
				t.Fatalf("aggregate thinking = %#v, want chat thinking metadata", info.Thinking)
			}
			if len(info.SupportedInputModalities) != 2 || info.SupportedInputModalities[1] != "image" {
				t.Fatalf("aggregate input modalities = %#v, want chat modalities", info.SupportedInputModalities)
			}

			r.UnregisterClient("chat-client")

			info, supportsChat = r.GetCatalogModelInfo("shared-catalog-model")
			if supportsChat {
				t.Fatal("expected image-only model after unregistering the last chat client")
			}
			if info == nil || info.Type != OpenAIImageModelType || info.DisplayName != "Image Model" {
				t.Fatalf("remaining metadata = %#v, want image-only registration", info)
			}
			if !info.SupportsImageAPI {
				t.Fatal("expected remaining image API capability")
			}
		})
	}
}

func TestGetAvailableModelsPrefersChatRegistrationMetadata(t *testing.T) {
	for _, imageFirst := range []bool{false, true} {
		name := "chat-first"
		if imageFirst {
			name = "image-first"
		}
		t.Run(name, func(t *testing.T) {
			r := newTestModelRegistry()
			registerSharedCatalogTestModel(r, imageFirst)

			model := onlyAvailableCatalogTestModel(t, r)
			if got := model["type"]; got != "openai-compatibility" {
				t.Fatalf("shared model type = %#v, want chat type", got)
			}
			if got := model["display_name"]; got != "Chat Model" {
				t.Fatalf("shared display_name = %#v, want chat metadata", got)
			}
			if got := model["context_length"]; got != 123456 {
				t.Fatalf("shared context_length = %#v, want chat metadata", got)
			}

			r.UnregisterClient("chat-client")

			model = onlyAvailableCatalogTestModel(t, r)
			if got := model["type"]; got != OpenAIImageModelType {
				t.Fatalf("remaining model type = %#v, want image-only type", got)
			}
			if got := model["display_name"]; got != "Image Model" {
				t.Fatalf("remaining display_name = %#v, want image metadata", got)
			}
		})
	}
}

func TestCatalogModelInfoSkipsNonQuotaSuspendedRegistrations(t *testing.T) {
	r := newTestModelRegistry()
	registerSharedCatalogTestModel(r, false)

	r.SuspendClientModel("chat-client", "shared-catalog-model", "model_not_supported")
	info, supportsChat := r.GetCatalogModelInfo("shared-catalog-model")
	if supportsChat || info == nil || info.DisplayName != "Image Model" {
		t.Fatalf("chat-suspended aggregate = %#v, supportsChat=%v", info, supportsChat)
	}
	if got := onlyAvailableCatalogTestModel(t, r)["display_name"]; got != "Image Model" {
		t.Fatalf("chat-suspended available metadata = %#v, want image metadata", got)
	}

	r.ResumeClientModel("chat-client", "shared-catalog-model")
	info, supportsChat = r.GetCatalogModelInfo("shared-catalog-model")
	if !supportsChat || info == nil || info.DisplayName != "Chat Model" || !info.SupportsImageAPI {
		t.Fatalf("resumed aggregate = %#v, supportsChat=%v", info, supportsChat)
	}

	r.SuspendClientModel("image-client", "shared-catalog-model", "model_not_supported")
	info, supportsChat = r.GetCatalogModelInfo("shared-catalog-model")
	if !supportsChat || info == nil || info.DisplayName != "Chat Model" {
		t.Fatalf("image-suspended aggregate = %#v, supportsChat=%v", info, supportsChat)
	}
	if info.SupportsImageAPI {
		t.Fatal("image capability should exclude the non-quota suspended image registration")
	}
	if got := onlyAvailableCatalogTestModel(t, r)["display_name"]; got != "Chat Model" {
		t.Fatalf("image-suspended available metadata = %#v, want chat metadata", got)
	}

	r.SuspendClientModel("chat-client", "shared-catalog-model", "model_not_supported")
	info, supportsChat = r.GetCatalogModelInfo("shared-catalog-model")
	if supportsChat || info != nil {
		t.Fatalf("all-suspended aggregate = %#v, supportsChat=%v, want unavailable", info, supportsChat)
	}
	if models := r.GetAvailableModels("openai"); len(models) != 0 {
		t.Fatalf("all-suspended available models = %#v, want none", models)
	}

	r.ResumeClientModel("chat-client", "shared-catalog-model")
	r.ResumeClientModel("image-client", "shared-catalog-model")
	r.SuspendClientModel("chat-client", "shared-catalog-model", "quota")
	info, supportsChat = r.GetCatalogModelInfo("shared-catalog-model")
	if !supportsChat || info == nil || info.DisplayName != "Chat Model" || !info.SupportsImageAPI {
		t.Fatalf("quota-suspended aggregate = %#v, supportsChat=%v", info, supportsChat)
	}
}

func TestGetCatalogModelInfoTreatsProviderBuiltinsAsNonChat(t *testing.T) {
	tests := []struct {
		provider string
		model    *ModelInfo
	}{
		{
			provider: "codex",
			model:    codexBuiltinImageModelInfo(),
		},
		{
			provider: "xai",
			model:    xaiBuiltinImageModelInfo(),
		},
		{
			provider: "xai",
			model:    xaiBuiltinVideoModelInfo(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.provider+"/"+tc.model.ID, func(t *testing.T) {
			r := newTestModelRegistry()
			r.RegisterClient("builtin-client", tc.provider, []*ModelInfo{tc.model})

			info, supportsChat := r.GetCatalogModelInfo(tc.model.ID)
			if supportsChat {
				t.Fatalf("builtin %q unexpectedly supports chat", tc.model.ID)
			}
			if info == nil || info.ID != tc.model.ID {
				t.Fatalf("builtin metadata = %#v, want %q", info, tc.model.ID)
			}
		})
	}
}

func TestGetCatalogModelInfoAggregatesVideoCapabilityAcrossSharedAlias(t *testing.T) {
	r := newTestModelRegistry()
	const modelID = "shared-video-catalog-model"
	r.RegisterClient("shared-video-chat", "xai", []*ModelInfo{{
		ID:          modelID,
		Type:        "xai",
		DisplayName: "Chat Model",
	}})
	r.RegisterClient("shared-video-endpoint", "xai", []*ModelInfo{{
		ID:               modelID,
		Type:             "xai",
		DisplayName:      "Video Model",
		SupportsVideoAPI: true,
		ChatDisabled:     true,
	}})

	info, supportsChat := r.GetCatalogModelInfo(modelID)
	if info == nil || !supportsChat || !info.SupportsVideoAPI || info.DisplayName != "Chat Model" {
		t.Fatalf("shared video aggregate = %#v, supportsChat=%v", info, supportsChat)
	}
	if !r.ClientModelSupportsExecution("shared-video-chat", modelID, ModelExecutionChat) {
		t.Fatal("chat registration lost chat execution")
	}
	if r.ClientModelSupportsExecution("shared-video-chat", modelID, ModelExecutionVideo) {
		t.Fatal("chat-only registration unexpectedly supports video")
	}
	if !r.ClientModelSupportsExecution("shared-video-endpoint", modelID, ModelExecutionVideo) {
		t.Fatal("video registration lost video execution")
	}
	if r.ClientModelSupportsExecution("shared-video-endpoint", modelID, ModelExecutionChat) {
		t.Fatal("video-only registration unexpectedly supports chat")
	}
}

func TestLookupModelExecutionSnapshot(t *testing.T) {
	r := newTestModelRegistry()
	const modelID = "shared-provider-execution-model"

	if snapshot, found := r.LookupModelExecutionSnapshot(modelID, ModelExecutionImage); found || snapshot.Supported {
		t.Fatalf("unregistered model = %#v, found %v; want empty, false", snapshot, found)
	}

	r.RegisterClient("compat-image", "openai-compatibility", []*ModelInfo{{
		ID:               modelID,
		Type:             OpenAIImageModelType,
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	snapshot, found := r.LookupModelExecutionSnapshot(modelID, ModelExecutionImage)
	if !found || !snapshot.Supported || snapshot.ProviderSupports("xai") || !snapshot.ProviderSupports("openai-compatibility") {
		t.Fatalf("compatibility image registration = %#v, found %v", snapshot, found)
	}
	if snapshot, found = r.LookupModelExecutionSnapshot("tenant/"+modelID, ModelExecutionImage); found || snapshot.Supported {
		t.Fatalf("different exact model ID = %#v, found %v; want empty, false", snapshot, found)
	}

	r.RegisterClient("xai-chat", "xai", []*ModelInfo{{
		ID:   modelID,
		Type: "xai",
	}})
	snapshot, found = r.LookupModelExecutionSnapshot(modelID, ModelExecutionImage)
	if !found || !snapshot.Supported || snapshot.ProviderSupports("xai") || !snapshot.ProviderSupports("openai-compatibility") {
		t.Fatalf("xAI chat plus compatibility image registration = %#v, found %v", snapshot, found)
	}

	r.RegisterClient("xai-image", "xai", []*ModelInfo{{
		ID:               modelID,
		Type:             "xai",
		SupportsImageAPI: true,
		ChatDisabled:     true,
	}})
	snapshot, found = r.LookupModelExecutionSnapshot(modelID, ModelExecutionImage)
	if !found || !snapshot.Supported || !snapshot.ProviderSupports("xai") || !snapshot.ProviderSupports("openai-compatibility") {
		t.Fatalf("shared image registration = %#v, found %v", snapshot, found)
	}
	r.SuspendClientModel("xai-image", modelID, "model_not_supported")
	r.SetModelQuotaExceeded("compat-image", modelID)
	snapshot, found = r.LookupModelExecutionSnapshot(modelID, ModelExecutionImage)
	if !found || !snapshot.Supported || !snapshot.ProviderSupports("xai") || !snapshot.ProviderSupports("openai-compatibility") {
		t.Fatalf("suspended and cooling registrations = %#v, found %v; want declared capabilities", snapshot, found)
	}

	r.UnregisterClient("xai-image")
	r.UnregisterClient("xai-chat")
	r.UnregisterClient("compat-image")
	if snapshot, found = r.LookupModelExecutionSnapshot(modelID, ModelExecutionImage); found || snapshot.Supported {
		t.Fatalf("removed model = %#v, found %v; want empty, false", snapshot, found)
	}
}

func registerSharedCatalogTestModel(r *ModelRegistry, imageFirst bool) {
	chat := &ModelInfo{
		ID:                        "shared-catalog-model",
		Object:                    "model",
		OwnedBy:                   "chat-owner",
		Type:                      "openai-compatibility",
		DisplayName:               "Chat Model",
		Description:               "Chat metadata",
		ContextLength:             123456,
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
		Thinking:                  &ThinkingSupport{Levels: []string{"low", "high"}},
	}
	image := &ModelInfo{
		ID:                        "shared-catalog-model",
		Object:                    "model",
		OwnedBy:                   "image-owner",
		Type:                      OpenAIImageModelType,
		DisplayName:               "Image Model",
		Description:               "Image metadata",
		SupportedOutputModalities: []string{"image"},
	}

	registerChat := func() {
		r.RegisterClient("chat-client", "openai-compatibility", []*ModelInfo{chat})
	}
	registerImage := func() {
		r.RegisterClient("image-client", "openai-compatibility", []*ModelInfo{image})
	}
	if imageFirst {
		registerImage()
		registerChat()
		return
	}
	registerChat()
	registerImage()
}

func onlyAvailableCatalogTestModel(t *testing.T, r *ModelRegistry) map[string]any {
	t.Helper()
	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("available models = %#v, want one model", models)
	}
	return models[0]
}
