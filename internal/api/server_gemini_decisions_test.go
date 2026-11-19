package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type decisionsUpstreamCapture struct {
	mu     sync.Mutex
	calls  int
	method string
	path   string
	query  string
	body   []byte
	apiKey string
}

func (c *decisionsUpstreamCapture) snapshot() decisionsUpstreamCapture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return decisionsUpstreamCapture{
		calls:  c.calls,
		method: c.method,
		path:   c.path,
		query:  c.query,
		body:   append([]byte(nil), c.body...),
		apiKey: c.apiKey,
	}
}

func (c *decisionsUpstreamCapture) reset() {
	c.mu.Lock()
	c.calls = 0
	c.body = nil
	c.mu.Unlock()
}

const decisionsUpstreamBody = `{"answers":{"is_damaged":{"type":"noul","noul":0.99,"confidence":0.99}},"usage":{"total_input_tokens":100,"input_tokens_by_modality":[{"modality":"text","tokens":100}],"total_output_tokens":0,"total_tokens":100}}`

func newDecisionsRouteServer(t *testing.T) (*Server, *decisionsUpstreamCapture) {
	t.Helper()
	capture := &decisionsUpstreamCapture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.calls++
		capture.method = r.Method
		capture.path = r.URL.Path
		capture.query = r.URL.RawQuery
		capture.body = append([]byte(nil), body...)
		capture.apiKey = r.Header.Get("x-goog-api-key")
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, decisionsUpstreamBody)
	}))
	t.Cleanup(upstream.Close)

	server := newTestServer(t)
	server.handlers.AuthManager.RegisterExecutor(executor.NewGeminiExecutor(server.getConfig()))
	cred := &auth.Auth{
		ID:       "gemini-decisions-route-auth",
		Provider: "gemini",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "gemini-upstream-secret",
			"base_url": upstream.URL,
		},
	}
	if _, errRegister := server.handlers.AuthManager.Register(t.Context(), cred); errRegister != nil {
		t.Fatalf("register gemini credential: %v", errRegister)
	}
	decisionModels := make([]*registry.ModelInfo, 0, 1)
	for _, model := range registry.GetGeminiModels() {
		if model != nil && model.ID == "gemini-decision-1" {
			decisionModels = append(decisionModels, model)
		}
	}
	if len(decisionModels) != 1 {
		t.Fatalf("expected gemini-decision-1 in the Gemini catalog, got %d", len(decisionModels))
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "gemini", decisionModels)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(cred.ID) })
	return server, capture
}

func TestDecisionsRouteRequiresAuthentication(t *testing.T) {
	server, capture := newDecisionsRouteServer(t)

	for _, tt := range []struct {
		name   string
		header string
	}{
		{name: "missing credentials"},
		{name: "invalid credentials", header: "Bearer wrong-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1beta/decisions", strings.NewReader(`{"model":"gemini-decision-1","input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`))
			req.Header.Set("Content-Type", "application/json")
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
			if got := capture.snapshot(); got.calls != 0 {
				t.Fatalf("upstream calls = %d, want 0", got.calls)
			}
		})
	}
}

func TestDecisionsRouteRelaysNativePayload(t *testing.T) {
	server, capture := newDecisionsRouteServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/decisions", strings.NewReader(`{"model":"gemini-decision-1","input":"The mug arrived cracked.","questions":{"is_damaged":{"type":"noul","instructions":"Did it arrive damaged?"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if rec.Body.String() != decisionsUpstreamBody {
		t.Fatalf("downstream body = %q, want upstream body unchanged", rec.Body.String())
	}

	got := capture.snapshot()
	if got.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", got.calls)
	}
	if got.path != "/v1beta/decisions" {
		t.Fatalf("upstream path = %q, want /v1beta/decisions", got.path)
	}
	if got.query != "" {
		t.Fatalf("upstream query = %q, want empty (no alt)", got.query)
	}
	if got.apiKey != "gemini-upstream-secret" {
		t.Fatalf("upstream api key = %q, want gemini-upstream-secret", got.apiKey)
	}
	if model := ojson.GetBytes(got.body, "model").String(); model != "gemini-decision-1" {
		t.Fatalf("upstream model = %q, want bare gemini-decision-1", model)
	}
	for _, field := range []string{"contents", "safetySettings", "tools", "generationConfig"} {
		if ojson.GetBytes(got.body, field).Exists() {
			t.Fatalf("upstream body must not contain %q: %s", field, got.body)
		}
	}
}

func TestDecisionsRouteRejectsStreamingAndMissingModel(t *testing.T) {
	server, capture := newDecisionsRouteServer(t)

	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "stream true", body: `{"model":"gemini-decision-1","input":"hi","questions":{"q":{"type":"noul"}},"stream":true}`},
		{name: "missing model", body: `{"input":"hi","questions":{"q":{"type":"noul"}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capture.reset()
			req := httptest.NewRequest(http.MethodPost, "/v1beta/decisions", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-key")
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if got := capture.snapshot(); got.calls != 0 {
				t.Fatalf("upstream calls = %d, want 0", got.calls)
			}
		})
	}
}

func TestDecisionsModelIsListedWithDecisionsMethod(t *testing.T) {
	server, _ := newDecisionsRouteServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	models := ojson.GetBytes(rec.Body.Bytes(), "models")
	if !models.IsArray() {
		t.Fatalf("response has no models array: %s", rec.Body.String())
	}
	var methods []string
	models.ForEach(func(_, item ojson.Result) bool {
		if strings.TrimPrefix(item.Get("name").String(), "models/") == "gemini-decision-1" {
			item.Get("supportedGenerationMethods").ForEach(func(_, method ojson.Result) bool {
				methods = append(methods, method.String())
				return true
			})
		}
		return true
	})
	if len(methods) != 1 || methods[0] != "decisions" {
		t.Fatalf("gemini-decision-1 supportedGenerationMethods = %v, want [decisions]", methods)
	}
}

func TestDecisionsRouteConsecutiveCallsDoNotFail(t *testing.T) {
	server, capture := newDecisionsRouteServer(t)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1beta/decisions", strings.NewReader(`{"model":"gemini-decision-1","input":"hi","questions":{"q":{"type":"noul","instructions":"ok?"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")
		rec := httptest.NewRecorder()
		server.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d, want %d; body=%s", i, rec.Code, http.StatusOK, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "total_output_tokens") {
			t.Fatalf("call %d: body = %s, want upstream usage preserved", i, rec.Body.String())
		}
	}
	if got := capture.snapshot(); got.calls != 3 {
		t.Fatalf("upstream calls = %d, want 3", got.calls)
	}
}
