package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestDecodeHomeModelsEmptyCatalog(t *testing.T) {
	for _, raw := range []string{"{}", " { } ", `{"claude":[],"codex-pro":[]}`} {
		entries, errDecode := decodeHomeModels([]byte(raw))
		if errDecode != nil || entries == nil || len(entries) != 0 {
			t.Errorf("decodeHomeModels(%q) = %v, %v, want empty list", raw, entries, errDecode)
		}
	}
	for _, raw := range []string{
		"", "null", "[]", "{bad}", `{"claude":[{}]}`, `{"claude":null}`, `{"error":null}`,
		`{"error":{"type":"internal_error","message":"unavailable"}}`,
		`{"error":[]}`, `{"error":[],"claude":[]}`,
		`{"error":[{"id":"not-a-model"}]}`, `{"error":[],"claude":[{"id":"model-a"}]}`,
	} {
		if _, errDecode := decodeHomeModels([]byte(raw)); errDecode == nil {
			t.Errorf("decodeHomeModels(%q) accepted malformed or error payload", raw)
		}
	}
}

func TestHomeEmptyModelCatalogResponses(t *testing.T) {
	server := newTestServer(t)
	server.cfg.Home.Enabled = true
	engine := gin.New()
	engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
	engine.GET("/v1beta/models", server.geminiModelsHandler(nil))
	engine.GET("/v1beta/models/*action", server.geminiGetHandler(nil))
	client := newHomeCatalogClient(t, "{}")
	previousHome := home.Current()
	home.SetCurrent(client)
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	registry.GetGlobalRegistry().RegisterClient("empty-catalog-local", "codex", []*registry.ModelInfo{{ID: "local-only-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("empty-catalog-local") })

	tests := []struct {
		name    string
		path    string
		headers map[string]string
		field   string
	}{
		{name: "openai", path: "/v1/models", field: "data"},
		{name: "claude", path: "/v1/models", headers: map[string]string{"Anthropic-Version": "2023-06-01"}, field: "data"},
		{name: "codex", path: "/v1/models?client_version=0.153.4", field: "models"},
		{name: "cpa", path: "/v1/models?client_version=cpa", field: "models"},
		{name: "gemini", path: "/v1beta/models", field: "models"},
		{name: "grok", path: "/v1/models", headers: map[string]string{"User-Agent": "grok-shell"}, field: "data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer scoped-key")
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
			}
			var payload map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if string(payload[test.field]) != "[]" {
				t.Fatalf("%s = %s, want []; body=%s", test.field, payload[test.field], recorder.Body.String())
			}
		})
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1beta/models/inaccessible-model", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("inaccessible model status = %d, want 404; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHomeModelCatalogErrorsRemainErrors(t *testing.T) {
	server := newTestServer(t)
	server.cfg.Home.Enabled = true
	engine := gin.New()
	engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
	for _, test := range []struct {
		name    string
		payload string
		status  int
	}{
		{name: "missing credentials", payload: `{"error":{"type":"no_credentials","message":"Missing API key"}}`, status: http.StatusUnauthorized},
		{name: "invalid credentials", payload: `{"error":{"type":"invalid_credential","message":"Invalid API key"}}`, status: http.StatusUnauthorized},
		{name: "policy failure", payload: `{"error":{"type":"internal_error","message":"Model access service error"}}`, status: http.StatusBadGateway},
		{name: "empty array error", payload: `{"error":[]}`, status: http.StatusBadGateway},
		{name: "error alongside empty section", payload: `{"error":[],"claude":[]}`, status: http.StatusBadGateway},
		{name: "error containing model-shaped records", payload: `{"error":[{"id":"not-a-model"}]}`, status: http.StatusBadGateway},
		{name: "error alongside models", payload: `{"error":[],"claude":[{"id":"model-a"}]}`, status: http.StatusBadGateway},
		{name: "empty response", payload: "", status: http.StatusBadGateway},
		{name: "malformed JSON", payload: "{bad}", status: http.StatusBadGateway},
		{name: "null catalog", payload: "null", status: http.StatusBadGateway},
		{name: "null section", payload: `{"claude":null}`, status: http.StatusBadGateway},
		{name: "missing model IDs", payload: `{"claude":[{}]}`, status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newHomeCatalogClient(t, test.payload)
			previousHome := home.Current()
			home.SetCurrent(client)
			t.Cleanup(func() { home.SetCurrent(previousHome) })
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			var payload map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if _, exists := payload["error"]; !exists {
				t.Fatalf("expected error response; body=%s", recorder.Body.String())
			}
		})
	}
}
