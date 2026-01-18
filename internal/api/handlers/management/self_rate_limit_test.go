package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func setupTestHandler() *Handler {
	cfg := &config.Config{}
	return &Handler{
		cfg: cfg,
	}
}

func TestGetAllSelfRateLimits_Empty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v0/management/self-rate-limit", nil)

	h.GetAllSelfRateLimits(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result map[string]config.ProviderRateLimit
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestPutSelfRateLimit_Valid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	body := `{"min-delay-ms": 100, "max-delay-ms": 500, "chunk-delay-ms": 50}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/v0/management/self-rate-limit/claude", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}

	h.PutSelfRateLimit(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var result config.ProviderRateLimit
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if result.MinDelayMs != 100 || result.MaxDelayMs != 500 || result.ChunkDelayMs != 50 {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestPutSelfRateLimit_MinGreaterThanMax(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	body := `{"min-delay-ms": 500, "max-delay-ms": 100, "chunk-delay-ms": 50}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/v0/management/self-rate-limit/claude", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}

	h.PutSelfRateLimit(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestPutSelfRateLimit_NegativeValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	body := `{"min-delay-ms": -100, "max-delay-ms": 500, "chunk-delay-ms": 50}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/v0/management/self-rate-limit/claude", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}

	h.PutSelfRateLimit(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestGetSelfRateLimit_AfterPut(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	// First PUT
	body := `{"min-delay-ms": 100, "max-delay-ms": 500, "chunk-delay-ms": 50}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/v0/management/self-rate-limit/claude", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}
	h.PutSelfRateLimit(c)

	// Then GET
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v0/management/self-rate-limit/claude", nil)
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}

	h.GetSelfRateLimit(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result config.ProviderRateLimit
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if result.MinDelayMs != 100 {
		t.Errorf("expected MinDelayMs 100, got %d", result.MinDelayMs)
	}
}

func TestGetSelfRateLimit_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v0/management/self-rate-limit/unknown", nil)
	c.Params = gin.Params{{Key: "provider", Value: "unknown"}}

	h.GetSelfRateLimit(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestDeleteSelfRateLimit_AfterPut(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	// First PUT
	body := `{"min-delay-ms": 100, "max-delay-ms": 500, "chunk-delay-ms": 50}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/v0/management/self-rate-limit/claude", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}
	h.PutSelfRateLimit(c)

	// Then DELETE
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/v0/management/self-rate-limit/claude", nil)
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}

	h.DeleteSelfRateLimit(c)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", w.Code)
	}

	// GET should now return 404 (override is nil, meaning cleared)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v0/management/self-rate-limit/claude", nil)
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}

	h.GetSelfRateLimit(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 after delete, got %d", w.Code)
	}
}

func TestDeleteSelfRateLimit_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/v0/management/self-rate-limit/unknown", nil)
	c.Params = gin.Params{{Key: "provider", Value: "unknown"}}

	h.DeleteSelfRateLimit(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestGetAllSelfRateLimits_MergesConfigAndOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := setupTestHandler()

	// Set up config with one provider
	h.cfg.SelfRateLimit = map[string]config.ProviderRateLimit{
		"vertex": {MinDelayMs: 50, MaxDelayMs: 200, ChunkDelayMs: 20},
	}

	// Add override for another provider
	body := `{"min-delay-ms": 100, "max-delay-ms": 500, "chunk-delay-ms": 50}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/v0/management/self-rate-limit/claude", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "provider", Value: "claude"}}
	h.PutSelfRateLimit(c)

	// GET all should return both
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/v0/management/self-rate-limit", nil)

	h.GetAllSelfRateLimits(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result map[string]config.ProviderRateLimit
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 providers, got %d", len(result))
	}

	if _, ok := result["vertex"]; !ok {
		t.Error("expected vertex from config")
	}
	if _, ok := result["claude"]; !ok {
		t.Error("expected claude from override")
	}
}
