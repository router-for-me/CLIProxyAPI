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

func TestPatchCommandAuthRejectedDoesNotMutateConfigProviders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		h      *Handler
		target string
		patch  func(*Handler, *gin.Context)
		check  func(*testing.T, *Handler)
	}{
		{
			name:   "gemini",
			h:      &Handler{cfg: &config.Config{GeminiKey: []config.GeminiKey{{APIKey: "static-key"}}}},
			target: "/v0/management/gemini-api-key",
			patch:  (*Handler).PatchGeminiKey,
			check: func(t *testing.T, h *Handler) {
				entry := h.cfg.GeminiKey[0]
				if entry.Auth != nil || entry.APIKey != "static-key" {
					t.Fatalf("gemini entry mutated to %#v", entry)
				}
			},
		},
		{
			name:   "claude",
			h:      &Handler{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "static-key"}}}},
			target: "/v0/management/claude-api-key",
			patch:  (*Handler).PatchClaudeKey,
			check: func(t *testing.T, h *Handler) {
				entry := h.cfg.ClaudeKey[0]
				if entry.Auth != nil || entry.APIKey != "static-key" {
					t.Fatalf("claude entry mutated to %#v", entry)
				}
			},
		},
		{
			name:   "vertex",
			h:      &Handler{cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "static-key"}}}},
			target: "/v0/management/vertex-api-key",
			patch:  (*Handler).PatchVertexCompatKey,
			check: func(t *testing.T, h *Handler) {
				entry := h.cfg.VertexCompatAPIKey[0]
				if entry.Auth != nil || entry.APIKey != "static-key" {
					t.Fatalf("vertex entry mutated to %#v", entry)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := map[string]any{
				"index": 0,
				"value": map[string]any{
					"auth": map[string]any{"command": "fetch-token"},
				},
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = jsonRequestBody(t, http.MethodPatch, tt.target, body)

			tt.patch(tt.h, c)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			tt.check(t, tt.h)
		})
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
