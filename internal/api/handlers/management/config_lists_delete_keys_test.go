package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func writeTestConfigFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(path, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to write test config: %v", errWrite)
	}
	return path
}

func TestDeleteGeminiKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 2 {
		t.Fatalf("gemini keys len = %d, want 2", got)
	}
}

func TestDeleteGeminiKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://a.example.com", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 1 {
		t.Fatalf("gemini keys len = %d, want 1", got)
	}
	if got := h.cfg.GeminiKey[0].BaseURL; got != "https://b.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://b.example.com")
	}
}

func TestDeleteGeminiStyleKeyRejectsAmbiguousRoutingIdentity(t *testing.T) {
	tests := []struct {
		name         string
		interactions bool
	}{
		{name: "Gemini"},
		{name: "Interactions", interactions: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-a"},
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-b"},
			}
			cfg := &config.Config{}
			path := "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://shared.example.com"
			if tc.interactions {
				cfg.InteractionsKey = entries
				path = "/v0/management/interactions-api-key?api-key=shared-key&base-url=https://shared.example.com"
			} else {
				cfg.GeminiKey = entries
			}
			handler := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodDelete, path, nil)

			if tc.interactions {
				handler.DeleteInteractionsKey(ctx)
			} else {
				handler.DeleteGeminiKey(ctx)
			}

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			remaining := cfg.GeminiKey
			if tc.interactions {
				remaining = cfg.InteractionsKey
			}
			if len(remaining) != 2 {
				t.Fatalf("remaining credential count = %d, want 2", len(remaining))
			}
		})
	}
}

func TestPatchGeminiStyleKeyRoutingIdentity(t *testing.T) {
	tests := []struct {
		name         string
		interactions bool
		firstBase    string
		wantStatus   int
	}{
		{name: "Gemini unique base URL", firstBase: "https://first.example.com", wantStatus: http.StatusOK},
		{name: "Gemini ambiguous base URL", firstBase: "https://shared.example.com", wantStatus: http.StatusBadRequest},
		{name: "Interactions unique base URL", interactions: true, firstBase: "https://first.example.com", wantStatus: http.StatusOK},
		{name: "Interactions ambiguous base URL", interactions: true, firstBase: "https://shared.example.com", wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: tc.firstBase, Prefix: "team-a"},
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-b"},
			}
			cfg := &config.Config{}
			path := "/v0/management/gemini-api-key?base-url=https://shared.example.com"
			if tc.interactions {
				cfg.InteractionsKey = entries
				path = "/v0/management/interactions-api-key?base-url=https://shared.example.com"
			} else {
				cfg.GeminiKey = entries
			}
			handler := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"match":"shared-key","value":{"prefix":"updated"}}`))

			if tc.interactions {
				handler.PatchInteractionsKey(ctx)
			} else {
				handler.PatchGeminiKey(ctx)
			}

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			remaining := cfg.GeminiKey
			if tc.interactions {
				remaining = cfg.InteractionsKey
			}
			if tc.wantStatus == http.StatusOK {
				if remaining[0].Prefix != "team-a" || remaining[1].Prefix != "updated" {
					t.Fatalf("prefixes = %q, %q; want team-a, updated", remaining[0].Prefix, remaining[1].Prefix)
				}
			} else if remaining[0].Prefix != "team-a" || remaining[1].Prefix != "team-b" {
				t.Fatalf("ambiguous patch changed prefixes to %q, %q", remaining[0].Prefix, remaining[1].Prefix)
			}
		})
	}
}

func TestDeleteClaudeKey_DeletesEmptyBaseURLWhenExplicitlyProvided(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: ""},
				{APIKey: "shared-key", BaseURL: "https://claude.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/claude-api-key?api-key=shared-key&base-url=", nil)

	h.DeleteClaudeKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude keys len = %d, want 1", got)
	}
	if got := h.cfg.ClaudeKey[0].BaseURL; got != "https://claude.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://claude.example.com")
	}
}

func TestDeleteVertexCompatKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/vertex-api-key?api-key=shared-key&base-url=https://b.example.com", nil)

	h.DeleteVertexCompatKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.VertexCompatAPIKey); got != 1 {
		t.Fatalf("vertex keys len = %d, want 1", got)
	}
	if got := h.cfg.VertexCompatAPIKey[0].BaseURL; got != "https://a.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://a.example.com")
	}
}

func TestDeleteXAIKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			XAIKey: []config.XAIKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/xai-api-key?api-key=shared-key", nil)

	h.DeleteXAIKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.XAIKey); got != 2 {
		t.Fatalf("xAI keys len = %d, want 2", got)
	}
}

func TestDeleteCodexKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/codex-api-key?api-key=shared-key", nil)

	h.DeleteCodexKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.CodexKey); got != 2 {
		t.Fatalf("codex keys len = %d, want 2", got)
	}
}
