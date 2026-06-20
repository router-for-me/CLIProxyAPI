package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchOpenAICompatRejectsNegativeAPIKeyEntrySelectionWeight(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "compat",
			BaseURL: "https://compat.example.com",
		}},
	}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)

	body := `{"name":"compat","value":{"api-key-entries":[{"api-key":"key-a","selection-weight":-1}]}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/config/openai-compatibility", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchOpenAICompat(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
	}
	if len(cfg.OpenAICompatibility[0].APIKeyEntries) != 0 {
		t.Fatalf("api-key-entries mutated on rejected patch: %#v", cfg.OpenAICompatibility[0].APIKeyEntries)
	}
}

func TestPutConfigListsRejectNegativeSelectionWeight(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	tests := []struct {
		name   string
		path   string
		body   string
		call   func(*Handler, *gin.Context)
		assert func(*testing.T, *config.Config)
	}{
		{
			name: "gemini",
			path: "/v0/management/config/gemini-api-key",
			body: `[{"api-key":"key-a","selection-weight":-1}]`,
			call: (*Handler).PutGeminiKeys,
			assert: func(t *testing.T, cfg *config.Config) {
				if len(cfg.GeminiKey) != 0 {
					t.Fatalf("GeminiKey mutated: %#v", cfg.GeminiKey)
				}
			},
		},
		{
			name: "claude",
			path: "/v0/management/config/claude-api-key",
			body: `[{"api-key":"key-a","selection-weight":-1}]`,
			call: (*Handler).PutClaudeKeys,
			assert: func(t *testing.T, cfg *config.Config) {
				if len(cfg.ClaudeKey) != 0 {
					t.Fatalf("ClaudeKey mutated: %#v", cfg.ClaudeKey)
				}
			},
		},
		{
			name: "openai compat provider",
			path: "/v0/management/config/openai-compatibility",
			body: `[{"name":"compat","base-url":"https://compat.example.com","selection-weight":-1}]`,
			call: (*Handler).PutOpenAICompat,
			assert: func(t *testing.T, cfg *config.Config) {
				if len(cfg.OpenAICompatibility) != 0 {
					t.Fatalf("OpenAICompatibility mutated: %#v", cfg.OpenAICompatibility)
				}
			},
		},
		{
			name: "openai compat api key",
			path: "/v0/management/config/openai-compatibility",
			body: `[{"name":"compat","base-url":"https://compat.example.com","api-key-entries":[{"api-key":"key-a","selection-weight":-1}]}]`,
			call: (*Handler).PutOpenAICompat,
			assert: func(t *testing.T, cfg *config.Config) {
				if len(cfg.OpenAICompatibility) != 0 {
					t.Fatalf("OpenAICompatibility mutated: %#v", cfg.OpenAICompatibility)
				}
			},
		},
		{
			name: "vertex",
			path: "/v0/management/config/vertex-api-key",
			body: `[{"api-key":"key-a","selection-weight":-1}]`,
			call: (*Handler).PutVertexCompatKeys,
			assert: func(t *testing.T, cfg *config.Config) {
				if len(cfg.VertexCompatAPIKey) != 0 {
					t.Fatalf("VertexCompatAPIKey mutated: %#v", cfg.VertexCompatAPIKey)
				}
			},
		},
		{
			name: "codex",
			path: "/v0/management/config/codex-api-key",
			body: `[{"api-key":"key-a","base-url":"https://codex.example.com","selection-weight":-1}]`,
			call: (*Handler).PutCodexKeys,
			assert: func(t *testing.T, cfg *config.Config) {
				if len(cfg.CodexKey) != 0 {
					t.Fatalf("CodexKey mutated: %#v", cfg.CodexKey)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			h := NewHandlerWithoutConfigFilePath(cfg, nil)

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			tt.call(h, ctx)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
			}
			tt.assert(t, cfg)
		})
	}
}
