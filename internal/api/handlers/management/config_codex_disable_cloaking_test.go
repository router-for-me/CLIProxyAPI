package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchCodexKeyUpdatesDisableCodexCloaking(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{CodexKey: []config.CodexKey{{
			APIKey:  "codex-key",
			BaseURL: "https://codex.example.com",
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	// 1. Patch to true
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", strings.NewReader(`{"index":0,"value":{"disable-codex-cloaking":true}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchCodexKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.CodexKey[0].DisableCodexCloaking == nil || !*h.cfg.CodexKey[0].DisableCodexCloaking {
		t.Fatalf("disable-codex-cloaking = %v, want true", h.cfg.CodexKey[0].DisableCodexCloaking)
	}

	// 2. Patch to false
	rec2 := httptest.NewRecorder()
	ctx2, _ := gin.CreateTestContext(rec2)
	ctx2.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", strings.NewReader(`{"index":0,"value":{"disable-codex-cloaking":false}}`))
	ctx2.Request.Header.Set("Content-Type", "application/json")
	h.PatchCodexKey(ctx2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}
	if h.cfg.CodexKey[0].DisableCodexCloaking == nil || *h.cfg.CodexKey[0].DisableCodexCloaking {
		t.Fatalf("disable-codex-cloaking = %v, want false", h.cfg.CodexKey[0].DisableCodexCloaking)
	}

	// 3. Patch to null (inherit)
	rec3 := httptest.NewRecorder()
	ctx3, _ := gin.CreateTestContext(rec3)
	ctx3.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", strings.NewReader(`{"index":0,"value":{"disable-codex-cloaking":null}}`))
	ctx3.Request.Header.Set("Content-Type", "application/json")
	h.PatchCodexKey(ctx3)

	if rec3.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec3.Code, http.StatusOK, rec3.Body.String())
	}
	if h.cfg.CodexKey[0].DisableCodexCloaking != nil {
		t.Fatalf("disable-codex-cloaking = %v, want nil", h.cfg.CodexKey[0].DisableCodexCloaking)
	}

	// 4. Patch with invalid non-boolean type
	rec4 := httptest.NewRecorder()
	ctx4, _ := gin.CreateTestContext(rec4)
	ctx4.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", strings.NewReader(`{"index":0,"value":{"disable-codex-cloaking":"not-a-bool"}}`))
	ctx4.Request.Header.Set("Content-Type", "application/json")
	h.PatchCodexKey(ctx4)

	if rec4.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec4.Code, http.StatusBadRequest, rec4.Body.String())
	}
}
