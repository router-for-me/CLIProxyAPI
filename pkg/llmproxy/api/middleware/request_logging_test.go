package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/logging"
)

type mockRequestLogger struct {
	enabled bool
	logged  bool
}

func (m *mockRequestLogger) IsEnabled() bool { return m.enabled }
func (m *mockRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	m.logged = true
	return nil
}
func (m *mockRequestLogger) LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (logging.StreamingLogWriter, error) {
	return &logging.NoOpStreamingLogWriter{}, nil
}

func TestRequestLoggingMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("LoggerNil", func(t *testing.T) {
		router := gin.New()
		router.Use(RequestLoggingMiddleware(nil))
		router.POST("/test", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/test", nil)
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("expected 200")
		}
	})

	t.Run("GETMethod", func(t *testing.T) {
		logger := &mockRequestLogger{enabled: true}
		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.GET("/test", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w, req)
		if logger.logged {
			t.Errorf("should not log GET requests")
		}
	})

	t.Run("ManagementPath", func(t *testing.T) {
		logger := &mockRequestLogger{enabled: true}
		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/management/test", func(c *gin.Context) { c.Status(200) })

		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/management/test", nil)
		router.ServeHTTP(w, req)
		if logger.logged {
			t.Errorf("should not log management paths")
		}
	})

	t.Run("LogEnabled", func(t *testing.T) {
		logger := &mockRequestLogger{enabled: true}
		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/chat/completions", func(c *gin.Context) {
			c.JSON(200, gin.H{"ok": true})
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"test":true}`)))
		router.ServeHTTP(w, req)
		if !logger.logged {
			t.Errorf("should have logged the request")
		}
	})
}

func TestShouldLogRequest(t *testing.T) {
	cases := []struct {
		path     string
		expected bool
	}{
		{"/v1/chat/completions", true},
		{"/management/config", false},
		{"/v0/management/config", false},
		{"/api/provider/test", true},
		{"/api/other", false},
	}

	for _, c := range cases {
		if got := shouldLogRequest(c.path); got != c.expected {
			t.Errorf("path %s: expected %v, got %v", c.path, c.expected, got)
		}
	}
}
