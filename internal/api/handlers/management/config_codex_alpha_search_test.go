package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchCodexKeyUpdatesAlphaSearch(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{CodexKey: []config.CodexKey{{
			APIKey:  "codex-key",
			BaseURL: "https://codex.example.com",
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", strings.NewReader(`{"index":0,"value":{"alpha-search":true}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchCodexKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !h.cfg.CodexKey[0].AlphaSearch {
		t.Fatal("alpha-search = false, want true")
	}
}
