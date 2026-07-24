package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestRouter(cfg *config.Config, apiKey string) *gin.Engine {
	router := gin.New()
	// Stand-in for AuthMiddleware: install the api key directly into context.
	router.Use(func(c *gin.Context) {
		if apiKey != "" {
			c.Set("apiKey", apiKey)
		}
		c.Next()
	})
	router.Use(ModelACLMiddleware(func() *config.Config { return cfg }))

	router.POST("/v1/chat/completions", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Echo the body back so the test can verify the downstream handler
		// was able to read it after the middleware peeked.
		c.Data(http.StatusOK, "application/json", body)
	})
	router.POST("/v1beta/models/*action", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "path": c.Request.URL.Path})
	})
	router.GET("/v1/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": []string{}})
	})
	return router
}

func TestModelACLMiddleware_NoPoliciesAllowsAllByDefault(t *testing.T) {
	cfg := &config.Config{}
	router := newTestRouter(cfg, "sk-anything")

	body, _ := json.Marshal(map[string]any{"model": "gpt-5"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "gpt-5") {
		t.Fatalf("downstream handler did not see body: %s", w.Body.String())
	}
}

func TestModelACLMiddleware_AllowedModelPasses(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-narrow")

	body, _ := json.Marshal(map[string]any{"model": "gpt-4o-mini", "messages": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelACLMiddleware_DisallowedModelRejected(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-narrow")

	body, _ := json.Marshal(map[string]any{"model": "claude-3-5-sonnet-20241022"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "model_not_allowed_for_key") {
		t.Fatalf("expected error code in body, got %s", w.Body.String())
	}
}

func TestModelACLMiddleware_DenyAllDefaultRejectsUnknownKey(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyDefaultPolicy: config.APIKeyDefaultPolicyDenyAll,
		},
	}
	router := newTestRouter(cfg, "sk-anything")
	body, _ := json.Marshal(map[string]any{"model": "gpt-5"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelACLMiddleware_GeminiPathExtraction(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-gemini"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-gemini", AllowedModels: []string{"gemini-2.0-flash"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-gemini")

	// Allowed model
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("allowed gemini model: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Disallowed model
	req = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:generateContent", strings.NewReader("{}"))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("disallowed gemini model: expected 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelACLMiddleware_ListEndpointAlwaysAllowed(t *testing.T) {
	// /v1/models has no body and no model in path; the middleware must
	// permit it regardless of policy so clients can still discover models.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyDefaultPolicy: config.APIKeyDefaultPolicyDenyAll,
		},
	}
	router := newTestRouter(cfg, "sk-anything")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on listing endpoint, got %d", w.Code)
	}
}
