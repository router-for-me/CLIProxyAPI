package amp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing/testutil"
	"github.com/stretchr/testify/assert"
)

// Characterization tests for fallback_handlers.go using testutil recorders
// These tests capture existing behavior before refactoring to routing layer

func TestCharacterization_LocalProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Register a mock provider for the test model
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("char-test-local", "anthropic", []*registry.ModelInfo{
		{ID: "test-model-local"},
	})
	defer reg.UnregisterClient("char-test-local")

	// Setup recorders
	proxyRecorder := testutil.NewFakeProxyRecorder()
	handlerRecorder := testutil.NewFakeHandlerRecorder()

	// Create gin context
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model": "test-model-local", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/anthropic/v1/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	// Create fallback handler with proxy recorder
	// Create a test server to act as the proxy target
	proxyServer := httptest.NewServer(proxyRecorder.ToHandler())
	defer proxyServer.Close()

	fh := NewFallbackHandler(func() *httputil.ReverseProxy {
		// Create a reverse proxy that forwards to our test server
		targetURL, _ := url.Parse(proxyServer.URL)
		return httputil.NewSingleHostReverseProxy(targetURL)
	})

	// Execute
	wrapped := fh.WrapHandler(handlerRecorder.GinHandler())
	wrapped(c)

	// Assert: proxy NOT called
	assert.False(t, proxyRecorder.Called, "proxy should NOT be called for local provider")

	// Assert: local handler called once
	assert.True(t, handlerRecorder.WasCalled(), "local handler should be called")
	assert.Equal(t, 1, handlerRecorder.GetCallCount(), "local handler should be called exactly once")

	// Assert: request body model unchanged
	assert.Contains(t, string(handlerRecorder.RequestBody), "test-model-local", "request body model should be unchanged")
}

func TestCharacterization_ModelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Register a mock provider for the TARGET model (the mapped-to model)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("char-test-mapped", "openai", []*registry.ModelInfo{
		{ID: "gpt-4-local"},
	})
	defer reg.UnregisterClient("char-test-mapped")

	// Setup recorders
	proxyRecorder := testutil.NewFakeProxyRecorder()
	handlerRecorder := testutil.NewFakeHandlerRecorder()

	// Create model mapper with a mapping
	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gpt-4-turbo", To: "gpt-4-local"},
	})

	// Create gin context
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Request with original model that gets mapped
	body := `{"model": "gpt-4-turbo", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/openai/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	// Create fallback handler with mapper
	proxyServer := httptest.NewServer(proxyRecorder.ToHandler())
	defer proxyServer.Close()

	fh := NewFallbackHandlerWithMapper(func() *httputil.ReverseProxy {
		targetURL, _ := url.Parse(proxyServer.URL)
		return httputil.NewSingleHostReverseProxy(targetURL)
	}, mapper, func() bool { return false })

	// Execute - use handler that returns model in response for rewriter to work
	wrapped := fh.WrapHandler(handlerRecorder.GinHandlerWithModel())
	wrapped(c)

	// Assert: proxy NOT called
	assert.False(t, proxyRecorder.Called, "proxy should NOT be called for model mapping")

	// Assert: local handler called once
	assert.True(t, handlerRecorder.WasCalled(), "local handler should be called")
	assert.Equal(t, 1, handlerRecorder.GetCallCount(), "local handler should be called exactly once")

	// Assert: request body model was rewritten to mapped model
	assert.Contains(t, string(handlerRecorder.RequestBody), "gpt-4-local", "request body model should be rewritten to mapped model")
	assert.NotContains(t, string(handlerRecorder.RequestBody), "gpt-4-turbo", "request body should NOT contain original model")

	// Assert: context has mapped_model key set
	mappedModel, exists := handlerRecorder.GetContextKey("mapped_model")
	assert.True(t, exists, "context should have mapped_model key")
	assert.Equal(t, "gpt-4-local", mappedModel, "mapped_model should be the target model")

	// Assert: response body model rewritten back to original
	// The response writer should rewrite model names in the response
	responseBody := w.Body.String()
	assert.Contains(t, responseBody, "gpt-4-turbo", "response should have original model name")
}

func TestCharacterization_AmpCreditsProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Setup recorders - NO local provider registered, NO mapping configured
	proxyRecorder := testutil.NewFakeProxyRecorder()
	handlerRecorder := testutil.NewFakeHandlerRecorder()

	// Create gin context with CloseNotifier support (required for ReverseProxy)
	w := testutil.NewCloseNotifierRecorder()
	c, _ := gin.CreateTestContext(w)

	// Request with a model that has no local provider and no mapping
	body := `{"model": "unknown-model-no-provider", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/openai/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	// Create fallback handler
	proxyServer := httptest.NewServer(proxyRecorder.ToHandler())
	defer proxyServer.Close()

	fh := NewFallbackHandler(func() *httputil.ReverseProxy {
		targetURL, _ := url.Parse(proxyServer.URL)
		return httputil.NewSingleHostReverseProxy(targetURL)
	})

	// Execute
	wrapped := fh.WrapHandler(handlerRecorder.GinHandler())
	wrapped(c)

	// Assert: proxy called once
	assert.True(t, proxyRecorder.Called, "proxy should be called when no local provider and no mapping")
	assert.Equal(t, 1, proxyRecorder.GetCallCount(), "proxy should be called exactly once")

	// Assert: local handler NOT called
	assert.False(t, handlerRecorder.WasCalled(), "local handler should NOT be called when falling back to proxy")

	// Assert: body forwarded to proxy is original (no rewrite)
	assert.Contains(t, string(proxyRecorder.RequestBody), "unknown-model-no-provider", "request body model should be unchanged when proxying")
}

