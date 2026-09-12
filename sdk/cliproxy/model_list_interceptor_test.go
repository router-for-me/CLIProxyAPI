package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelListInterceptorFunc func(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse

func (f modelListInterceptorFunc) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return f(ctx, req)
}

func TestModelListPluginMiddlewareFiltersCodexCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var captured pluginapi.ResponseInterceptRequest
	interceptor := modelListInterceptorFunc(func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
		captured = req
		headers := req.ResponseHeaders.Clone()
		headers.Set("X-Model-Policy", "filtered")
		return pluginapi.ResponseInterceptResponse{
			Headers: headers,
			Body:    []byte("{\"models\":[{\"slug\":\"allowed\"}]}"),
		}
	})

	engine := gin.New()
	engine.Use(modelListPluginMiddleware(interceptor))
	v1 := engine.Group("/v1")
	v1.Use(func(c *gin.Context) {
		c.Set("accessProvider", "cpa-key-policy")
		c.Set("accessMetadata", map[string]string{"key_id": "team-a"})
		c.Next()
	})
	v1.GET("/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"models": []gin.H{
				{"slug": "allowed"},
				{"slug": "blocked"},
			},
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=pi", nil)
	req.Header.Set("Authorization", "Bearer cpa_test")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got, want := strings.TrimSpace(recorder.Body.String()), "{\"models\":[{\"slug\":\"allowed\"}]}"; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
	if got := recorder.Header().Get("X-Model-Policy"); got != "filtered" {
		t.Fatalf("X-Model-Policy = %q, want filtered", got)
	}
	if captured.SourceFormat != modelListSourceCodex {
		t.Fatalf("SourceFormat = %q, want %q", captured.SourceFormat, modelListSourceCodex)
	}
	if got := captured.RequestHeaders.Get("Authorization"); got != "Bearer cpa_test" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := captured.Metadata["access_provider"]; got != "cpa-key-policy" {
		t.Fatalf("access_provider = %#v", got)
	}
	accessMetadata, ok := captured.Metadata["access_metadata"].(map[string]string)
	if !ok || accessMetadata["key_id"] != "team-a" {
		t.Fatalf("access_metadata = %#v", captured.Metadata["access_metadata"])
	}
	if got := captured.Metadata["client_version"]; got != "pi" {
		t.Fatalf("client_version = %#v", got)
	}
	if !strings.Contains(string(captured.Body), "blocked") {
		t.Fatalf("interceptor did not receive original catalog: %s", captured.Body)
	}
}

func TestModelListPluginMiddlewareSkipsErrorResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	called := false
	interceptor := modelListInterceptorFunc(func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
		called = true
		return pluginapi.ResponseInterceptResponse{Headers: req.ResponseHeaders, Body: req.Body}
	})

	engine := gin.New()
	engine.Use(modelListPluginMiddleware(interceptor))
	engine.GET("/v1/models", func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "denied"})
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if called {
		t.Fatal("response interceptor was called for an error response")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(recorder.Body.String(), "denied") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}
