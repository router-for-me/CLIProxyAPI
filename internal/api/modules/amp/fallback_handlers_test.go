package amp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/stretchr/testify/assert"
)

// Characterization tests for fallback_handlers.go
// These tests capture existing behavior before refactoring to routing layer

func TestFallbackHandler_WrapHandler_LocalProvider_NoMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Setup: model that has local providers (gemini-2.5-pro is registered)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/anthropic/v1/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	// Handler that should be called (not proxy)
	handlerCalled := false
	handler := func(c *gin.Context) {
		handlerCalled = true
		c.JSON(200, gin.H{"status": "ok"})
	}

	// Create fallback handler
	fh := NewFallbackHandler(func() *httputil.ReverseProxy {
		return nil // no proxy
	})

	// Execute
	wrapped := fh.WrapHandler(handler)
	wrapped(c)

	// Assert: handler should be called directly (no mapping needed)
	assert.True(t, handlerCalled, "handler should be called for local provider")
	assert.Equal(t, 200, w.Code)
}

func TestFallbackHandler_WrapHandler_MappingApplied(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Register a mock provider for the target model
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client", "anthropic", []*registry.ModelInfo{
		{ID: "claude-opus-4-5-thinking"},
	})

	// Setup: model that needs mapping
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model": "claude-opus-4-5-20251101", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/anthropic/v1/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	// Handler to capture rewritten body
	var capturedBody []byte
	handler := func(c *gin.Context) {
		capturedBody, _ = io.ReadAll(c.Request.Body)
		c.JSON(200, gin.H{"status": "ok"})
	}

	// Create fallback handler with mapper
	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "claude-opus-4-5-20251101", To: "claude-opus-4-5-thinking"},
	})

	fh := NewFallbackHandlerWithMapper(
		func() *httputil.ReverseProxy { return nil },
		mapper,
		func() bool { return false },
	)

	// Execute
	wrapped := fh.WrapHandler(handler)
	wrapped(c)

	// Assert: body should be rewritten
	assert.Contains(t, string(capturedBody), "claude-opus-4-5-thinking")

	// Assert: context should have mapped model
	mappedModel, exists := c.Get(MappedModelContextKey)
	assert.True(t, exists, "MappedModelContextKey should be set")
	assert.NotEmpty(t, mappedModel)
}

func TestFallbackHandler_WrapHandler_ThinkingSuffixPreserved(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Register a mock provider for the target model
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-2", "anthropic", []*registry.ModelInfo{
		{ID: "claude-opus-4-5-thinking"},
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Model with thinking suffix
	body := `{"model": "claude-opus-4-5-20251101(xhigh)", "messages": []}`
	req := httptest.NewRequest(http.MethodPost, "/api/provider/anthropic/v1/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	var capturedBody []byte
	handler := func(c *gin.Context) {
		capturedBody, _ = io.ReadAll(c.Request.Body)
		c.JSON(200, gin.H{"status": "ok"})
	}

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "claude-opus-4-5-20251101", To: "claude-opus-4-5-thinking"},
	})

	fh := NewFallbackHandlerWithMapper(
		func() *httputil.ReverseProxy { return nil },
		mapper,
		func() bool { return false },
	)

	wrapped := fh.WrapHandler(handler)
	wrapped(c)

	// Assert: thinking suffix should be preserved
	assert.Contains(t, string(capturedBody), "(xhigh)")
}

func TestFallbackHandler_WrapHandler_NoProvider_NoMapping_ProxyEnabled(t *testing.T) {
	// Skip: httptest.ResponseRecorder doesn't implement http.CloseNotifier
	// which is required by httputil.ReverseProxy. This test requires a real
	// HTTP server and client to properly test proxy behavior.
	t.Skip("requires real HTTP server for proxy testing")
}
