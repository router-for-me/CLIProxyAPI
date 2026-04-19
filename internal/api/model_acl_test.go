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

func TestModelACLMiddleware_OversizedBodyRejectedWith413(t *testing.T) {
	// An oversized request body must not silently bypass policy by being
	// too large to inspect — the middleware is expected to return 413 so
	// the client sees a clear failure instead of the ACL being skipped.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-narrow")

	// Build a body just over the cap.
	filler := bytes.Repeat([]byte("a"), int(modelACLMaxBodyBytes)+128)
	body := append([]byte(`{"model":"gpt-4o","pad":"`), filler...)
	body = append(body, '"', '}')
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "request_too_large") {
		t.Fatalf("expected request_too_large error type, got %s", w.Body.String())
	}
}

func TestModelACLMiddleware_OversizedBodyRejectedViaContentLength(t *testing.T) {
	// When Content-Length alone tells us the body is too large we should
	// bail before reading, to avoid buffering a large payload even briefly.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-narrow")
	body := []byte(`{"model":"gpt-4o"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = modelACLMaxBodyBytes + 1
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 via Content-Length, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelACLMiddleware_GeminiPathPreservesPrefix(t *testing.T) {
	// Gemini deployments that opt into force-model-prefix use paths like
	// /v1beta/models/teamA/gemini-3-pro:generateContent where the routed
	// model identifier is literally "teamA/gemini-3-pro" — the first "/"
	// after the prefix is part of the model, not a path separator. The
	// extractor must forward the whole segment-before-":" so the ACL
	// checks the real model rather than truncating to the prefix segment.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-prefixed"},
			APIKeyPolicies: []config.APIKeyPolicy{
				// IsModelAllowedForKey strips the "<prefix>/" before glob
				// matching, so "gemini-3-*" should accept prefixed and
				// unprefixed forms alike.
				{Key: "sk-prefixed", AllowedModels: []string{"gemini-3-*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-prefixed")

	// Allowed prefixed model.
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/teamA/gemini-3-pro:generateContent", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("allowed prefixed gemini model: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Disallowed prefixed model — if the extractor were still truncating at
	// the first "/", this would erroneously check "teamA" against the ACL
	// and the test would give a false-positive pass. By choosing a model
	// segment that definitely does not match "gemini-3-*" we lock in that
	// the extractor forwards the real identifier.
	req = httptest.NewRequest(http.MethodPost, "/v1beta/models/teamA/claude-3-sonnet:generateContent", strings.NewReader("{}"))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("disallowed prefixed gemini model: expected 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelACLMiddleware_ModelInPeekLargeBodyPassesThrough(t *testing.T) {
	// When "model" appears within the peek window but the body as a whole is
	// larger than the peek, the middleware must (a) still extract the model
	// correctly, (b) enforce policy based on it, and (c) preserve the full
	// body for the downstream handler. This locks in the peek-first
	// optimization: the fast path is only valid if the downstream reader
	// still sees every byte.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-narrow")

	// Body with model at the top, then a large trailing "pad" string that
	// pushes total size well beyond modelACLPeekBytes but well below
	// modelACLMaxBodyBytes.
	padSize := int(modelACLPeekBytes) * 4
	filler := bytes.Repeat([]byte("x"), padSize)
	body := append([]byte(`{"model":"gpt-4o-mini","pad":"`), filler...)
	body = append(body, '"', '}')

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String()[:min(200, len(w.Body.String()))])
	}
	if w.Body.Len() != len(body) {
		t.Fatalf("downstream handler received %d bytes, want %d (full body must be preserved)", w.Body.Len(), len(body))
	}
	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("downstream handler received mutated body")
	}
}

func TestModelACLMiddleware_ModelAfterPeekFallsBackCorrectly(t *testing.T) {
	// When "model" is NOT in the peek window but is within the cap, the
	// middleware must fall back to reading the remainder and still extract
	// the model correctly. This covers the less common but legal case of
	// a large prompt preceding the "model" field.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-narrow")

	// Place a large "pad" field BEFORE "model" so the peek window fills
	// before gjson can see the model key.
	padSize := int(modelACLPeekBytes) * 2
	filler := bytes.Repeat([]byte("y"), padSize)
	body := append([]byte(`{"pad":"`), filler...)
	body = append(body, '"', ',')
	body = append(body, []byte(`"model":"gpt-4o-late"}`)...)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (model allowed), got %d", w.Code)
	}
	if w.Body.Len() != len(body) {
		t.Fatalf("downstream body length %d != request length %d", w.Body.Len(), len(body))
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