func TestCharacterization_BodyRestore(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Register a mock provider for the test model
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("char-test-body", "anthropic", []*registry.ModelInfo{
		{ID: "test-model-body"},
	})
	defer reg.UnregisterClient("char-test-body")

	// Setup recorders
	proxyRecorder := testutil.NewFakeProxyRecorder()
	handlerRecorder := testutil.NewFakeHandlerRecorder()

	// Create gin context
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Create a complex request body that will be read by the wrapper for model extraction
	originalBody := `{"model": "test-model-body", "messages": [{"role": "user", "content": "hello"}], "temperature": 0.7, "stream": true}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/anthropic/v1/messages", bytes.NewReader([]byte(originalBody)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	// Create fallback handler with proxy recorder
	proxyServer := httptest.NewServer(proxyRecorder.ToHandler())
	defer proxyServer.Close()

	fh := NewFallbackHandler(func() *httputil.ReverseProxy {
		targetURL, _ := url.Parse(proxyServer.URL)
		return httputil.NewSingleHostReverseProxy(targetURL)
	})

	// Execute
	wrapped := fh.WrapHandler(handlerRecorder.GinHandler())
	wrapped(c)

	// Assert: local handler called (not proxy, since we have a local provider)
	assert.True(t, handlerRecorder.WasCalled(), "local handler should be called")
	assert.False(t, proxyRecorder.Called, "proxy should NOT be called for local provider")

	// Assert: handler receives complete original body
	// This verifies that the body was properly restored after the wrapper read it for model extraction
	assert.Equal(t, originalBody, string(handlerRecorder.RequestBody), "handler should receive complete original body after wrapper reads it for model extraction")
}

// TestCharacterization_GeminiV1Beta1_PostModels tests that POST requests with /models/ path use Gemini bridge handler
// This is a characterization test for the route gating logic in routes.go
func TestCharacterization_GeminiV1Beta1_PostModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Register a mock provider for the test model (Gemini format uses path-based model extraction)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("char-test-gemini", "google", []*registry.ModelInfo{
		{ID: "gemini-pro"},
	})
	defer reg.UnregisterClient("char-test-gemini")

	// Setup recorders
	proxyRecorder := testutil.NewFakeProxyRecorder()
	handlerRecorder := testutil.NewFakeHandlerRecorder()

	// Create a test server for the proxy
	proxyServer := httptest.NewServer(proxyRecorder.ToHandler())
	defer proxyServer.Close()

	// Create fallback handler
	fh := NewFallbackHandler(func() *httputil.ReverseProxy {
		targetURL, _ := url.Parse(proxyServer.URL)
		return httputil.NewSingleHostReverseProxy(targetURL)
	})

	// Create the Gemini bridge handler (simulating what routes.go does)
	geminiBridge := createGeminiBridgeHandler(handlerRecorder.GinHandler())
	geminiV1Beta1Handler := fh.WrapHandler(geminiBridge)

	// Create router with the same gating logic as routes.go
	r := gin.New()
	r.Any("/api/provider/google/v1beta1/*path", func(c *gin.Context) {
		if c.Request.Method == "POST" {
			if path := c.Param("path"); strings.Contains(path, "/models/") {
				// POST with /models/ path -> use Gemini bridge with fallback handler
				geminiV1Beta1Handler(c)
				return
			}
		}
		// Non-POST or no /models/ in path -> proxy upstream
		proxyRecorder.ServeHTTP(c.Writer, c.Request)
	})

	// Execute: POST request with /models/ in path
	body := `{"contents": [{"role": "user", "parts": [{"text": "hello"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/google/v1beta1/publishers/google/models/gemini-pro:generateContent", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Assert: local Gemini handler called
	assert.True(t, handlerRecorder.WasCalled(), "local Gemini handler should be called for POST /models/")

	// Assert: proxy NOT called
	assert.False(t, proxyRecorder.Called, "proxy should NOT be called for POST /models/ path")
}

// TestCharacterization_GeminiV1Beta1_GetProxies tests that GET requests to Gemini v1beta1 always use proxy
// This is a characterization test for the route gating logic in routes.go
func TestCharacterization_GeminiV1Beta1_GetProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Setup recorders
	proxyRecorder := testutil.NewFakeProxyRecorder()
	handlerRecorder := testutil.NewFakeHandlerRecorder()

	// Create a test server for the proxy
	proxyServer := httptest.NewServer(proxyRecorder.ToHandler())
	defer proxyServer.Close()

	// Create fallback handler
	fh := NewFallbackHandler(func() *httputil.ReverseProxy {
		targetURL, _ := url.Parse(proxyServer.URL)
		return httputil.NewSingleHostReverseProxy(targetURL)
	})

	// Create the Gemini bridge handler
	geminiBridge := createGeminiBridgeHandler(handlerRecorder.GinHandler())
	geminiV1Beta1Handler := fh.WrapHandler(geminiBridge)

	// Create router with the same gating logic as routes.go
	r := gin.New()
	r.Any("/api/provider/google/v1beta1/*path", func(c *gin.Context) {
		if c.Request.Method == "POST" {
			if path := c.Param("path"); strings.Contains(path, "/models/") {
				geminiV1Beta1Handler(c)
				return
			}
		}
		proxyRecorder.ServeHTTP(c.Writer, c.Request)
	})

	// Execute: GET request (even with /models/ in path)
	req := httptest.NewRequest(http.MethodGet, "/api/provider/google/v1beta1/publishers/google/models/gemini-pro", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Assert: proxy called
	assert.True(t, proxyRecorder.Called, "proxy should be called for GET requests")
	assert.Equal(t, 1, proxyRecorder.GetCallCount(), "proxy should be called exactly once")

	// Assert: local handler NOT called
	assert.False(t, handlerRecorder.WasCalled(), "local handler should NOT be called for GET requests")
}
