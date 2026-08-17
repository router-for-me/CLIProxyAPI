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

func TestPatchDisableCoolingOverrideForEveryFamily(t *testing.T) {
	initial := true
	tests := []struct {
		name  string
		setup func(*config.Config)
		patch func(*Handler, *gin.Context)
		get   func(*config.Config) *bool
	}{
		{
			name: "gemini",
			setup: func(cfg *config.Config) {
				cfg.GeminiKey = []config.GeminiKey{{APIKey: "key", DisableCooling: &initial}}
			},
			patch: (*Handler).PatchGeminiKey,
			get:   func(cfg *config.Config) *bool { return cfg.GeminiKey[0].DisableCooling },
		},
		{
			name: "interactions",
			setup: func(cfg *config.Config) {
				cfg.InteractionsKey = []config.GeminiKey{{APIKey: "key", DisableCooling: &initial}}
			},
			patch: (*Handler).PatchInteractionsKey,
			get:   func(cfg *config.Config) *bool { return cfg.InteractionsKey[0].DisableCooling },
		},
		{
			name: "claude",
			setup: func(cfg *config.Config) {
				cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "key", DisableCooling: &initial}}
			},
			patch: (*Handler).PatchClaudeKey,
			get:   func(cfg *config.Config) *bool { return cfg.ClaudeKey[0].DisableCooling },
		},
		{
			name: "openai compatibility",
			setup: func(cfg *config.Config) {
				cfg.OpenAICompatibility = []config.OpenAICompatibility{{
					Name:           "compat",
					BaseURL:        "https://compat.example.com",
					APIKeyEntries:  []config.OpenAICompatibilityAPIKey{{APIKey: "key"}},
					DisableCooling: &initial,
				}}
			},
			patch: (*Handler).PatchOpenAICompat,
			get:   func(cfg *config.Config) *bool { return cfg.OpenAICompatibility[0].DisableCooling },
		},
		{
			name: "vertex",
			setup: func(cfg *config.Config) {
				cfg.VertexCompatAPIKey = []config.VertexCompatKey{{
					APIKey:         "key",
					BaseURL:        "https://vertex.example.com",
					DisableCooling: &initial,
				}}
			},
			patch: (*Handler).PatchVertexCompatKey,
			get:   func(cfg *config.Config) *bool { return cfg.VertexCompatAPIKey[0].DisableCooling },
		},
		{
			name: "codex",
			setup: func(cfg *config.Config) {
				cfg.CodexKey = []config.CodexKey{{
					APIKey:         "key",
					BaseURL:        "https://codex.example.com",
					DisableCooling: &initial,
				}}
			},
			patch: (*Handler).PatchCodexKey,
			get:   func(cfg *config.Config) *bool { return cfg.CodexKey[0].DisableCooling },
		},
		{
			name: "xai",
			setup: func(cfg *config.Config) {
				cfg.XAIKey = []config.XAIKey{{
					APIKey:         "key",
					BaseURL:        "https://api.x.ai/v1",
					DisableCooling: &initial,
				}}
			},
			patch: (*Handler).PatchXAIKey,
			get:   func(cfg *config.Config) *bool { return cfg.XAIKey[0].DisableCooling },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			tc.setup(cfg)
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

			patch := func(value string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				body := fmt.Sprintf(`{"index":0,"value":{"disable-cooling":%s}}`, value)
				ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/key", strings.NewReader(body))
				ctx.Request.Header.Set("Content-Type", "application/json")
				tc.patch(h, ctx)
				return rec
			}

			if rec := patch("false"); rec.Code != http.StatusOK {
				t.Fatalf("false patch status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if override := tc.get(cfg); override == nil || *override {
				t.Fatalf("disable-cooling = %v, want explicit false", override)
			}

			if rec := patch("null"); rec.Code != http.StatusOK {
				t.Fatalf("null patch status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if override := tc.get(cfg); override != nil {
				t.Fatalf("disable-cooling = %v, want inherited value", override)
			}
		})
	}
}
