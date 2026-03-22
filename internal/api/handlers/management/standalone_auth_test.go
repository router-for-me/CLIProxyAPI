package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// TestMiddleware_LocalPasswordOnly verifies fix dff3e67a: when no SecretKey and no
// MANAGEMENT_PASSWORD env var are set, a localhost request authenticated with the
// localPassword must succeed (HTTP 200), not be rejected (HTTP 403).
func TestMiddleware_LocalPasswordOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		// No envSecret, no secretHash — only localPassword
		localPassword: "tui-test-password",
	}

	router := gin.New()
	router.Use(h.Middleware())
	router.GET("/management/config", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	t.Run("correct localPassword from localhost is accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management/config", nil)
		req.Header.Set("Authorization", "Bearer tui-test-password")
		req.RemoteAddr = "127.0.0.1:12345"

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 with correct localPassword, got %d", w.Code)
		}
	})

	t.Run("no credentials returns 403 when no key is set", func(t *testing.T) {
		// Handler with no credentials at all — should reject immediately
		hEmpty := &Handler{
			cfg:            &config.Config{},
			failedAttempts: make(map[string]*attemptInfo),
		}
		rEmpty := gin.New()
		rEmpty.Use(hEmpty.Middleware())
		rEmpty.GET("/management/config", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})

		req := httptest.NewRequest(http.MethodGet, "/management/config", nil)
		req.Header.Set("Authorization", "Bearer anything")
		req.RemoteAddr = "127.0.0.1:12345"

		w := httptest.NewRecorder()
		rEmpty.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 with no credential source, got %d", w.Code)
		}
	})

	t.Run("wrong localPassword from localhost is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management/config", nil)
		req.Header.Set("Authorization", "Bearer wrong-password")
		req.RemoteAddr = "127.0.0.1:12345"

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Error("expected non-200 with wrong password, got 200")
		}
	})

	t.Run("no Authorization header returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management/config", nil)
		req.RemoteAddr = "127.0.0.1:12345"

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with no auth header, got %d", w.Code)
		}
	})
}
