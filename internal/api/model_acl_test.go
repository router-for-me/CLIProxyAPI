package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
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

func TestModelACLMiddleware_WebsocketUpgradeRejectedForRestrictedKey(t *testing.T) {
	// A restricted key must not be able to bypass the model ACL by upgrading
	// to websocket, because the model identifier is only sent in later frames
	// that the middleware cannot inspect. The upgrade is rejected; the
	// downstream websocket handler never runs.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("apiKey", "sk-narrow")
		c.Next()
	})
	router.Use(ModelACLMiddleware(func() *config.Config { return cfg }))
	handlerRan := false
	router.GET("/v1/responses", func(c *gin.Context) {
		handlerRan = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for ws upgrade on restricted key, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "websocket_not_allowed_for_restricted_key") {
		t.Fatalf("expected specific error type, got %s", w.Body.String())
	}
	if handlerRan {
		t.Fatalf("downstream websocket handler must not run when upgrade is rejected")
	}
}

func TestModelACLMiddleware_WebsocketUpgradeAllowedForUnrestrictedKey(t *testing.T) {
	// A key with no policy and the default allow-all policy should still be
	// able to use websocket upgrades — there's no restriction to enforce, so
	// blocking the upgrade would be a regression. We only need at least one
	// unrelated policy in the config so the middleware does not short-circuit
	// out of policy evaluation entirely.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-unrestricted", "sk-other"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-other", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("apiKey", "sk-unrestricted")
		c.Next()
	})
	router.Use(ModelACLMiddleware(func() *config.Config { return cfg }))
	router.GET("/v1/responses", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for ws upgrade on unrestricted key, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelACLMiddleware_WebsocketUpgradeRejectedUnderDenyAllDefault(t *testing.T) {
	// Under deny-all default with no explicit policy matching the key, every
	// model is denied under the normal ACL — so the websocket upgrade, which
	// cannot be inspected, must also be denied. Otherwise the upgrade path
	// would be strictly more permissive than the JSON-body path.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyDefaultPolicy: config.APIKeyDefaultPolicyDenyAll,
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("apiKey", "sk-anything")
		c.Next()
	})
	router.Use(ModelACLMiddleware(func() *config.Config { return cfg }))
	router.GET("/v1/responses", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 under deny-all default, got %d", w.Code)
	}
}

func TestModelACLMiddleware_OversizedBodyDoesNotDrainRemainder(t *testing.T) {
	// When the body exceeds modelACLMaxBodyBytes, the middleware must return
	// 413 *without* reading the remainder. Otherwise a chunked/streamed
	// request without a trustworthy Content-Length can hold the goroutine
	// indefinitely, turning the ACL into a request-slot exhaustion path.
	//
	// We prove this by wrapping the body in a reader that blocks after N
	// bytes — the middleware must stop reading and return 413 before
	// blocking.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouter(cfg, "sk-narrow")

	// Body prefix that has no "model" field so the middleware falls through
	// from the peek to the full-body read. The prefix fills the cap; the
	// blocking reader then refuses to return EOF.
	prefix := append([]byte(`{"prompt":"`), bytes.Repeat([]byte("a"), int(modelACLMaxBodyBytes)+2048)...)
	body := &blockAfterReader{data: prefix}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	// Intentionally no ContentLength so the ContentLength short-circuit does
	// not fire; the middleware must detect oversize by reading bytes.
	req.ContentLength = -1
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-done:
		// OK — middleware returned promptly.
	case <-time.After(2 * time.Second):
		t.Fatalf("middleware did not return within 2s; appears to be draining the body")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
}

// blockAfterReader emits its `data` bytes and then blocks on any further Read
// call. It is used to prove the middleware does not attempt to drain the rest
// of a request body after detecting oversize.
type blockAfterReader struct {
	data []byte
	pos  int
}

func (r *blockAfterReader) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	// Block indefinitely — any Read past the prefix is a bug in the ACL
	// middleware.
	select {}
}

// newTestRouterWithCodexAlias wires the same middleware stack as newTestRouter
// but exposes a /backend-api/codex/responses route so tests can verify the
// alias group is also gated by ModelACLMiddleware (not just /v1).
func newTestRouterWithCodexAlias(cfg *config.Config, apiKey string) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if apiKey != "" {
			c.Set("apiKey", apiKey)
		}
		c.Next()
	})
	router.Use(ModelACLMiddleware(func() *config.Config { return cfg }))

	router.POST("/backend-api/codex/responses", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.Data(http.StatusOK, "application/json", body)
	})
	return router
}

func TestModelACLMiddleware_CodexAliasDisallowedModelRejected(t *testing.T) {
	// The /backend-api/codex/* alias group routes the same handlers as
	// /v1/responses. Without ModelACLMiddleware on this group a restricted
	// key could bypass the allowlist by calling the alias path; this test
	// pins down that the middleware is wired (or at least that the same
	// middleware, when installed, gates the alias correctly).
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouterWithCodexAlias(cfg, "sk-narrow")

	body, _ := json.Marshal(map[string]any{"model": "claude-3-5-sonnet-20241022"})
	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on codex alias for disallowed model, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "model_not_allowed_for_key") {
		t.Fatalf("expected error code in body, got %s", w.Body.String())
	}
}

func TestModelACLMiddleware_CodexAliasAllowedModelPasses(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-5*"}},
			},
		},
	}
	router := newTestRouterWithCodexAlias(cfg, "sk-narrow")

	body, _ := json.Marshal(map[string]any{"model": "gpt-5-codex"})
	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on codex alias for allowed model, got %d body=%s", w.Code, w.Body.String())
	}
}

