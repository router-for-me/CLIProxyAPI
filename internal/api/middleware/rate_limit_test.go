package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestInMemoryLimiterStore_TryConsume(t *testing.T) {
	store := NewInMemoryLimiterStore()

	t.Run("allows requests under capacity", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			allowed, _ := store.TryConsume("test-key-1", 10, 1.0)
			if !allowed {
				t.Errorf("request %d should be allowed", i)
			}
		}
	})

	t.Run("blocks requests over capacity", func(t *testing.T) {
		key := "test-key-2"
		for i := 0; i < 3; i++ {
			store.TryConsume(key, 3, 0.1)
		}
		allowed, retryAfter := store.TryConsume(key, 3, 0.1)
		if allowed {
			t.Error("request should be blocked")
		}
		if retryAfter <= 0 {
			t.Error("retryAfter should be positive")
		}
	})

	t.Run("refills tokens over time", func(t *testing.T) {
		key := "test-key-3"
		for i := 0; i < 2; i++ {
			store.TryConsume(key, 2, 10.0) // High refill rate
		}
		time.Sleep(150 * time.Millisecond)
		allowed, _ := store.TryConsume(key, 2, 10.0)
		if !allowed {
			t.Error("request should be allowed after refill")
		}
	})

	t.Run("zero capacity allows all", func(t *testing.T) {
		allowed, _ := store.TryConsume("test-key-4", 0, 1.0)
		if !allowed {
			t.Error("zero capacity should allow all requests")
		}
	})

	t.Run("zero refill rate allows all", func(t *testing.T) {
		allowed, _ := store.TryConsume("test-key-5", 10, 0)
		if !allowed {
			t.Error("zero refill rate should allow all requests")
		}
	})
}

func TestRateLimiter_IsEnabled(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		rl := NewRateLimiter(nil)
		if rl.IsEnabled() {
			t.Error("should be disabled when config is nil")
		}
	})

	t.Run("enabled when config says so", func(t *testing.T) {
		cfg := &config.RateLimitConfig{Enabled: true}
		rl := NewRateLimiter(cfg)
		if !rl.IsEnabled() {
			t.Error("should be enabled when config.Enabled is true")
		}
	})

	t.Run("can update config", func(t *testing.T) {
		rl := NewRateLimiter(nil)
		cfg := &config.RateLimitConfig{Enabled: true}
		rl.UpdateConfig(cfg)
		if !rl.IsEnabled() {
			t.Error("should be enabled after UpdateConfig")
		}
	})
}

func TestRateLimitMiddleware(t *testing.T) {
	cfg := &config.RateLimitConfig{
		Enabled: true,
		Messages: config.MessagesRateLimitConfig{
			PerIP: config.TokenBucketConfig{
				Capacity:        2,
				RefillPerSecond: 0.1,
			},
		},
	}
	rl := NewRateLimiter(cfg)

	handler := func(c *gin.Context) {
		c.String(http.StatusOK, "OK")
	}

	t.Run("allows requests under limit", func(t *testing.T) {
		router := gin.New()
		router.POST("/v1/messages", RateLimitMiddleware(rl), handler)

		for i := 0; i < 2; i++ {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.RemoteAddr = "192.168.1.100:12345"
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("request %d: expected 200, got %d", i, w.Code)
			}
		}
	})

	t.Run("blocks requests over limit", func(t *testing.T) {
		rl2 := NewRateLimiter(&config.RateLimitConfig{
			Enabled: true,
			Messages: config.MessagesRateLimitConfig{
				PerIP: config.TokenBucketConfig{
					Capacity:        1,
					RefillPerSecond: 0.001,
				},
			},
		})

		router := gin.New()
		router.POST("/v1/messages", RateLimitMiddleware(rl2), handler)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.RemoteAddr = "192.168.1.200:12345"
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("first request: expected 200, got %d", w.Code)
		}

		w = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.RemoteAddr = "192.168.1.200:12345"
		router.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Errorf("second request: expected 429, got %d", w.Code)
		}

		if w.Header().Get("Retry-After") == "" {
			t.Error("Retry-After header should be set")
		}
	})

	t.Run("disabled middleware passes through", func(t *testing.T) {
		disabledRL := NewRateLimiter(&config.RateLimitConfig{Enabled: false})

		router := gin.New()
		router.POST("/v1/messages", RateLimitMiddleware(disabledRL), handler)

		for i := 0; i < 10; i++ {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("disabled limiter should allow all requests, got %d", w.Code)
			}
		}
	})
}

func TestExtractAuthID(t *testing.T) {
	t.Run("extracts from Authorization header", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Authorization", "Bearer sk-test-1234567890")

		authID := extractAuthID(c)
		if !strings.HasPrefix(authID, "key:") {
			t.Errorf("expected key prefix, got %s", authID)
		}
	})

	t.Run("extracts from x-api-key header", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("x-api-key", "test-api-key-12345")

		authID := extractAuthID(c)
		if !strings.HasPrefix(authID, "key:") {
			t.Errorf("expected key prefix, got %s", authID)
		}
	})

	t.Run("extracts from context auth_id", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Set("auth_id", "custom-auth-id")

		authID := extractAuthID(c)
		if authID != "custom-auth-id" {
			t.Errorf("expected custom-auth-id, got %s", authID)
		}
	})

	t.Run("returns empty for no auth", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

		authID := extractAuthID(c)
		if authID != "" {
			t.Errorf("expected empty, got %s", authID)
		}
	})
}

func TestExtractModelFromRequest(t *testing.T) {
	t.Run("extracts model from body", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		body := `{"model": "claude-3-opus"}`
		c.Set("request_body", []byte(body))

		model := extractModelFromRequest(c)
		if model != "claude-3-opus" {
			t.Errorf("expected claude-3-opus, got %s", model)
		}
	})

	t.Run("returns unknown for missing body", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

		model := extractModelFromRequest(c)
		if model != "unknown" {
			t.Errorf("expected unknown, got %s", model)
		}
	})

	t.Run("returns unknown for missing model field", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		body := `{"messages": []}`
		c.Set("request_body", []byte(body))

		model := extractModelFromRequest(c)
		if model != "unknown" {
			t.Errorf("expected unknown, got %s", model)
		}
	})
}
