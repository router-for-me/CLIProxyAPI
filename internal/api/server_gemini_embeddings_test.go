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

type geminiEmbeddingUpstreamCapture struct {
	mu     sync.Mutex
	calls  int
	method string
	path   string
	query  string
	body   []byte
	apiKey string
}

func (c *geminiEmbeddingUpstreamCapture) snapshot() geminiEmbeddingUpstreamCapture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return geminiEmbeddingUpstreamCapture{calls: c.calls, method: c.method, path: c.path, query: c.query, body: append([]byte(nil), c.body...), apiKey: c.apiKey}
}

func newGeminiEmbeddingRouteServer(t *testing.T) (*Server, *geminiEmbeddingUpstreamCapture, string) {
	t.Helper()
	const upstreamBody = `{"embedding":{"values":[0.1,0.2]},"usageMetadata":{"promptTokenCount":3,"totalTokenCount":3}}`
	capture := &geminiEmbeddingUpstreamCapture{}
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
		_, _ = io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(upstream.Close)

	server := newTestServer(t)
	server.handlers.AuthManager.RegisterExecutor(executor.NewGeminiExecutor(server.getConfig()))
	cred := &auth.Auth{
		ID:       "gemini-embedding-route-auth",
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
	embeddingModels := make([]*registry.ModelInfo, 0, 2)
	for _, model := range registry.GetGeminiModels() {
		if model != nil && (model.ID == "gemini-embedding-001" || model.ID == "gemini-embedding-2") {
			embeddingModels = append(embeddingModels, model)
		}
	}
	if len(embeddingModels) != 2 {
		t.Fatalf("expected both embedding models in the Gemini catalog, got %d", len(embeddingModels))
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "gemini", embeddingModels)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(cred.ID) })
	return server, capture, upstreamBody
}

func TestGeminiEmbeddingRouteRequiresAuthentication(t *testing.T) {
	server, capture, _ := newGeminiEmbeddingRouteServer(t)

	for _, tt := range []struct {
		name   string
		header string
	}{
		{name: "missing credentials"},
		{name: "invalid credentials", header: "Bearer wrong-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-embedding-001:embedContent", strings.NewReader(`{"content":{"parts":[{"text":"hi"}]}}`))
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

func TestGeminiEmbeddingRouteRelaysNativePayload(t *testing.T) {
	server, capture, upstreamBody := newGeminiEmbeddingRouteServer(t)

	for _, tt := range []struct {
		name     string
		action   string
		body     string
		wantPath string
	}{
		{
			name:     "embedContent",
			action:   "embedContent",
			body:     `{"content":{"parts":[{"text":"hi"}]}}`,
			wantPath: "/v1beta/models/gemini-embedding-001:embedContent",
		},
		{
			name:     "batchEmbedContents",
			action:   "batchEmbedContents",
			body:     `{"requests":[{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"a"}]}},{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"b"}]}}]}`,
			wantPath: "/v1beta/models/gemini-embedding-001:batchEmbedContents",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capture.mu.Lock()
			capture.calls = 0
			capture.body = nil
			capture.mu.Unlock()

			req := httptest.NewRequest(http.MethodPost, tt.wantPath, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-key")
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if rec.Body.String() != upstreamBody {
				t.Fatalf("downstream body = %q, want upstream body unchanged", rec.Body.String())
			}

			got := capture.snapshot()
			if got.calls != 1 {
				t.Fatalf("upstream calls = %d, want 1", got.calls)
			}
			if got.path != tt.wantPath {
				t.Fatalf("upstream path = %q, want %q", got.path, tt.wantPath)
			}
			if strings.Contains(got.query, "alt=") {
				t.Fatalf("upstream query must not carry alt: %q", got.query)
			}
			if got.apiKey != "gemini-upstream-secret" {
				t.Fatalf("upstream api key = %q, want gemini-upstream-secret", got.apiKey)
			}
			for _, field := range []string{"contents", "safetySettings", "tools", "generationConfig"} {
				if ojson.GetBytes(got.body, field).Exists() {
					t.Fatalf("upstream body must not contain %q: %s", field, got.body)
				}
			}
		})
	}
}

func TestGeminiModelsExposeEmbeddingGenerationMethods(t *testing.T) {
	server, _, _ := newGeminiEmbeddingRouteServer(t)

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
	found := map[string][]string{}
	models.ForEach(func(_, item ojson.Result) bool {
		name := strings.TrimPrefix(item.Get("name").String(), "models/")
		var methods []string
		item.Get("supportedGenerationMethods").ForEach(func(_, method ojson.Result) bool {
			methods = append(methods, method.String())
			return true
		})
		found[name] = methods
		return true
	})
	for _, id := range []string{"gemini-embedding-001", "gemini-embedding-2"} {
		methods, ok := found[id]
		if !ok {
			t.Fatalf("%s missing from model list: %v", id, found)
		}
		if strings.Join(methods, ",") != "embedContent,batchEmbedContents" {
			t.Fatalf("%s supportedGenerationMethods = %v, want embedContent and batchEmbedContents", id, methods)
		}
	}
}
