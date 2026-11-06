package openai

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestOpenAIModelsPreservesModelModalitiesEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clientID := "openai-model-capabilities-e2e-test"
	registry.GetGlobalRegistry().RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:                        "gpt-vision-e2e",
		OwnedBy:                   "openai",
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
		SupportedParameters:       []string{"tools", "reasoning"},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })

	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
	router := gin.New()
	router.GET("/v1/models", handler.OpenAIModels)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/models", nil))
	if recorder.Code != 200 {
		t.Fatalf("GET /v1/models status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode /v1/models response: %v; body=%s", errDecode, recorder.Body.String())
	}
	var model map[string]any
	for _, candidate := range payload.Data {
		if candidate["id"] == "gpt-vision-e2e" {
			model = candidate
			break
		}
	}
	if model == nil {
		t.Fatalf("missing gpt-vision-e2e in /v1/models response: %s", recorder.Body.String())
	}
	if got := model["input_modalities"]; got == nil {
		t.Fatal("input_modalities missing from /v1/models response")
	} else if want := "[\"text\",\"image\"]"; marshalJSON(t, got) != want {
		t.Fatalf("input_modalities = %s, want %s", marshalJSON(t, got), want)
	}
	if got := model["output_modalities"]; got == nil {
		t.Fatal("output_modalities missing from /v1/models response")
	} else if want := "[\"text\"]"; marshalJSON(t, got) != want {
		t.Fatalf("output_modalities = %s, want %s", marshalJSON(t, got), want)
	}
	if got := model["supported_parameters"]; got == nil {
		t.Fatal("supported_parameters missing from /v1/models response")
	} else if want := "[\"tools\",\"reasoning\"]"; marshalJSON(t, got) != want {
		t.Fatalf("supported_parameters = %s, want %s", marshalJSON(t, got), want)
	}
	if _, exists := model["metadata_model_id"]; exists {
		t.Fatal("internal metadata_model_id leaked into /v1/models response")
	}
}

func marshalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal JSON value: %v", errMarshal)
	}
	return string(raw)
}
