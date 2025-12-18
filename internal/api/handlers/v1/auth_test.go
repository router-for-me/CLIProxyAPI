package v1

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestAuthRequired_EmptySecret(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthRequired(""), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer any-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("AuthRequired with empty secret should return 401, got %d", w.Code)
	}
}

func TestAuthRequired_ValidBearerToken(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthRequired("test-secret"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("AuthRequired with valid bearer token should return 200, got %d", w.Code)
	}
}

func TestAuthRequired_ValidManagementKey(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthRequired("test-secret"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Management-Key", "test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("AuthRequired with valid X-Management-Key should return 200, got %d", w.Code)
	}
}

func TestAuthRequired_InvalidTokenStandalone(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthRequired("test-secret"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("AuthRequired with invalid token should return 401, got %d", w.Code)
	}
}

func TestAuthRequired_MissingAuth(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthRequired("test-secret"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("AuthRequired with no auth should return 401, got %d", w.Code)
	}
}

func TestAuthRequired_NonBearerAuth(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthRequired("test-secret"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("AuthRequired with non-bearer auth should return 401, got %d", w.Code)
	}
}

func TestAuthRequired_CaseInsensitiveBearer(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthRequired("test-secret"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	testCases := []string{"bearer", "BEARER", "Bearer", "BeArEr"}
	for _, prefix := range testCases {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", prefix+" test-secret")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("AuthRequired should accept %q prefix, got status %d", prefix, w.Code)
		}
	}
}

func TestAuthOptional_NoAuth(t *testing.T) {
	var authenticated bool
	r := gin.New()
	r.GET("/test", AuthOptional("test-secret"), func(c *gin.Context) {
		_, authenticated = c.Get("authenticated")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("AuthOptional with no auth should return 200, got %d", w.Code)
	}

	if authenticated {
		t.Error("authenticated should be false when no auth provided")
	}
}

func TestAuthOptional_WithValidAuth(t *testing.T) {
	var authenticated bool
	r := gin.New()
	r.GET("/test", AuthOptional("test-secret"), func(c *gin.Context) {
		val, exists := c.Get("authenticated")
		authenticated = exists && val.(bool)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("AuthOptional with valid auth should return 200, got %d", w.Code)
	}

	if !authenticated {
		t.Error("authenticated should be true when valid auth provided")
	}
}

func TestAuthOptional_EmptySecret(t *testing.T) {
	r := gin.New()
	r.GET("/test", AuthOptional(""), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("AuthOptional with empty secret should return 200, got %d", w.Code)
	}
}

func TestExtractToken_BearerWithSpaces(t *testing.T) {
	r := gin.New()
	var extractedToken string
	r.GET("/test", func(c *gin.Context) {
		extractedToken = extractToken(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer   test-secret  ")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if extractedToken != "test-secret" {
		t.Errorf("extractToken should trim whitespace, got %q", extractedToken)
	}
}

func TestExtractToken_ManagementKeyWithSpaces(t *testing.T) {
	r := gin.New()
	var extractedToken string
	r.GET("/test", func(c *gin.Context) {
		extractedToken = extractToken(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Management-Key", "  test-secret  ")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if extractedToken != "test-secret" {
		t.Errorf("extractToken should trim whitespace from X-Management-Key, got %q", extractedToken)
	}
}

func TestExtractToken_BearerTakesPrecedence(t *testing.T) {
	r := gin.New()
	var extractedToken string
	r.GET("/test", func(c *gin.Context) {
		extractedToken = extractToken(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer bearer-token")
	req.Header.Set("X-Management-Key", "management-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if extractedToken != "bearer-token" {
		t.Errorf("Bearer token should take precedence, got %q", extractedToken)
	}
}
