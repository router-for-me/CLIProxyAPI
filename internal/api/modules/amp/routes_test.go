package amp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

func TestRegisterManagementRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Spy to track if proxy handler was called
	proxyCalled := false
	proxyHandler := func(c *gin.Context) {
		proxyCalled = true
		c.String(200, "proxied")
	}

	m := &AmpModule{}
	m.registerManagementRoutes(r, proxyHandler, false) // false = don't restrict to localhost in tests

	managementPaths := []string{
		"/api/internal",
		"/api/internal/some/path",
		"/api/user",
		"/api/user/profile",
		"/api/auth",
		"/api/auth/login",
		"/api/meta",
		"/api/telemetry",
		"/api/threads",
		"/api/otel",
		"/api/provider/google/v1beta1/models",
	}

	for _, path := range managementPaths {
		t.Run(path, func(t *testing.T) {
			proxyCalled = false
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code == http.StatusNotFound {
				t.Fatalf("route %s not registered", path)
			}
			if !proxyCalled {
				t.Fatalf("proxy handler not called for %s", path)
			}
		})
	}
}

func TestRegisterProviderAliases_AllProvidersRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Minimal base handler setup (no need to initialize, just check routing)
	base := &handlers.BaseAPIHandler{}

	// Track if auth middleware was called
	authCalled := false
	authMiddleware := func(c *gin.Context) {
		authCalled = true
		c.Header("X-Auth", "ok")
		// Abort with success to avoid calling the actual handler (which needs full setup)
		c.AbortWithStatus(http.StatusOK)
	}

	m := &AmpModule{authMiddleware_: authMiddleware}
	m.registerProviderAliases(r, base, authMiddleware)

	paths := []struct {
		path   string
		method string
	}{
		{"/api/provider/openai/models", http.MethodGet},
		{"/api/provider/anthropic/models", http.MethodGet},
		{"/api/provider/google/models", http.MethodGet},
		{"/api/provider/groq/models", http.MethodGet},
		{"/api/provider/openai/chat/completions", http.MethodPost},
		{"/api/provider/anthropic/v1/messages", http.MethodPost},
		{"/api/provider/google/v1beta/models", http.MethodGet},
	}

	for _, tc := range paths {
		t.Run(tc.path, func(t *testing.T) {
			authCalled = false
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code == http.StatusNotFound {
				t.Fatalf("route %s %s not registered", tc.method, tc.path)
			}
			if !authCalled {
				t.Fatalf("auth middleware not executed for %s", tc.path)
			}
			if w.Header().Get("X-Auth") != "ok" {
				t.Fatalf("auth middleware header not set for %s", tc.path)
			}
		})
	}
}

func TestRegisterProviderAliases_DynamicModelsHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	base := &handlers.BaseAPIHandler{}

	m := &AmpModule{authMiddleware_: func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) }}
	m.registerProviderAliases(r, base, func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) })

	providers := []string{"openai", "anthropic", "google", "groq", "cerebras"}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			path := "/api/provider/" + provider + "/models"
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			// Should not 404
			if w.Code == http.StatusNotFound {
				t.Fatalf("models route not found for provider: %s", provider)
			}
		})
	}
}

func TestRegisterProviderAliases_V1Routes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	base := &handlers.BaseAPIHandler{}

	m := &AmpModule{authMiddleware_: func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) }}
	m.registerProviderAliases(r, base, func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) })

	v1Paths := []struct {
		path   string
		method string
	}{
		{"/api/provider/openai/v1/models", http.MethodGet},
		{"/api/provider/openai/v1/chat/completions", http.MethodPost},
		{"/api/provider/openai/v1/completions", http.MethodPost},
		{"/api/provider/anthropic/v1/messages", http.MethodPost},
		{"/api/provider/anthropic/v1/messages/count_tokens", http.MethodPost},
	}

	for _, tc := range v1Paths {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code == http.StatusNotFound {
				t.Fatalf("v1 route %s %s not registered", tc.method, tc.path)
			}
		})
	}
}

func TestRegisterProviderAliases_V1BetaRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	base := &handlers.BaseAPIHandler{}

	m := &AmpModule{authMiddleware_: func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) }}
	m.registerProviderAliases(r, base, func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) })

	v1betaPaths := []struct {
		path   string
		method string
	}{
		{"/api/provider/google/v1beta/models", http.MethodGet},
		{"/api/provider/google/v1beta/models/generateContent", http.MethodPost},
	}

	for _, tc := range v1betaPaths {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code == http.StatusNotFound {
				t.Fatalf("v1beta route %s %s not registered", tc.method, tc.path)
			}
		})
	}
}

func TestRegisterProviderAliases_NoAuthMiddleware(t *testing.T) {
	// Test that routes still register even if auth middleware is nil (fallback behavior)
	gin.SetMode(gin.TestMode)
	r := gin.New()

	base := &handlers.BaseAPIHandler{}

	m := &AmpModule{authMiddleware_: nil} // No auth middleware
	m.registerProviderAliases(r, base, func(c *gin.Context) { c.AbortWithStatus(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/api/provider/openai/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Should still work (with fallback no-op auth)
	if w.Code == http.StatusNotFound {
		t.Fatal("routes should register even without auth middleware")
	}
}
