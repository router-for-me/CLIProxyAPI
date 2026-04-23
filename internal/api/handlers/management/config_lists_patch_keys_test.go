package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestPatchGeminiKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com", Prefix: "a"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com", Prefix: "b"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/gemini-api-key", bytes.NewBufferString(`{"match":"shared-key","value":{"prefix":"updated"}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchGeminiKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := h.cfg.GeminiKey[0].Prefix; got != "a" {
		t.Fatalf("first prefix = %q, want %q", got, "a")
	}
	if got := h.cfg.GeminiKey[1].Prefix; got != "b" {
		t.Fatalf("second prefix = %q, want %q", got, "b")
	}
}

func TestPatchGeminiKey_MatchesDuplicateAPIKeyByBaseURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com", Prefix: "a"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com", Prefix: "b"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/gemini-api-key", bytes.NewBufferString(`{"match":"shared-key","base-url":"https://b.example.com","value":{"prefix":"updated"}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchGeminiKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.GeminiKey[0].Prefix; got != "a" {
		t.Fatalf("first prefix = %q, want %q", got, "a")
	}
	if got := h.cfg.GeminiKey[1].Prefix; got != "updated" {
		t.Fatalf("second prefix = %q, want %q", got, "updated")
	}
}

func TestPatchClaudeKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com", Prefix: "a"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com", Prefix: "b"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key", bytes.NewBufferString(`{"match":"shared-key","value":{"prefix":"updated"}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchClaudeKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestPatchClaudeKey_RejectsDuplicateAPIKeyAndBaseURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: "https://same.example.com", Prefix: "a"},
				{APIKey: "shared-key", BaseURL: "https://same.example.com", Prefix: "b"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key", bytes.NewBufferString(`{"match":"shared-key","base-url":"https://same.example.com","value":{"prefix":"updated"}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchClaudeKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].Prefix; got != "a" {
		t.Fatalf("first prefix = %q, want %q", got, "a")
	}
	if got := h.cfg.ClaudeKey[1].Prefix; got != "b" {
		t.Fatalf("second prefix = %q, want %q", got, "b")
	}
}

func TestPatchVertexCompatKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com", Prefix: "a"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com", Prefix: "b"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/vertex-api-key", bytes.NewBufferString(`{"match":"shared-key","value":{"prefix":"updated"}}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchVertexCompatKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestDeleteOpenAICompat_ReturnsNotFoundWhenNameMissing(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{Name: "existing", BaseURL: "https://api.example.com"}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/openai-compatibility?name=missing", nil)

	h.DeleteOpenAICompat(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := len(h.cfg.OpenAICompatibility); got != 1 {
		t.Fatalf("openai compatibility len = %d, want 1", got)
	}
}

func TestDeleteOpenAICompat_TrimsNameQuery(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{Name: "existing", BaseURL: "https://api.example.com"}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/openai-compatibility?name=%20existing%20", nil)

	h.DeleteOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.OpenAICompatibility); got != 0 {
		t.Fatalf("openai compatibility len = %d, want 0", got)
	}
}
