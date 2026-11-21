package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	management "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Models of an API key from config.yaml are registered under auth.ID, so the
// v8 management models route finds them by the auth_index the auth list shows.
func TestConfigAPIKeyModelsListedByAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	cfg := &config.Config{
		GeminiKey:       []config.GeminiKey{{APIKey: "k-gemini", Models: []config.GeminiModel{{Name: "gemini-cfg"}}}},
		InteractionsKey: []config.GeminiKey{{APIKey: "k-interactions", Models: []config.GeminiModel{{Name: "interactions-cfg"}}}},
		ClaudeKey:       []config.ClaudeKey{{APIKey: "k-claude", Models: []config.ClaudeModel{{Name: "claude-cfg"}}}},
		CodexKey:        []config.CodexKey{{APIKey: "k-codex", BaseURL: "https://codex.example", Models: []config.CodexModel{{Name: "codex-cfg"}}}},
		XAIKey:          []config.XAIKey{{APIKey: "k-xai", BaseURL: "https://xai.example", Models: []config.XAIModel{{Name: "xai-cfg"}}}},
		MetaKey:         []config.MetaKey{{APIKey: "k-meta", BaseURL: "https://meta.example", Models: []config.MetaModel{{Name: "meta-cfg"}}}},
		VertexCompatAPIKey: []config.VertexCompatKey{{
			APIKey: "k-vertex", BaseURL: "https://vertex.example", Models: []config.VertexCompatModel{{Name: "vertex-cfg"}},
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat", BaseURL: "https://compat.example",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k-compat"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "compat-cfg"}},
		}},
	}
	want := map[string]string{
		"gemini": "gemini-cfg", "gemini-interactions": "interactions-cfg", "claude": "claude-cfg",
		"codex": "codex-cfg", "xai": "xai-cfg", "meta": "meta-cfg", "vertex": "vertex-cfg", "compat": "compat-cfg",
	}

	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil || len(auths) != len(want) {
		t.Fatalf("synthesize: err=%v, auths=%d, want %d", err, len(auths), len(want))
	}

	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	h := management.NewHandlerWithoutConfigFilePath(cfg, manager)
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		service.registerModelsForAuth(context.Background(), auth)
	}

	for _, auth := range auths {
		model := want[auth.Provider]
		if model == "" {
			model = want[auth.Label]
		}
		if auth.FileName != "" || model == "" {
			t.Fatalf("auth %s (provider %q, label %q): FileName=%q, want a config credential in the table", auth.ID, auth.Provider, auth.Label, auth.FileName)
		}
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials/models?auth_index="+url.QueryEscape(auth.EnsureIndex()), nil)
		h.GetAuthFileModelsV8(ctx)
		var resp struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s err=%v", auth.Provider, rec.Code, rec.Body.String(), err)
		}
		if len(resp.Models) != 1 || resp.Models[0].ID != model {
			t.Fatalf("%s (%s): models = %s, want [%s]", auth.Provider, auth.ID, rec.Body.String(), model)
		}
	}
}
