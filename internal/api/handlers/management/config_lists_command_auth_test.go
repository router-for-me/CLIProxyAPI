package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchOpenAICompatRejectedCommandAuthDoesNotMutateConfig(t *testing.T) {
	t.Parallel()

	h := &Handler{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "proxy",
		BaseURL: "https://proxy.example.com/v1",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
			APIKey: "static-key",
		}},
	}}}}
	body := map[string]any{
		"index": 0,
		"value": map[string]any{
			"auth": map[string]any{"command": "fetch-token"},
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = jsonRequestBody(t, http.MethodPatch, "/v0/management/openai-compatibility", body)

	h.PatchOpenAICompat(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	entry := h.cfg.OpenAICompatibility[0]
	if entry.Auth != nil {
		t.Fatalf("auth mutated to %#v, want nil", entry.Auth)
	}
	if got := entry.APIKeyEntries[0].APIKey; got != "static-key" {
		t.Fatalf("api-key = %q, want static-key", got)
	}
}

func TestPatchCodexRejectedCommandAuthDoesNotMutateConfig(t *testing.T) {
	t.Parallel()

	h := &Handler{cfg: &config.Config{CodexKey: []config.CodexKey{{
		APIKey:  "static-key",
		BaseURL: "https://proxy.example.com/v1",
	}}}}
	body := map[string]any{
		"index": 0,
		"value": map[string]any{
			"auth": map[string]any{"command": "fetch-token"},
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = jsonRequestBody(t, http.MethodPatch, "/v0/management/codex-api-key", body)

	h.PatchCodexKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	entry := h.cfg.CodexKey[0]
	if entry.Auth != nil {
		t.Fatalf("auth mutated to %#v, want nil", entry.Auth)
	}
	if got := entry.APIKey; got != "static-key" {
		t.Fatalf("api-key = %q, want static-key", got)
	}
}

func jsonRequestBody(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	return req
}
