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

	// Create module with proxy for testing
	m := &AmpModule{
		restrictToLocalhost: false, // disable localhost restriction for tests
	}

	// Create a mock proxy that tracks calls
	proxyCalled := false
	mockProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled = true
		w.WriteHeader(200)
		w.Write([]byte("proxied"))
	}))
	defer mockProxy.Close()

	// Create real proxy to mock server
	proxy, _ := createReverseProxy(mockProxy.URL, NewStaticSecretSource(""))
	m.setProxy(proxy)

	base := &handlers.BaseAPIHandler{}
	m.registerManagementRoutes(r, base, nil)
	srv := httptest.NewServer(r)
	defer srv.Close()

	managementPaths := []struct {
		path   string
		method string
	}{
		{"/api/internal", http.MethodGet},
		{"/api/internal/some/path", http.MethodGet},
		{"/api/user", http.MethodGet},
		{"/api/user/profile", http.MethodGet},
		{"/api/auth", http.MethodGet},
		{"/api/auth/login", http.MethodGet},
		{"/api/meta", http.MethodGet},
		{"/api/telemetry", http.MethodGet},
		{"/api/threads", http.MethodGet},
		{"/threads/", http.MethodGet},
		{"/threads.rss", http.MethodGet}, // Root-level route (no /api prefix)
		{"/api/otel", http.MethodGet},
		{"/api/tab", http.MethodGet},
		{"/api/tab/some/path", http.MethodGet},
		{"/auth", http.MethodGet},           // Root-level auth route
		{"/auth/cli-login", http.MethodGet}, // CLI login flow
		{"/auth/callback", http.MethodGet},  // OAuth callback
		// Google v1beta1 bridge should still proxy non-model requests (GET) and allow POST
		{"/api/provider/google/v1beta1/models", http.MethodGet},
		{"/api/provider/google/v1beta1/models", http.MethodPost},
	}

	for _, path := range managementPaths {
		t.Run(path.path, func(t *testing.T) {
			proxyCalled = false
			req, err := http.NewRequest(path.method, srv.URL+path.path, nil)
			if err != nil {
				t.Fatalf("failed to build request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("route %s not registered", path.path)
			}
			if !proxyCalled {
				t.Fatalf("proxy handler not called for %s", path.path)
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

func TestLocalhostOnlyMiddleware_PreventsSpoofing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Create module with localhost restriction enabled
	m := &AmpModule{
		restrictToLocalhost: true,
	}

	// Apply dynamic localhost-only middleware
	r.Use(m.localhostOnlyMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	tests := []struct {
		name           string
		remoteAddr     string
		forwardedFor   string
		expectedStatus int
		description    string
	}{
		{
			name:           "spoofed_header_remote_connection",
			remoteAddr:     "192.168.1.100:12345",
			forwardedFor:   "127.0.0.1",
			expectedStatus: http.StatusForbidden,
			description:    "Spoofed X-Forwarded-For header should be ignored",
		},
		{
			name:           "real_localhost_ipv4",
			remoteAddr:     "127.0.0.1:54321",
			forwardedFor:   "",
			expectedStatus: http.StatusOK,
			description:    "Real localhost IPv4 connection should work",
		},
		{
			name:           "real_localhost_ipv6",
			remoteAddr:     "[::1]:54321",
			forwardedFor:   "",
			expectedStatus: http.StatusOK,
			description:    "Real localhost IPv6 connection should work",
		},
		{
			name:           "remote_ipv4",
			remoteAddr:     "203.0.113.42:8080",
			forwardedFor:   "",
			expectedStatus: http.StatusForbidden,
			description:    "Remote IPv4 connection should be blocked",
		},
		{
			name:           "remote_ipv6",
			remoteAddr:     "[2001:db8::1]:9090",
			forwardedFor:   "",
			expectedStatus: http.StatusForbidden,
			description:    "Remote IPv6 connection should be blocked",
		},
		{
			name:           "spoofed_localhost_ipv6",
			remoteAddr:     "203.0.113.42:8080",
			forwardedFor:   "::1",
			expectedStatus: http.StatusForbidden,
			description:    "Spoofed X-Forwarded-For with IPv6 localhost should be ignored",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("%s: expected status %d, got %d", tt.description, tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestLocalhostOnlyMiddleware_HotReload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Create module with localhost restriction initially enabled
	m := &AmpModule{
		restrictToLocalhost: true,
	}

	// Apply dynamic localhost-only middleware
	r.Use(m.localhostOnlyMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Test 1: Remote IP should be blocked when restriction is enabled
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403 when restriction enabled, got %d", w.Code)
	}

	// Test 2: Hot-reload - disable restriction
	m.setRestrictToLocalhost(false)

	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 after disabling restriction, got %d", w.Code)
	}

	// Test 3: Hot-reload - re-enable restriction
	m.setRestrictToLocalhost(true)

	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403 after re-enabling restriction, got %d", w.Code)
	}
}