// newTestRouterWithMultipart adds a multipart-aware echo handler so we can
// verify that the multipart body remains parseable downstream after the
// middleware extracts the "model" field.
func newTestRouterWithMultipart(cfg *config.Config, apiKey string) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if apiKey != "" {
			c.Set("apiKey", apiKey)
		}
		c.Next()
	})
	router.Use(ModelACLMiddleware(func() *config.Config { return cfg }))

	router.POST("/v1/images/edits", func(c *gin.Context) {
		// Use gin's helper which calls ParseMultipartForm; if the middleware
		// already parsed it the call is a no-op (idempotent), and the form
		// values must still be available.
		form, err := c.MultipartForm()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"model":  c.PostForm("model"),
			"prompt": c.PostForm("prompt"),
			"files":  len(form.File["image"]),
		})
	})
	return router
}

func buildMultipartBody(t *testing.T, fields map[string]string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	for k, contents := range files {
		fw, err := mw.CreateFormFile(k, k+".bin")
		if err != nil {
			t.Fatalf("create form file %s: %v", k, err)
		}
		if _, err := fw.Write(contents); err != nil {
			t.Fatalf("write form file %s: %v", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, mw.FormDataContentType()
}

func TestModelACLMiddleware_MultipartFormDataDisallowedModelRejected(t *testing.T) {
	// Image-edit-style requests carry "model" as a multipart form field,
	// not in a JSON body. The middleware must extract it from the form and
	// enforce the allowlist; otherwise restricted keys could bypass policy
	// by switching to the multipart upload path.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouterWithMultipart(cfg, "sk-narrow")

	body, ct := buildMultipartBody(t,
		map[string]string{"model": "dall-e-3", "prompt": "a cat"},
		map[string][]byte{"image": bytes.Repeat([]byte{0xAB}, 4096)},
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed model in multipart, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dall-e-3") {
		t.Fatalf("expected denied model name in error response, got %s", w.Body.String())
	}
}

func TestModelACLMiddleware_MultipartFormDataAllowedModelPasses(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-image-*"}},
			},
		},
	}
	router := newTestRouterWithMultipart(cfg, "sk-narrow")

	body, ct := buildMultipartBody(t,
		map[string]string{"model": "gpt-image-1", "prompt": "a cat"},
		map[string][]byte{"image": bytes.Repeat([]byte{0xAB}, 4096)},
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for allowed model in multipart, got %d body=%s", w.Code, w.Body.String())
	}
	// Downstream handler must still see the parsed multipart form, including
	// the file part. ParseMultipartForm is idempotent so the second call from
	// the handler returns the cached form.
	if !strings.Contains(w.Body.String(), `"model":"gpt-image-1"`) {
		t.Fatalf("downstream did not see model field: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"prompt":"a cat"`) {
		t.Fatalf("downstream did not see prompt field: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"files":1`) {
		t.Fatalf("downstream did not see file part: %s", w.Body.String())
	}
}

func TestModelACLMiddleware_MultipartWithoutModelFieldAllowed(t *testing.T) {
	// A multipart request that has no "model" field should be treated like
	// any other "no extractable model" request: allowed through (no model =>
	// no enforcement target). The route handler then enforces its own
	// requirements.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouterWithMultipart(cfg, "sk-narrow")

	body, ct := buildMultipartBody(t,
		map[string]string{"prompt": "no model field"},
		nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when no model field present, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestModelACLMiddleware_NonJSONContentTypeAllowedThrough(t *testing.T) {
	// A POST with Content-Type: application/octet-stream (or any non-JSON,
	// non-multipart media type) must NOT be buffered for "model" inspection.
	// The middleware allows the request through so the route handler sees an
	// untouched body. Without this gating, large binary uploads would be
	// buffered up to the 10 MiB cap on every request even when there is no
	// JSON "model" field to find.
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"sk-narrow"},
			APIKeyPolicies: []config.APIKeyPolicy{
				{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
			},
		},
	}
	router := newTestRouterWithCodexAlias(cfg, "sk-narrow")

	// Body is large enough that buffering would be observable; if the
	// middleware were still buffering octet-stream, this would trip the
	// 10 MiB cap and 413 instead of 200.
	payload := bytes.Repeat([]byte{0x01}, int(modelACLMaxBodyBytes)+1024)
	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for non-JSON content-type, got %d body-prefix=%q", w.Code, w.Body.String()[:min(160, w.Body.Len())])
	}
}

func TestModelACLMiddleware_JSONWithCharsetParameterStillInspected(t *testing.T) {
	// Content-Type "application/json; charset=utf-8" must be treated as JSON
	// — the middleware uses mime.ParseMediaType so parameters do not defeat
	// the dispatch. Pin this down so a tightening of the dispatch (e.g.
	// strict equality) does not silently disable enforcement on real-world
	// clients that include the charset param.
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
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (model rejected) with charset-tagged JSON, got %d", w.Code)
	}
}

func TestModelACLMiddleware_VendorJSONContentTypeInspected(t *testing.T) {
	// application/vnd.api+json (and similar +json suffixes) must dispatch to
	// the JSON path, not fall through to "unknown body shape => allow".
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
	req.Header.Set("Content-Type", "application/vnd.api+json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with +json suffix, got %d", w.Code)
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
