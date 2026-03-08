package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type embeddingCaptureExecutor struct {
	lastURL    string
	lastMethod string
	lastBody   string
	respBody   string
}

func (e *embeddingCaptureExecutor) Identifier() string { return "gemini" }

func (e *embeddingCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *embeddingCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (<-chan coreexecutor.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func (e *embeddingCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *embeddingCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *embeddingCaptureExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	e.lastURL = req.URL.String()
	e.lastMethod = req.Method
	e.lastBody = string(body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(e.respBody)),
	}, nil
}

func registerEmbeddingTestAuth(t *testing.T, executor *embeddingCaptureExecutor) (*coreauth.Manager, string) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "gemini-embed-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "gemini-embedding-001"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	return manager, auth.ID
}

func TestOpenAIEmbeddingsSingleInputUsesGeminiEmbedContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &embeddingCaptureExecutor{
		respBody: `{"embedding":{"values":[0.1,0.2,0.3]}}`,
	}
	manager, _ := registerEmbeddingTestAuth(t, executor)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/embeddings", h.Embeddings)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"gemini-embedding-001","input":"hello","dimensions":3}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.lastMethod != http.MethodPost {
		t.Fatalf("method = %q, want %q", executor.lastMethod, http.MethodPost)
	}
	if !strings.Contains(executor.lastURL, "/v1beta/models/gemini-embedding-001:embedContent") {
		t.Fatalf("url = %q, want embedContent route", executor.lastURL)
	}
	if got := gjson.Get(executor.lastBody, "content.parts.0.text").String(); got != "hello" {
		t.Fatalf("request text = %q, want %q", got, "hello")
	}
	if got := gjson.Get(executor.lastBody, "outputDimensionality").Int(); got != 3 {
		t.Fatalf("outputDimensionality = %d, want 3", got)
	}
	if got := gjson.Get(resp.Body.String(), "data.0.embedding.1").Float(); got != 0.2 {
		t.Fatalf("response embedding = %v, want 0.2 at index 1", got)
	}
}

func TestOpenAIEmbeddingsBatchInputUsesGeminiBatchEmbedContents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &embeddingCaptureExecutor{
		respBody: `{"embeddings":[{"values":[1,2]},{"values":[3,4]}]}`,
	}
	manager, _ := registerEmbeddingTestAuth(t, executor)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/embeddings", h.Embeddings)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"gemini-embedding-001","input":["alpha","beta"]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if !strings.Contains(executor.lastURL, "/v1beta/models/gemini-embedding-001:batchEmbedContents") {
		t.Fatalf("url = %q, want batchEmbedContents route", executor.lastURL)
	}
	if got := gjson.Get(executor.lastBody, "requests.0.content.parts.0.text").String(); got != "alpha" {
		t.Fatalf("first request text = %q, want %q", got, "alpha")
	}
	if got := gjson.Get(executor.lastBody, "requests.1.content.parts.0.text").String(); got != "beta" {
		t.Fatalf("second request text = %q, want %q", got, "beta")
	}
	if got := len(gjson.Get(resp.Body.String(), "data").Array()); got != 2 {
		t.Fatalf("response embeddings count = %d, want 2", got)
	}
}

func TestOpenAIEmbeddingsAcceptsBase64EncodingFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &embeddingCaptureExecutor{
		respBody: `{"embedding":{"values":[0.5,0.25]}}`,
	}
	manager, _ := registerEmbeddingTestAuth(t, executor)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/embeddings", h.Embeddings)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"gemini-embedding-001","input":"hello","encoding_format":"base64"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := gjson.Get(resp.Body.String(), "data.0.embedding.0").Float(); got != 0.5 {
		t.Fatalf("response embedding = %v, want 0.5 at index 0", got)
	}
}

func TestOpenAIModelsIncludesGeminiEmbeddingWhenGeminiAuthExists(t *testing.T) {
	executor := &embeddingCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "gemini-model-list-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	models := h.Models()

	found := false
	for _, model := range models {
		if id, _ := model["id"].(string); id == "gemini-embedding-001" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("gemini-embedding-001 not found in models list")
	}
}
