package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestOpenAIModelsCapabilitiesReturnsVersionedProviderRows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "models-capabilities-handler-test-client"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{{
		ID:                        "factory/test-capability-model",
		ContextLength:             1050000,
		MaxCompletionTokens:       128000,
		SupportedParameters:       []string{"tools"},
		UnsupportedParameters:     []string{"temperature"},
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
		Thinking:                  &registry.ThinkingSupport{Levels: []string{"low", "high"}},
	}})
	defer modelRegistry.UnregisterClient(clientID)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models?capabilities=true", nil)
	(&OpenAIAPIHandler{}).OpenAIModels(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Object        string                     `json:"object"`
		SchemaVersion int                        `json:"schema_version"`
		Data          []registry.ModelCapability `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Object != "model_capability_list" || response.SchemaVersion != 1 {
		t.Fatalf("response header = object %q schema %d", response.Object, response.SchemaVersion)
	}
	for _, model := range response.Data {
		if model.ID != "factory/test-capability-model" || model.Provider != "factory" {
			continue
		}
		if model.ContextWindow == nil || *model.ContextWindow != 1050000 || model.MaxOutputTokens == nil || *model.MaxOutputTokens != 128000 {
			t.Fatalf("capability row has wrong limits: %#v", model)
		}
		return
	}
	t.Fatalf("factory capability row missing: %#v", response.Data)
}

func TestModelCapabilitiesV1FixtureDefinesSharedAndUnknownSemantics(t *testing.T) {
	raw, err := os.ReadFile("testdata/model_capabilities_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Object        string                     `json:"object"`
		SchemaVersion int                        `json:"schema_version"`
		Data          []registry.ModelCapability `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if response.Object != "model_capability_list" || response.SchemaVersion != 1 {
		t.Fatalf("fixture header = object %q schema %d", response.Object, response.SchemaVersion)
	}
	sharedProviders := map[string]bool{}
	unknownFound := false
	for _, model := range response.Data {
		if model.ID == "shared/example-model" {
			sharedProviders[model.Provider] = true
		}
		if model.ID == "unknown/example-model" {
			unknownFound = model.ContextWindow == nil && model.MaxOutputTokens == nil && model.InputModalities == nil && model.OutputModalities == nil && model.Reasoning == nil && model.SupportedParameters == nil && model.UnsupportedParameters == nil
		}
	}
	if !sharedProviders["provider-a"] || !sharedProviders["provider-b"] {
		t.Fatalf("fixture must represent shared IDs as separate provider rows: %#v", sharedProviders)
	}
	if !unknownFound {
		t.Fatal("fixture must represent unknown capability fields as null")
	}
}
