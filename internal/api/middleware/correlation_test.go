package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

var uuidV4Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestCorrelationMiddleware_GeneratesIDWhenNotProvided(t *testing.T) {
	router := gin.New()
	router.Use(CorrelationMiddleware())
	router.GET("/test", func(c *gin.Context) {
		correlationID := GetCorrelationID(c)
		c.String(http.StatusOK, correlationID)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !uuidV4Regex.MatchString(body) {
		t.Errorf("expected UUID v4 format, got %q", body)
	}

	respCorrelationID := w.Header().Get("X-Correlation-ID")
	if respCorrelationID != body {
		t.Errorf("response X-Correlation-ID header %q does not match context value %q", respCorrelationID, body)
	}

	respRequestID := w.Header().Get("X-Request-ID")
	if respRequestID != body {
		t.Errorf("response X-Request-ID header %q does not match context value %q", respRequestID, body)
	}
}

func TestCorrelationMiddleware_ExtractsFromXCorrelationIDHeader(t *testing.T) {
	router := gin.New()
	router.Use(CorrelationMiddleware())
	router.GET("/test", func(c *gin.Context) {
		correlationID := GetCorrelationID(c)
		c.String(http.StatusOK, correlationID)
	})

	expectedID := "my-custom-correlation-id-123"
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Correlation-ID", expectedID)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if body != expectedID {
		t.Errorf("expected correlation ID %q, got %q", expectedID, body)
	}

	respCorrelationID := w.Header().Get("X-Correlation-ID")
	if respCorrelationID != expectedID {
		t.Errorf("response X-Correlation-ID header %q does not match expected %q", respCorrelationID, expectedID)
	}
}

func TestCorrelationMiddleware_BackwardCompatibilityWithXRequestID(t *testing.T) {
	router := gin.New()
	router.Use(CorrelationMiddleware())
	router.GET("/test", func(c *gin.Context) {
		correlationID := GetCorrelationID(c)
		c.String(http.StatusOK, correlationID)
	})

	expectedID := "legacy-request-id-456"
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", expectedID)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if body != expectedID {
		t.Errorf("expected correlation ID %q, got %q", expectedID, body)
	}

	respRequestID := w.Header().Get("X-Request-ID")
	if respRequestID != expectedID {
		t.Errorf("response X-Request-ID header %q does not match expected %q", respRequestID, expectedID)
	}
}

func TestCorrelationMiddleware_XCorrelationIDTakesPrecedence(t *testing.T) {
	router := gin.New()
	router.Use(CorrelationMiddleware())
	router.GET("/test", func(c *gin.Context) {
		correlationID := GetCorrelationID(c)
		c.String(http.StatusOK, correlationID)
	})

	expectedID := "correlation-id-wins"
	legacyID := "request-id-loses"
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Correlation-ID", expectedID)
	req.Header.Set("X-Request-ID", legacyID)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if body != expectedID {
		t.Errorf("expected correlation ID %q (from X-Correlation-ID), got %q", expectedID, body)
	}

	respCorrelationID := w.Header().Get("X-Correlation-ID")
	if respCorrelationID != expectedID {
		t.Errorf("response X-Correlation-ID header %q does not match expected %q", respCorrelationID, expectedID)
	}
}

func TestCorrelationMiddleware_ResponseIncludesBothHeaders(t *testing.T) {
	router := gin.New()
	router.Use(CorrelationMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	correlationID := w.Header().Get("X-Correlation-ID")
	requestID := w.Header().Get("X-Request-ID")

	if correlationID == "" {
		t.Error("expected X-Correlation-ID header to be present in response")
	}
	if requestID == "" {
		t.Error("expected X-Request-ID header to be present in response")
	}
	if correlationID != requestID {
		t.Errorf("X-Correlation-ID (%q) and X-Request-ID (%q) should match", correlationID, requestID)
	}
}

func TestCorrelationMiddleware_CorrelationIDAvailableInContext(t *testing.T) {
	var capturedID string

	router := gin.New()
	router.Use(CorrelationMiddleware())
	router.GET("/test", func(c *gin.Context) {
		capturedID = GetCorrelationID(c)
		val, exists := c.Get(CorrelationContextKey)
		if !exists {
			t.Error("expected correlation ID to be stored in context")
			return
		}
		if val.(string) != capturedID {
			t.Errorf("context value %q does not match GetCorrelationID result %q", val, capturedID)
		}
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if capturedID == "" {
		t.Error("expected correlation ID to be non-empty")
	}
	if !uuidV4Regex.MatchString(capturedID) {
		t.Errorf("expected UUID v4 format, got %q", capturedID)
	}
}

func TestGetCorrelationID_ReturnsEmptyForNilContext(t *testing.T) {
	id := GetCorrelationID(nil)
	if id != "" {
		t.Errorf("expected empty string for nil context, got %q", id)
	}
}

func TestGetCorrelationID_ReturnsEmptyWhenNotSet(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	id := GetCorrelationID(c)
	if id != "" {
		t.Errorf("expected empty string when not set, got %q", id)
	}
}
