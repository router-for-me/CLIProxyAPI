package amp

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/modules"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

func TestAmpModule_Name(t *testing.T) {
	m := New()
	if m.Name() != "amp-routing" {
		t.Fatalf("want amp-routing, got %s", m.Name())
	}
}

func TestAmpModule_New(t *testing.T) {
	accessManager := sdkaccess.NewManager()
	authMiddleware := func(c *gin.Context) { c.Next() }

	m := NewLegacy(accessManager, authMiddleware)

	if m.accessManager != accessManager {
		t.Fatal("accessManager not set")
	}
	if m.authMiddleware_ == nil {
		t.Fatal("authMiddleware not set")
	}
	if m.enabled {
		t.Fatal("enabled should be false initially")
	}
	if m.proxy != nil {
		t.Fatal("proxy should be nil initially")
	}
}

func TestAmpModule_Register_WithUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Fake upstream to ensure URL is valid
	upstream := httptest.NewServer(nil)
	defer upstream.Close()

	accessManager := sdkaccess.NewManager()
	base := &handlers.BaseAPIHandler{}

	m := NewLegacy(accessManager, func(c *gin.Context) { c.Next() })

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			UpstreamURL:    upstream.URL,
			UpstreamAPIKey: "test-key",
		},
	}

	ctx := modules.Context{Engine: r, BaseHandler: base, Config: cfg, AuthMiddleware: func(c *gin.Context) { c.Next() }}
	if err := m.Register(ctx); err != nil {
		t.Fatalf("register error: %v", err)
	}

	if !m.enabled {
		t.Fatal("module should be enabled with upstream URL")
	}
	if m.proxy == nil {
		t.Fatal("proxy should be initialized")
	}
	if m.secretSource == nil {
		t.Fatal("secretSource should be initialized")
	}
}

func TestAmpModule_Register_WithoutUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	accessManager := sdkaccess.NewManager()
	base := &handlers.BaseAPIHandler{}

	m := NewLegacy(accessManager, func(c *gin.Context) { c.Next() })

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			UpstreamURL: "", // No upstream
		},
	}

	ctx := modules.Context{Engine: r, BaseHandler: base, Config: cfg, AuthMiddleware: func(c *gin.Context) { c.Next() }}
	if err := m.Register(ctx); err != nil {
		t.Fatalf("register should not error without upstream: %v", err)
	}

	if m.enabled {
		t.Fatal("module should be disabled without upstream URL")
	}
	if m.proxy != nil {
		t.Fatal("proxy should not be initialized without upstream")
	}

	// But provider aliases should still be registered
	req := httptest.NewRequest("GET", "/api/provider/openai/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == 404 {
		t.Fatal("provider aliases should be registered even without upstream")
	}
}

func TestAmpModule_Register_InvalidUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	accessManager := sdkaccess.NewManager()
	base := &handlers.BaseAPIHandler{}

	m := NewLegacy(accessManager, func(c *gin.Context) { c.Next() })

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			UpstreamURL: "://invalid-url",
		},
	}

	ctx := modules.Context{Engine: r, BaseHandler: base, Config: cfg, AuthMiddleware: func(c *gin.Context) { c.Next() }}
	if err := m.Register(ctx); err == nil {
		t.Fatal("expected error for invalid upstream URL")
	}
}

