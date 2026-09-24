package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchPriorityForEveryProvider(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*config.Config)
		patch    func(*Handler, *gin.Context)
		endpoint string
		get      func(*config.Config) int
	}{
		{
			name:     "claude",
			setup:    func(cfg *config.Config) { cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "key"}} },
			patch:    (*Handler).PatchClaudeKey,
			endpoint: "/v0/management/claude-api-key",
			get:      func(cfg *config.Config) int { return cfg.ClaudeKey[0].Priority },
		},
		{
			name: "xai",
			setup: func(cfg *config.Config) {
				cfg.XAIKey = []config.XAIKey{{APIKey: "key", BaseURL: "https://example.com"}}
			},
			patch:    (*Handler).PatchXAIKey,
			endpoint: "/v0/management/xai-api-key",
			get:      func(cfg *config.Config) int { return cfg.XAIKey[0].Priority },
		},
		{
			name: "meta",
			setup: func(cfg *config.Config) {
				cfg.MetaKey = []config.MetaKey{{APIKey: "key", BaseURL: "https://example.com"}}
			},
			patch:    (*Handler).PatchMetaKey,
			endpoint: "/v0/management/meta-api-key",
			get:      func(cfg *config.Config) int { return cfg.MetaKey[0].Priority },
		},
		{
			name: "codex",
			setup: func(cfg *config.Config) {
				cfg.CodexKey = []config.CodexKey{{APIKey: "key", BaseURL: "https://example.com"}}
			},
			patch:    (*Handler).PatchCodexKey,
			endpoint: "/v0/management/codex-api-key",
			get:      func(cfg *config.Config) int { return cfg.CodexKey[0].Priority },
		},
		{
			name:     "gemini",
			setup:    func(cfg *config.Config) { cfg.GeminiKey = []config.GeminiKey{{APIKey: "key"}} },
			patch:    (*Handler).PatchGeminiKey,
			endpoint: "/v0/management/gemini-api-key",
			get:      func(cfg *config.Config) int { return cfg.GeminiKey[0].Priority },
		},
		{
			name:     "interactions",
			setup:    func(cfg *config.Config) { cfg.InteractionsKey = []config.GeminiKey{{APIKey: "key"}} },
			patch:    (*Handler).PatchInteractionsKey,
			endpoint: "/v0/management/interactions-api-key",
			get:      func(cfg *config.Config) int { return cfg.InteractionsKey[0].Priority },
		},
		{
			name: "vertex",
			setup: func(cfg *config.Config) {
				cfg.VertexCompatAPIKey = []config.VertexCompatKey{{APIKey: "key", BaseURL: "https://example.com"}}
			},
			patch:    (*Handler).PatchVertexCompatKey,
			endpoint: "/v0/management/vertex-api-key",
			get:      func(cfg *config.Config) int { return cfg.VertexCompatAPIKey[0].Priority },
		},
		{
			name: "openai compatibility",
			setup: func(cfg *config.Config) {
				cfg.OpenAICompatibility = []config.OpenAICompatibility{{
					Name:    "compat",
					BaseURL: "https://compat.example.com",
				}}
			},
			patch:    (*Handler).PatchOpenAICompat,
			endpoint: "/v0/management/openai-compatibility",
			get:      func(cfg *config.Config) int { return cfg.OpenAICompatibility[0].Priority },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			test.setup(cfg)
			configFile := writeTestConfigFile(t)
			h := &Handler{cfg: cfg, configFilePath: configFile}

			// Update priority to 7
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, test.endpoint, strings.NewReader(`{"index":0,"value":{"priority":7}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := test.get(cfg); got != 7 {
				t.Fatalf("priority = %d, want 7", got)
			}

			// Verify priority persisted to YAML
			savedBytes, errRead := os.ReadFile(configFile)
			if errRead != nil {
				t.Fatalf("os.ReadFile() error = %v", errRead)
			}
			if !strings.Contains(string(savedBytes), "priority: 7") {
				t.Fatalf("saved YAML missing 'priority: 7':\n%s", string(savedBytes))
			}

			// Omitting priority preserves existing priority
			rec = httptest.NewRecorder()
			ctx, _ = gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, test.endpoint, strings.NewReader(`{"index":0,"value":{"prefix":"team-a"}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("omit priority: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := test.get(cfg); got != 7 {
				t.Fatalf("preserved priority = %d, want 7", got)
			}

			// Reset priority to 0 explicitly
			rec = httptest.NewRecorder()
			ctx, _ = gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, test.endpoint, strings.NewReader(`{"index":0,"value":{"priority":0}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("reset priority: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := test.get(cfg); got != 0 {
				t.Fatalf("reset priority = %d, want 0", got)
			}
		})
	}
}
