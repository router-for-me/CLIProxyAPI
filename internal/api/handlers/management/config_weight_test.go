package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchAPIKeyWeightForEveryFamily(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*config.Config)
		patch func(*Handler, *gin.Context)
		get   func(*config.Config) *int
	}{
		{name: "gemini", setup: func(cfg *config.Config) { cfg.GeminiKey = []config.GeminiKey{{APIKey: "key"}} }, patch: (*Handler).PatchGeminiKey, get: func(cfg *config.Config) *int { return cfg.GeminiKey[0].Weight }},
		{name: "interactions", setup: func(cfg *config.Config) { cfg.InteractionsKey = []config.GeminiKey{{APIKey: "key"}} }, patch: (*Handler).PatchInteractionsKey, get: func(cfg *config.Config) *int { return cfg.InteractionsKey[0].Weight }},
		{name: "claude", setup: func(cfg *config.Config) { cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "key"}} }, patch: (*Handler).PatchClaudeKey, get: func(cfg *config.Config) *int { return cfg.ClaudeKey[0].Weight }},
		{name: "vertex", setup: func(cfg *config.Config) {
			cfg.VertexCompatAPIKey = []config.VertexCompatKey{{APIKey: "key", BaseURL: "https://example.com"}}
		}, patch: (*Handler).PatchVertexCompatKey, get: func(cfg *config.Config) *int { return cfg.VertexCompatAPIKey[0].Weight }},
		{name: "codex", setup: func(cfg *config.Config) {
			cfg.CodexKey = []config.CodexKey{{APIKey: "key", BaseURL: "https://example.com"}}
		}, patch: (*Handler).PatchCodexKey, get: func(cfg *config.Config) *int { return cfg.CodexKey[0].Weight }},
		{name: "xai", setup: func(cfg *config.Config) {
			cfg.XAIKey = []config.XAIKey{{APIKey: "key", BaseURL: "https://example.com"}}
		}, patch: (*Handler).PatchXAIKey, get: func(cfg *config.Config) *int { return cfg.XAIKey[0].Weight }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			test.setup(cfg)
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/key", strings.NewReader(`{"index":0,"value":{"weight":7}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if weight := test.get(cfg); weight == nil || *weight != 7 {
				t.Fatalf("weight = %v, want 7", weight)
			}
		})
	}
}

func TestPatchAPIKeyWeightResetAndStrictValidation(t *testing.T) {
	initial := 5
	cfg := &config.Config{GeminiKey: []config.GeminiKey{{APIKey: "key", Weight: &initial}}}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	patch := func(raw string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		body := fmt.Sprintf(`{"index":0,"value":{"weight":%s}}`, raw)
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/gemini-api-key", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.PatchGeminiKey(ctx)
		return rec
	}

	for _, invalid := range []string{"1.5", "1000001", "9223372036854775808", `"7"`} {
		rec := patch(invalid)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("weight %s status = %d, want 400; body=%s", invalid, rec.Code, rec.Body.String())
		}
		if cfg.GeminiKey[0].Weight == nil || *cfg.GeminiKey[0].Weight != initial {
			t.Fatalf("invalid weight %s changed config", invalid)
		}
	}

	if rec := patch("null"); rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if cfg.GeminiKey[0].Weight != nil {
		t.Fatalf("reset weight = %v, want nil default", cfg.GeminiKey[0].Weight)
	}
}

func TestPutAPIKeyWeightRejectsAboveMaximum(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/gemini-api-key", strings.NewReader(`[{"api-key":"key","weight":1000001}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutGeminiKeys(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.GeminiKey) != 0 {
		t.Fatal("invalid PUT changed config")
	}
}
