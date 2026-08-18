package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchClaudeKeyFingerprintProfile(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "test-claude-key"},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	// Patch fingerprint-profile to claude-code-cli
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"fingerprint-profile":"claude-code-cli"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[0].FingerprintProfile; got != "claude-code-cli" {
		t.Fatalf("FingerprintProfile = %q, want %q", got, "claude-code-cli")
	}

	// Patch fingerprint-profile back to empty
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"fingerprint-profile":""}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[0].FingerprintProfile; got != "" {
		t.Fatalf("FingerprintProfile = %q, want empty", got)
	}
}