func TestAmpModule_OnConfigUpdated_CacheInvalidation(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "secrets.json")
	if err := os.WriteFile(p, []byte(`{"apiKey@https://ampcode.com/":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}

	m := &AmpModule{enabled: true}
	ms := NewMultiSourceSecretWithPath("", p, time.Minute)
	m.secretSource = ms
	m.lastConfig = &config.AmpCode{
		UpstreamAPIKey: "old-key",
	}

	// Warm the cache
	if _, err := ms.Get(context.Background()); err != nil {
		t.Fatal(err)
	}

	if ms.cache == nil {
		t.Fatal("expected cache to be set")
	}

	// Update config - should invalidate cache
	if err := m.OnConfigUpdated(&config.Config{AmpCode: config.AmpCode{UpstreamURL: "http://x", UpstreamAPIKey: "new-key"}}); err != nil {
		t.Fatal(err)
	}

	if ms.cache != nil {
		t.Fatal("expected cache to be invalidated")
	}
}

func TestAmpModule_OnConfigUpdated_NotEnabled(t *testing.T) {
	m := &AmpModule{enabled: false}

	// Should not error or panic when disabled
	if err := m.OnConfigUpdated(&config.Config{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAmpModule_OnConfigUpdated_URLRemoved(t *testing.T) {
	m := &AmpModule{enabled: true}
	ms := NewMultiSourceSecret("", 0)
	m.secretSource = ms

	// Config update with empty URL - should log warning but not error
	cfg := &config.Config{AmpCode: config.AmpCode{UpstreamURL: ""}}

	if err := m.OnConfigUpdated(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAmpModule_OnConfigUpdated_NonMultiSourceSecret(t *testing.T) {
	// Test that OnConfigUpdated doesn't panic with StaticSecretSource
	m := &AmpModule{enabled: true}
	m.secretSource = NewStaticSecretSource("static-key")

	cfg := &config.Config{AmpCode: config.AmpCode{UpstreamURL: "http://example.com"}}

	// Should not error or panic
	if err := m.OnConfigUpdated(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAmpModule_AuthMiddleware_Fallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Create module with no auth middleware
	m := &AmpModule{authMiddleware_: nil}

	// Get the fallback middleware via getAuthMiddleware
	ctx := modules.Context{Engine: r, AuthMiddleware: nil}
	middleware := m.getAuthMiddleware(ctx)

	if middleware == nil {
		t.Fatal("getAuthMiddleware should return a fallback, not nil")
	}

	// Test that it works
	called := false
	r.GET("/test", middleware, func(c *gin.Context) {
		called = true
		c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Fatal("fallback middleware should allow requests through")
	}
}

func TestAmpModule_SecretSource_FromConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	upstream := httptest.NewServer(nil)
	defer upstream.Close()

	accessManager := sdkaccess.NewManager()
	base := &handlers.BaseAPIHandler{}

	m := NewLegacy(accessManager, func(c *gin.Context) { c.Next() })

	// Config with explicit API key
	cfg := &config.Config{
		AmpCode: config.AmpCode{
			UpstreamURL:    upstream.URL,
			UpstreamAPIKey: "config-key",
		},
	}

	ctx := modules.Context{Engine: r, BaseHandler: base, Config: cfg, AuthMiddleware: func(c *gin.Context) { c.Next() }}
	if err := m.Register(ctx); err != nil {
		t.Fatalf("register error: %v", err)
	}

	// Secret source should be MultiSourceSecret with config key
	if m.secretSource == nil {
		t.Fatal("secretSource should be set")
	}

	// Verify it returns the config key
	key, err := m.secretSource.Get(context.Background())
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if key != "config-key" {
		t.Fatalf("want config-key, got %s", key)
	}
}

func TestAmpModule_ProviderAliasesAlwaysRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)

	scenarios := []struct {
		name      string
		configURL string
	}{
		{"with_upstream", "http://example.com"},
		{"without_upstream", ""},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			r := gin.New()
			accessManager := sdkaccess.NewManager()
			base := &handlers.BaseAPIHandler{}

			m := NewLegacy(accessManager, func(c *gin.Context) { c.Next() })

			cfg := &config.Config{AmpCode: config.AmpCode{UpstreamURL: scenario.configURL}}

			ctx := modules.Context{Engine: r, BaseHandler: base, Config: cfg, AuthMiddleware: func(c *gin.Context) { c.Next() }}
			if err := m.Register(ctx); err != nil && scenario.configURL != "" {
				t.Fatalf("register error: %v", err)
			}

			// Provider aliases should always be available
			req := httptest.NewRequest("GET", "/api/provider/openai/models", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code == 404 {
				t.Fatal("provider aliases should be registered")
			}
		})
	}
}

func TestAmpModule_hasUpstreamAPIKeysChanged_DetectsRemovedKeyWithDuplicateInput(t *testing.T) {
	m := &AmpModule{}

	oldCfg := &config.AmpCode{
		UpstreamAPIKeys: []config.AmpUpstreamAPIKeyEntry{
			{UpstreamAPIKey: "u1", APIKeys: []string{"k1", "k2"}},
		},
	}
	newCfg := &config.AmpCode{
		UpstreamAPIKeys: []config.AmpUpstreamAPIKeyEntry{
			{UpstreamAPIKey: "u1", APIKeys: []string{"k1", "k1"}},
		},
	}

	if !m.hasUpstreamAPIKeysChanged(oldCfg, newCfg) {
		t.Fatal("expected change to be detected when k2 is removed but new list contains duplicates")
	}
}

func TestAmpModule_hasUpstreamAPIKeysChanged_IgnoresEmptyAndWhitespaceKeys(t *testing.T) {
	m := &AmpModule{}

	oldCfg := &config.AmpCode{
		UpstreamAPIKeys: []config.AmpUpstreamAPIKeyEntry{
			{UpstreamAPIKey: "u1", APIKeys: []string{"k1", "k2"}},
		},
	}
	newCfg := &config.AmpCode{
		UpstreamAPIKeys: []config.AmpUpstreamAPIKeyEntry{
			{UpstreamAPIKey: "u1", APIKeys: []string{"  k1  ", "", "k2", "   "}},
		},
	}

	if m.hasUpstreamAPIKeysChanged(oldCfg, newCfg) {
		t.Fatal("expected no change when only whitespace/empty entries differ")
	}
}
