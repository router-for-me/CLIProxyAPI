package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/client/grokbuild"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestModelsDispatchByGrokShellUserAgent(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-grok-shell-model-list"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: "grok-shell-openai-model", DisplayName: "Grok Shell Model", ContextLength: 256000, Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}},
	})
	modelRegistry.RegisterClient(clientID+"-claude", "claude", []*registry.ModelInfo{
		{ID: "grok-shell-claude-model", DisplayName: "Claude Catalog Model", ContextLength: 200000},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
		modelRegistry.UnregisterClient(clientID + "-claude")
	})

	server := newTestServer(t)
	for _, userAgent := range []string{
		"grok-shell/0.2.119 (macos; aarch64)",
		"grok-pager/0.2.119 grok-shell/0.2.119 (macos; aarch64)",
	} {
		t.Run(userAgent, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://proxy.example.test/v1/models?client_version", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("User-Agent", userAgent)
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Object string `json:"object"`
				Data   []struct {
					ID               string `json:"id"`
					Model            string `json:"model"`
					Name             string `json:"name"`
					ContextWindow    int    `json:"context_window"`
					APIBackend       string `json:"api_backend"`
					SupportedInAPI   bool   `json:"supported_in_api"`
					ReasoningEfforts []struct {
						Value string `json:"value"`
					} `json:"reasoning_efforts"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
			}
			if response.Object != "list" {
				t.Fatalf("object = %q, want list", response.Object)
			}
			var foundOpenAI, foundClaude bool
			for _, model := range response.Data {
				switch model.ID {
				case "grok-shell-openai-model":
					foundOpenAI = true
					if model.Model != model.ID || model.Name != "Grok Shell Model" || model.ContextWindow != 256000 {
						t.Fatalf("OpenAI model mapping = %#v", model)
					}
					if model.APIBackend != "responses" || !model.SupportedInAPI {
						t.Fatalf("OpenAI model routing fields = %#v", model)
					}
					if len(model.ReasoningEfforts) != 1 || model.ReasoningEfforts[0].Value != "high" {
						t.Fatalf("OpenAI reasoning efforts = %#v", model.ReasoningEfforts)
					}
				case "grok-shell-claude-model":
					foundClaude = true
					if model.Model != model.ID || model.Name != "Claude Catalog Model" || model.ContextWindow != 200000 {
						t.Fatalf("Claude model mapping = %#v", model)
					}
					if len(model.ReasoningEfforts) != 0 {
						t.Fatalf("Claude reasoning efforts = %#v, want none", model.ReasoningEfforts)
					}
				}
			}
			if !foundOpenAI {
				t.Fatalf("registered OpenAI Grok model missing: %s", recorder.Body.String())
			}
			if !foundClaude {
				t.Fatalf("registered Claude Grok model missing: %s", recorder.Body.String())
			}
		})
	}
}

func TestModelsDispatchKeepsOrdinaryOpenAIResponse(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-ordinary-model-list-after-grok"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "ordinary-model"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "curl/8.7.1")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	var response struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Object != "list" {
		t.Fatalf("object = %q, want list", response.Object)
	}
	found := false
	for _, model := range response.Data {
		if _, exists := model["api_backend"]; exists {
			t.Fatalf("ordinary response contains Grok field: %#v", model)
		}
		if id, ok := model["id"].(string); ok && id == "ordinary-model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("registered ordinary model missing: %s", recorder.Body.String())
	}
}

func TestModelsSurfacesContextLengthForAutoDiscoveringClients(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-context-length-models"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: "ctx-model", ContextLength: 372000, InputTokenLimit: 500000, MaxContextLength: 368000, MaxCompletionTokens: 128000, OutputTokenLimit: 64000, Created: 1770912000, OwnedBy: "openai"},
		{ID: "input-output-fallback-model", InputTokenLimit: 1048576, OutputTokenLimit: 65536},
		{ID: "no-context-model"},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "curl/8.7.1")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Models that carry context metadata must now surface it on the plain
	// /v1/models response (no client_version) so auto-discovering agents can
	// set correct per-model limits.
	var withCtx, noCtx int
	for _, model := range response.Data {
		id, _ := model["id"].(string)
		switch id {
		case "ctx-model":
			withCtx++
			if got := model["context_length"]; got != float64(372000) {
				t.Fatalf("ctx-model context_length = %#v, want 372000 (body=%s)", got, recorder.Body.String())
			}
			if got := model["max_context_length"]; got != float64(368000) {
				t.Fatalf("ctx-model max_context_length = %#v, want 368000", got)
			}
			if got := model["max_completion_tokens"]; got != float64(128000) {
				t.Fatalf("ctx-model max_completion_tokens = %#v, want 128000", got)
			}
			if got, ok := model["created"].(float64); !ok || got != 1770912000 {
				t.Fatalf("ctx-model created = %#v, want 1770912000", model["created"])
			}
			if got := model["owned_by"]; got != "openai" {
				t.Fatalf("ctx-model owned_by = %#v, want openai", got)
			}
		case "input-output-fallback-model":
			withCtx++
			if got := model["context_length"]; got != float64(1048576) {
				t.Fatalf("input-output-fallback-model context_length = %#v, want 1048576", got)
			}
			if got := model["max_completion_tokens"]; got != float64(65536) {
				t.Fatalf("input-output-fallback-model max_completion_tokens = %#v, want 65536", got)
			}
		case "no-context-model":
			noCtx++
			// Zero-context models must not gain a context_length key.
			if _, exists := model["context_length"]; exists {
				t.Fatalf("no-context-model leaked empty context_length: %#v", model)
			}
		}
	}
	if withCtx != 2 || noCtx != 1 {
		t.Fatalf("withCtx=%d noCtx=%d, want 2 withCtx and 1 noCtx (body=%s)", withCtx, noCtx, recorder.Body.String())
	}
}

func TestGrokHomeModelAdapterOmitsReasoning(t *testing.T) {
	models := grokModelsFromHomeEntries([]homeModelEntry{
		{id: "home-model", displayName: "Home Model", contextLength: 1234},
		{id: "home-model-without-context", displayName: "No Context Model"},
	})
	if len(models) != 2 {
		t.Fatalf("Home model count = %d, want 2", len(models))
	}
	if models[0].ID != "home-model" || models[0].DisplayName != "Home Model" || models[0].ContextLength != 1234 {
		t.Fatalf("Home model adapter = %#v", models[0])
	}
	if models[1].ID != "home-model-without-context" || models[1].DisplayName != "No Context Model" || models[1].ContextLength != 0 {
		t.Fatalf("Home zero-context adapter = %#v", models[1])
	}

	response := grokbuild.BuildResponse(models)
	if len(response.Data) != 2 || response.Data[0].ReasoningEfforts != nil || response.Data[1].ReasoningEfforts != nil {
		t.Fatalf("Home reasoning efforts = %#v", response.Data)
	}

	wire, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		t.Fatalf("marshal Home response: %v", errMarshal)
	}
	var wireResponse struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(wire, &wireResponse); errUnmarshal != nil {
		t.Fatalf("decode Home response JSON: %v; body=%s", errUnmarshal, wire)
	}
	if len(wireResponse.Data) != 2 {
		t.Fatalf("wire Home model count = %d, want 2; body=%s", len(wireResponse.Data), wire)
	}
	contextWindow, exists := wireResponse.Data[0]["context_window"]
	if !exists {
		t.Fatalf("Home model context_window missing from wire response: %s", wire)
	}
	var gotContextWindow int
	if errDecode := json.Unmarshal(contextWindow, &gotContextWindow); errDecode != nil {
		t.Fatalf("decode Home context_window: %v", errDecode)
	}
	if gotContextWindow != 1234 {
		t.Fatalf("Home context_window = %d, want 1234", gotContextWindow)
	}
	if _, exists := wireResponse.Data[0]["reasoning_efforts"]; exists {
		t.Fatalf("Home model contains omitted reasoning_efforts: %s", wire)
	}
	if _, exists := wireResponse.Data[1]["context_window"]; exists {
		t.Fatalf("zero-context Home model contains omitted context_window: %s", wire)
	}
	if _, exists := wireResponse.Data[1]["reasoning_efforts"]; exists {
		t.Fatalf("zero-context Home model contains omitted reasoning_efforts: %s", wire)
	}
}

func TestGrokModelsPreferHomeOverRegistry(t *testing.T) {
	previousHome := home.Current()
	home.ClearCurrent()
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-grok-home-source"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "local-only-model"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	server := newTestServer(t)
	server.cfg.Home.Enabled = true
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "grok-shell/0.2.119")
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = req
	server.handleGrokModels(ginContext)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "home control center unavailable") {
		t.Fatalf("Home failure response missing expected error: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "local-only-model") {
		t.Fatalf("Home failure response leaked local registry model: %s", recorder.Body.String())
	}
}
