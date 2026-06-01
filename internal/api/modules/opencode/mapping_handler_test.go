package opencode

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

// captureHandler returns a gin handler that records the request body it observes.
func captureHandler(seen *[]byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		*seen = body
		c.Status(http.StatusOK)
	}
}

func doPost(r *gin.Engine, path, body string) {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)
}

func TestMappingHandler_RewritesModelWhenMapped(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("opencode-mh-1", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("opencode-mh-1")

	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "claude-sonnet-4-5", To: "claude-sonnet-4"},
	})
	mh := NewMappingHandler(mapper, func() bool { return false })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var seen []byte
	r.POST("/opencode/v1/messages", mh.Wrap(captureHandler(&seen)))

	doPost(r, "/opencode/v1/messages", `{"model":"claude-sonnet-4-5","max_tokens":1}`)

	if got := gjson.GetBytes(seen, "model").String(); got != "claude-sonnet-4" {
		t.Fatalf("expected model rewritten to claude-sonnet-4, got %q", got)
	}
}

func TestMappingHandler_PassthroughWhenNoMapping(t *testing.T) {
	mapper := NewModelMapper(nil)
	mh := NewMappingHandler(mapper, func() bool { return false })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var seen []byte
	r.POST("/opencode/v1/chat/completions", mh.Wrap(captureHandler(&seen)))

	doPost(r, "/opencode/v1/chat/completions", `{"model":"some-unmapped-model","messages":[]}`)

	if got := gjson.GetBytes(seen, "model").String(); got != "some-unmapped-model" {
		t.Fatalf("expected model untouched, got %q", got)
	}
}

func TestMappingHandler_ForceModeOverridesLocalProvider(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	// Both source and target models have providers; force mode should still remap.
	reg.RegisterClient("opencode-mh-2", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5", OwnedBy: "anthropic", Type: "claude"},
		{ID: "gpt-5", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("opencode-mh-2")

	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "claude-sonnet-4-5", To: "gpt-5"},
	})

	gin.SetMode(gin.TestMode)

	// Default mode: source has a local provider, so no remap.
	defaultMH := NewMappingHandler(mapper, func() bool { return false })
	rDefault := gin.New()
	var seenDefault []byte
	rDefault.POST("/p", defaultMH.Wrap(captureHandler(&seenDefault)))
	doPost(rDefault, "/p", `{"model":"claude-sonnet-4-5"}`)
	if got := gjson.GetBytes(seenDefault, "model").String(); got != "claude-sonnet-4-5" {
		t.Fatalf("default mode: expected model untouched, got %q", got)
	}

	// Force mode: mapping wins even though the source has a local provider.
	forceMH := NewMappingHandler(mapper, func() bool { return true })
	rForce := gin.New()
	var seenForce []byte
	rForce.POST("/p", forceMH.Wrap(captureHandler(&seenForce)))
	doPost(rForce, "/p", `{"model":"claude-sonnet-4-5"}`)
	if got := gjson.GetBytes(seenForce, "model").String(); got != "gpt-5" {
		t.Fatalf("force mode: expected model remapped to gpt-5, got %q", got)
	}
}

func TestMappingHandler_GeminiPathModelExtraction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	captured := ""
	r.POST("/opencode/v1beta/models/*action", func(c *gin.Context) {
		captured = extractModelFromRequest(nil, c)
		c.Status(http.StatusOK)
	})
	doPost(r, "/opencode/v1beta/models/gemini-2.5-pro:generateContent", "")
	if captured != "gemini-2.5-pro" {
		t.Fatalf("expected gemini-2.5-pro extracted from path, got %q", captured)
	}
}
